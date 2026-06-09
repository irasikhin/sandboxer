package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestAllowedIPAndIPv6 pins down the allowlist's behaviour for IP literals: a
// raw IP never matches a domain allowlist (no DNS round-trip can launder it
// in), while an explicitly allowed IP literal — including bracketed IPv6 with a
// port — matches itself.
func TestAllowedIPAndIPv6(t *testing.T) {
	domains := []string{"example.com"}
	if Allowed(domains, "93.184.216.34:443") {
		t.Error("direct IPv4 must not match a domain allowlist")
	}
	if Allowed(domains, "[2606:2800:220:1:248:1893:25c8:1946]:443") {
		t.Error("direct IPv6 must not match a domain allowlist")
	}
	if !Allowed([]string{"::1"}, "[::1]:443") {
		t.Error("explicitly allowed bracketed IPv6 (with port) should match")
	}
	if !Allowed([]string{"::1"}, "::1") {
		t.Error("explicitly allowed bare IPv6 literal should match")
	}
	if Allowed([]string{"::1"}, "[2606::1]:443") {
		t.Error("a different IPv6 must not match")
	}
}

func TestTargetHost(t *testing.T) {
	if got := targetHost(&http.Request{URL: &url.URL{Host: "u.com:443"}, Host: "h.com"}); got != "u.com:443" {
		t.Errorf("targetHost prefers URL.Host, got %q", got)
	}
	if got := targetHost(&http.Request{URL: &url.URL{}, Host: "h.com"}); got != "h.com" {
		t.Errorf("targetHost fallback = %q, want h.com", got)
	}
}

func TestRemoveHopByHop(t *testing.T) {
	h := http.Header{}
	h.Set("Connection", "X-Custom, Keep-Alive")
	h.Set("X-Custom", "1")
	h.Set("Keep-Alive", "timeout=5")
	h.Set("X-Keep", "stays")
	removeHopByHop(h)
	if h.Get("X-Custom") != "" {
		t.Error("header named in Connection should be removed")
	}
	if h.Get("Keep-Alive") != "" {
		t.Error("hop-by-hop Keep-Alive should be removed")
	}
	if h.Get("Connection") != "" {
		t.Error("Connection header itself should be removed")
	}
	if h.Get("X-Keep") != "stays" {
		t.Error("non-hop-by-hop header should survive")
	}
}

func TestHandleHTTPBadGateway(t *testing.T) {
	h := New([]string{"127.0.0.1"})
	// Allowed host, but nothing is listening on port 1 → 502.
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

func TestAllowedWhitespaceHost(t *testing.T) {
	if Allowed([]string{"example.com"}, "   ") {
		t.Error("whitespace-only host should be rejected")
	}
}

func TestHandleHTTPOriginForm(t *testing.T) {
	// Origin-form request (no scheme/host on the URL): handleHTTP must fill them
	// from r.Host before routing. Port 1 is unreachable → 502, but only after
	// the scheme/host fixup runs.
	h := New([]string{"127.0.0.1"})
	req := httptest.NewRequest(http.MethodGet, "/path", nil)
	req.Host = "127.0.0.1:1"
	req.URL.Scheme = ""
	req.URL.Host = ""
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("origin-form status = %d, want 502", rec.Code)
	}
}

func TestHandleConnectDialFail(t *testing.T) {
	h := New([]string{"127.0.0.1"})
	req := &http.Request{Method: http.MethodConnect, Host: "127.0.0.1:1", URL: &url.URL{Host: "127.0.0.1:1"}}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("CONNECT to unreachable host status = %d, want 502", rec.Code)
	}
}

func TestHandleConnectHijackUnsupported(t *testing.T) {
	// A reachable, allowed target so the dial succeeds; the ResponseWriter is a
	// recorder (no Hijacker), so the tunnel setup must fail with 500.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	h := New([]string{"127.0.0.1"})
	req := &http.Request{Method: http.MethodConnect, Host: addr, URL: &url.URL{Host: addr}}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (hijacking unsupported)", rec.Code)
	}
}

