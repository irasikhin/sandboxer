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
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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
	// upstream, when non-nil, is a parent proxy this proxy chains every allowed
	// request through (the allowlist is still enforced first). nil = dial origins
	// directly.
	upstream *url.URL
	// dialTimeout bounds how long an upstream TCP dial may take.
	dialTimeout time.Duration
}

// New returns a Handler enforcing the given domain allowlist, dialing origins
// directly.
func New(domains []string) *Handler { return NewWithUpstream(domains, nil) }

// NewWithUpstream returns a Handler enforcing the allowlist and, when upstream
// is non-nil, chaining every allowed request through that parent proxy. The
// allowlist is enforced before any upstream connection is made, so chaining
// never widens what the agent may reach.
func NewWithUpstream(domains []string, upstream *url.URL) *Handler {
	cp := make([]string, len(domains))
	copy(cp, domains)
	var proxyFn func(*http.Request) (*url.URL, error)
	if upstream != nil {
		proxyFn = http.ProxyURL(upstream)
	}
	return &Handler{
		domains:     cp,
		upstream:    upstream,
		dialTimeout: 30 * time.Second,
		transport: &http.Transport{
			Proxy: proxyFn,
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

// handleConnect establishes a CONNECT tunnel to an allowed host. With an
// upstream proxy configured the tunnel runs through it (CONNECT-through-CONNECT);
// otherwise the origin is dialed directly.
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

	upstream, pending, err := h.dialTunnel(dialAddr)
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
	// Forward any bytes the upstream proxy already buffered past its own CONNECT
	// reply — they are the first bytes of the tunneled stream.
	if len(pending) > 0 {
		if _, err := clientConn.Write(pending); err != nil {
			_ = clientConn.Close()
			_ = upstream.Close()
			return
		}
	}

	// Pipe both directions until either side closes, then tear everything down.
	done := make(chan struct{}, 2)
	go pipe(upstream, clientConn, done)
	go pipe(clientConn, upstream, done)
	<-done
	_ = clientConn.Close()
	_ = upstream.Close()
}

// dialTunnel opens the far side of a CONNECT tunnel to dialAddr. Without an
// upstream proxy it dials the origin directly. With one it dials the upstream
// proxy and issues a nested CONNECT, returning the live connection plus any
// bytes the proxy buffered past its CONNECT reply (which belong to the tunnel).
func (h *Handler) dialTunnel(dialAddr string) (net.Conn, []byte, error) {
	if h.upstream == nil {
		conn, err := net.DialTimeout("tcp", dialAddr, h.dialTimeout)
		return conn, nil, err
	}

	proxyAddr := h.upstream.Host
	if _, _, err := net.SplitHostPort(proxyAddr); err != nil {
		proxyAddr = net.JoinHostPort(proxyAddr, "80")
	}
	conn, err := net.DialTimeout("tcp", proxyAddr, h.dialTimeout)
	if err != nil {
		return nil, nil, err
	}

	// Authority-form request-target (CONNECT host:port) per the stdlib's own
	// proxy tunneling: URL.Opaque carries the target verbatim.
	connReq := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: dialAddr},
		Host:   dialAddr,
		Header: make(http.Header),
	}
	if u := h.upstream.User; u != nil {
		pw, _ := u.Password()
		connReq.Header.Set("Proxy-Authorization",
			"Basic "+base64.StdEncoding.EncodeToString([]byte(u.Username()+":"+pw)))
	}

	// Bound the CONNECT handshake so a silent upstream can't hang us; clear the
	// deadline once the tunnel is up (it is long-lived).
	_ = conn.SetDeadline(time.Now().Add(h.dialTimeout))
	if err := connReq.Write(conn); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("upstream proxy CONNECT write: %w", err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, connReq)
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("upstream proxy CONNECT response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("upstream proxy CONNECT returned %s", resp.Status)
	}
	_ = conn.SetDeadline(time.Time{})

	var pending []byte
	if n := br.Buffered(); n > 0 {
		pending = make([]byte, n)
		if _, err := io.ReadFull(br, pending); err != nil {
			_ = conn.Close()
			return nil, nil, fmt.Errorf("upstream proxy CONNECT drain: %w", err)
		}
	}
	return conn, pending, nil
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
// the given domain allowlist, dialing origins directly.
func ListenAndServe(addr string, domains []string) error {
	return ListenAndServeUpstream(addr, domains, "")
}

// ListenAndServeUpstream runs the allowlist proxy on addr (default ":8888")
// enforcing the given domain allowlist and, when upstream is non-empty, chaining
// every allowed request through that parent proxy. This is the entry point the
// `sandboxer _proxy` mode calls. An invalid upstream URL is a hard error.
func ListenAndServeUpstream(addr string, domains []string, upstream string) error {
	if addr == "" {
		addr = ":8888"
	}
	var up *url.URL
	if upstream != "" {
		u, err := url.Parse(upstream)
		if err != nil {
			return fmt.Errorf("invalid upstream proxy URL %q: %w", upstream, err)
		}
		up = u
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           NewWithUpstream(domains, up),
		ReadHeaderTimeout: 30 * time.Second,
	}
	return srv.ListenAndServe()
}

// compile-time assertion that Handler satisfies http.Handler.
var _ http.Handler = (*Handler)(nil)
