// Package proxy implements an egress allowlist forward-proxy.
//
// It replaces a previously used gost sidecar. The proxy behaves as an HTTP
// forward-proxy: only hosts matching the allowlist (a configured domain itself
// or any of its subdomains) may be reached; every other request is rejected
// with HTTP 403 Forbidden.
//
// The agent container runs on an --internal network with HTTP_PROXY/HTTPS_PROXY
// pointed at this proxy, making the proxy the sole egress path and the single
// point where the domain allowlist is enforced.
//
// HTTPS traffic is handled via CONNECT tunneling (the proxy only inspects the
// requested host, never the encrypted payload), while plain HTTP requests are
// forwarded through a shared *http.Transport.
package proxy

import (
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// hopByHopHeaders are connection-specific headers that must not be forwarded
// between the client and the upstream origin.
var hopByHopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// Allowed reports whether host is permitted by the allowlist.
//
// Any :port suffix is stripped and the comparison is case-insensitive. A host
// matches when, for some domain d in domains, host == d or host ends with
// "." + d (i.e. it is a subdomain of d). An empty domains list allows nothing,
// and an empty host is always rejected.
func Allowed(domains []string, host string) bool {
	if host == "" {
		return false
	}
	// Strip any :port suffix. net.SplitHostPort fails when there is no port,
	// in which case we keep the original host.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// Handler is an http.Handler implementing the allowlist forward-proxy.
type Handler struct {
	domains   []string
	transport *http.Transport
	// dialTimeout bounds how long an upstream TCP dial may take.
	dialTimeout time.Duration
}

// New returns a Handler enforcing the given domain allowlist.
func New(domains []string) *Handler {
	cp := make([]string, len(domains))
	copy(cp, domains)
	return &Handler{
		domains:     cp,
		dialTimeout: 30 * time.Second,
		transport: &http.Transport{
			Proxy: nil,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     false,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

// ServeHTTP dispatches CONNECT requests to the tunnel handler and everything
// else to the plain HTTP forwarder.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		h.handleConnect(w, r)
		return
	}
	h.handleHTTP(w, r)
}

// targetHost returns the host[:port] target for the request, preferring
// r.URL.Host (set on absolute-form proxy requests) and falling back to r.Host.
func targetHost(r *http.Request) string {
	if r.URL != nil && r.URL.Host != "" {
		return r.URL.Host
	}
	return r.Host
}

// handleConnect establishes a CONNECT tunnel to an allowed host.
func (h *Handler) handleConnect(w http.ResponseWriter, r *http.Request) {
	target := targetHost(r)
	if !Allowed(h.domains, target) {
		http.Error(w, "403 Forbidden: host not in egress allowlist", http.StatusForbidden)
		return
	}

	// Ensure there is a port; CONNECT targets almost always carry one, but
	// default to 443 (HTTPS) when absent.
	dialAddr := target
	if _, _, err := net.SplitHostPort(dialAddr); err != nil {
		dialAddr = net.JoinHostPort(dialAddr, "443")
	}

	upstream, err := net.DialTimeout("tcp", dialAddr, h.dialTimeout)
	if err != nil {
		http.Error(w, "502 Bad Gateway: "+err.Error(), http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "proxy does not support hijacking", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		// The connection may already be in an unusable state; best-effort only.
		http.Error(w, "502 Bad Gateway: "+err.Error(), http.StatusBadGateway)
		return
	}

	if _, err := io.WriteString(clientConn, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		_ = clientConn.Close()
		_ = upstream.Close()
		return
	}

	// Pipe both directions until either side closes, then tear everything down.
	done := make(chan struct{}, 2)
	go pipe(upstream, clientConn, done)
	go pipe(clientConn, upstream, done)
	<-done
	_ = clientConn.Close()
	_ = upstream.Close()
}

// pipe copies from src to dst, signalling done when the copy finishes.
func pipe(dst io.Writer, src io.Reader, done chan<- struct{}) {
	_, _ = io.Copy(dst, src)
	done <- struct{}{}
}

// handleHTTP forwards a plain (non-CONNECT) HTTP proxy request to the origin
// when its host is allowed.
func (h *Handler) handleHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Host
	if host == "" {
		host = r.Host
	}
	if !Allowed(h.domains, host) {
		http.Error(w, "403 Forbidden: host not in egress allowlist", http.StatusForbidden)
		return
	}

	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""

	// Proxy requests sometimes arrive in origin form once the host is known;
	// make sure the outbound URL is absolute so Transport can route it.
	if outReq.URL.Scheme == "" {
		outReq.URL.Scheme = "http"
	}
	if outReq.URL.Host == "" {
		outReq.URL.Host = r.Host
	}

	removeHopByHop(outReq.Header)

	resp, err := h.transport.RoundTrip(outReq)
	if err != nil {
		http.Error(w, "502 Bad Gateway: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	removeHopByHop(resp.Header)
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// removeHopByHop deletes hop-by-hop headers, including any headers named in the
// Connection header per RFC 7230.
func removeHopByHop(h http.Header) {
	for _, conn := range h.Values("Connection") {
		for _, name := range strings.Split(conn, ",") {
			if name = strings.TrimSpace(name); name != "" {
				h.Del(name)
			}
		}
	}
	for _, hdr := range hopByHopHeaders {
		h.Del(hdr)
	}
}

// copyHeader appends all values from src into dst.
func copyHeader(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// ListenAndServe runs the allowlist proxy on addr (default ":8888") enforcing
// the given domain allowlist. This is the entry point the `sandboxer _proxy`
// mode is expected to call.
func ListenAndServe(addr string, domains []string) error {
	if addr == "" {
		addr = ":8888"
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           New(domains),
		ReadHeaderTimeout: 30 * time.Second,
	}
	return srv.ListenAndServe()
}

// compile-time assertion that Handler satisfies http.Handler.
var _ http.Handler = (*Handler)(nil)