// serveFakeConnectProxy answers a single CONNECT with 200, then splices the
// caller to the host it asked for — a minimal stand-in for a host-running parent
// proxy the egress sidecar chains through.
func serveFakeConnectProxy(c net.Conn) {
	defer func() { _ = c.Close() }()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	dst, err := net.Dial("tcp", req.Host)
	if err != nil {
		return
	}
	defer func() { _ = dst.Close() }()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(dst, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, dst); done <- struct{}{} }()
	<-done
}

// connectThrough issues a CONNECT for target via the proxy at proxAddr and
// returns the proxy's response.
func connectThrough(t *testing.T, proxAddr, target string) (*http.Response, *bufio.Reader, net.Conn) {
	t.Helper()
	conn, err := net.Dial("tcp", proxAddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	return resp, br, conn
}

// TestHandleConnectViaUpstream verifies the CONNECT-through-CONNECT chain: an
// allowed host tunnels through the configured upstream proxy and round-trips,
// while a denied host is rejected BEFORE the upstream is ever contacted.
func TestHandleConnectViaUpstream(t *testing.T) {
	// Echo target the tunnel ultimately reaches.
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = target.Close() }()
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func() { defer func() { _ = c.Close() }(); _, _ = io.Copy(c, c) }()
		}
	}()

	// Fake parent proxy; signals each time it is contacted.
	upstreamDialed := make(chan struct{}, 1)
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Close() }()
	go func() {
		for {
			c, err := up.Accept()
			if err != nil {
				return
			}
			upstreamDialed <- struct{}{}
			go serveFakeConnectProxy(c)
		}
	}()

	upURL, _ := url.Parse("http://" + up.Addr().String())
	upURL.User = url.UserPassword("user", "pass") // also exercises the Proxy-Authorization branch
	prox := httptest.NewServer(NewWithUpstream([]string{"127.0.0.1"}, upURL))
	defer prox.Close()
	proxAddr := strings.TrimPrefix(prox.URL, "http://")

	// 1) Denied host → 403, and the upstream is never dialed.
	denied, _, _ := connectThrough(t, proxAddr, "blocked.example.com:443")
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("denied CONNECT status = %d, want 403", denied.StatusCode)
	}
	select {
	case <-upstreamDialed:
		t.Fatal("upstream contacted for a denied host — allowlist must gate before chaining")
	default:
	}

	// 2) Allowed host → tunnels through the upstream and the echo round-trips.
	resp, br, conn := connectThrough(t, proxAddr, target.Addr().String())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("allowed CONNECT status = %d, want 200", resp.StatusCode)
	}
	if _, err := io.WriteString(conn, "ping\n"); err != nil {
		t.Fatal(err)
	}
	got, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if got != "ping\n" {
		t.Errorf("echo through upstream = %q, want %q", got, "ping\n")
	}
	select {
	case <-upstreamDialed:
	default:
		t.Error("upstream was not contacted for an allowed host")
	}
}

// TestHandleConnectUpstreamErrors covers the two upstream failure modes: a
// non-200 CONNECT reply and an unreachable upstream both surface to the agent as
// 502 (never a false 200 followed by a dead tunnel).
func TestHandleConnectUpstreamErrors(t *testing.T) {
	// Upstream that rejects the nested CONNECT with a non-200 status.
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Close() }()
	go func() {
		for {
			c, err := up.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_, _ = http.ReadRequest(bufio.NewReader(c))
				_, _ = io.WriteString(c, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
			}()
		}
	}()
	rejectURL, _ := url.Parse("http://" + up.Addr().String())
	rejectProx := httptest.NewServer(NewWithUpstream([]string{"127.0.0.1"}, rejectURL))
	defer rejectProx.Close()
	if resp, _, _ := connectThrough(t, strings.TrimPrefix(rejectProx.URL, "http://"), "127.0.0.1:443"); resp.StatusCode != http.StatusBadGateway {
		t.Errorf("non-200 upstream CONNECT → status %d, want 502", resp.StatusCode)
	}

	// Upstream pointing at a closed port → dial fails → 502.
	deadURL, _ := url.Parse("http://127.0.0.1:1")
	deadProx := httptest.NewServer(NewWithUpstream([]string{"127.0.0.1"}, deadURL))
	defer deadProx.Close()
	if resp, _, _ := connectThrough(t, strings.TrimPrefix(deadProx.URL, "http://"), "127.0.0.1:443"); resp.StatusCode != http.StatusBadGateway {
		t.Errorf("unreachable upstream → status %d, want 502", resp.StatusCode)
	}
}

