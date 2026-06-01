package proxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

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