// TestHandleConnectUpstreamDefaultPortAndPending covers the port-less upstream
// branch (default :80) and the drain of tunnel bytes the upstream bundles into
// the same write as its CONNECT reply.
func TestHandleConnectUpstreamDefaultPortAndPending(t *testing.T) {
	// (a) Upstream URL with no port → dialTunnel defaults to :80; nothing listens
	// there so the agent gets a 502.
	noPort, _ := url.Parse("http://127.0.0.1")
	noPortProx := httptest.NewServer(NewWithUpstream([]string{"127.0.0.1"}, noPort))
	defer noPortProx.Close()
	if resp, _, _ := connectThrough(t, strings.TrimPrefix(noPortProx.URL, "http://"), "127.0.0.1:443"); resp.StatusCode != http.StatusBadGateway {
		t.Errorf("port-less upstream → status %d, want 502", resp.StatusCode)
	}

	// (b) Upstream that sends tunnel bytes in the same write as its CONNECT reply
	// → the handler must drain and forward them before piping.
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Close() }()
	go func() {
		c, err := up.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = http.ReadRequest(bufio.NewReader(c))
		_, _ = io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\nEARLY\n")
		_, _ = io.Copy(io.Discard, c) // hold the tunnel open until the client closes
	}()
	upURL, _ := url.Parse("http://" + up.Addr().String())
	prox := httptest.NewServer(NewWithUpstream([]string{"127.0.0.1"}, upURL))
	defer prox.Close()
	_, br, _ := connectThrough(t, strings.TrimPrefix(prox.URL, "http://"), "127.0.0.1:443")
	got, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if got != "EARLY\n" {
		t.Errorf("buffered upstream tunnel bytes = %q, want %q", got, "EARLY\n")
	}
}

// TestHandleConnectUpstreamClosed: an upstream that drops the connection before
// replying to the nested CONNECT surfaces as 502 (covers the read-response
// error branch).
func TestHandleConnectUpstreamClosed(t *testing.T) {
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Close() }()
	go func() {
		c, err := up.Accept()
		if err != nil {
			return
		}
		_ = c.Close() // close before replying → the handler's ReadResponse sees EOF
	}()
	upURL, _ := url.Parse("http://" + up.Addr().String())
	prox := httptest.NewServer(NewWithUpstream([]string{"127.0.0.1"}, upURL))
	defer prox.Close()
	if resp, _, _ := connectThrough(t, strings.TrimPrefix(prox.URL, "http://"), "127.0.0.1:443"); resp.StatusCode != http.StatusBadGateway {
		t.Errorf("upstream closing mid-CONNECT → status %d, want 502", resp.StatusCode)
	}
}

func TestListenAndServeUpstreamErrors(t *testing.T) {
	// An unparseable upstream URL is a hard error before binding.
	if err := ListenAndServeUpstream(":0", nil, "://nope"); err == nil {
		t.Error("invalid upstream URL should error")
	}
	// A valid upstream with a bad listen address still reaches (and fails at) the
	// bind, exercising the upstream-parsed branch.
	if err := ListenAndServeUpstream("127.0.0.1:-1", nil, "http://up:3128"); err == nil {
		t.Error("bad listen address should error even with a valid upstream")
	}
}

func TestListenAndServeErrors(t *testing.T) {
	if err := ListenAndServe("127.0.0.1:-1", nil); err == nil {
		t.Error("ListenAndServe with a bad address should error")
	}
	// Default-addr branch: occupy :8888, then the default bind must fail.
	ln, err := net.Listen("tcp", ":8888")
	if err != nil {
		t.Skip("port 8888 unavailable; skipping default-addr check")
	}
	defer ln.Close()
	if err := ListenAndServe("", nil); err == nil {
		t.Error("ListenAndServe('') should fail when :8888 is occupied")
	}
}
