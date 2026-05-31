package proxy

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestAllowed(t *testing.T) {
	domains := []string{"example.com", "registry.npmjs.org"}

	cases := []struct {
		host string
		want bool
	}{
		// allowed
		{"example.com", true},
		{"sub.example.com", true},
		{"registry.npmjs.org", true},
		{"a.b.example.com", true},
		{"EXAMPLE.com", true},     // case-insensitive
		{"example.com:443", true}, // port stripped
		// blocked
		{"evilexample.com", false},
		{"example.com.evil", false},
		{"other.org", false},
		{"", false},
	}

	for _, c := range cases {
		if got := Allowed(domains, c.host); got != c.want {
			t.Errorf("Allowed(%q) = %v, want %v", c.host, got, c.want)
		}
	}

	// Empty domains list allows nothing.
	for _, host := range []string{"example.com", "sub.example.com", "anything.org", ""} {
		if Allowed(nil, host) {
			t.Errorf("Allowed(nil, %q) = true, want false", host)
		}
		if Allowed([]string{}, host) {
			t.Errorf("Allowed([], %q) = true, want false", host)
		}
	}
}

// hostOf extracts the bare host (no port) from a server URL.
func hostOf(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Hostname()
}

// proxyClient returns an http.Client that routes all requests through proxyURL.
func proxyClient(t *testing.T, proxyURL string, tlsConfig *tls.Config) *http.Client {
	t.Helper()
	pu, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatalf("parse proxy url %q: %v", proxyURL, err)
	}
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(pu),
			TLSClientConfig: tlsConfig,
		},
	}
}

func TestPlainHTTPForwardAllowed(t *testing.T) {
	const body = "hello-from-origin"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
	defer origin.Close()

	// Allow the origin's host (typically 127.0.0.1).
	domains := []string{hostOf(t, origin.URL)}

	prox := httptest.NewServer(New(domains))
	defer prox.Close()

	client := proxyClient(t, prox.URL, nil)
	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatalf("GET via proxy: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

func TestPlainHTTPForwardBlocked(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "should-not-reach")
	}))
	defer origin.Close()

	// Allowlist that does NOT include the origin's host.
	prox := httptest.NewServer(New([]string{"example.com"}))
	defer prox.Close()

	client := proxyClient(t, prox.URL, nil)
	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatalf("GET via proxy: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestConnectTunnelAllowedAndBlocked(t *testing.T) {
	const body = "tls-origin-body"
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
	defer origin.Close()

	originHost := hostOf(t, origin.URL)
	tlsCfg := &tls.Config{InsecureSkipVerify: true}

	t.Run("allowed", func(t *testing.T) {
		prox := httptest.NewServer(New([]string{originHost}))
		defer prox.Close()

		client := proxyClient(t, prox.URL, tlsCfg)
		resp, err := client.Get(origin.URL)
		if err != nil {
			t.Fatalf("CONNECT via proxy: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		got, _ := io.ReadAll(resp.Body)
		if string(got) != body {
			t.Fatalf("body = %q, want %q", got, body)
		}
	})

	t.Run("blocked", func(t *testing.T) {
		prox := httptest.NewServer(New([]string{"example.com"}))
		defer prox.Close()

		client := proxyClient(t, prox.URL, tlsCfg)
		resp, err := client.Get(origin.URL)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("expected CONNECT to a blocked host to fail, got status %d", resp.StatusCode)
		}
	})
}

// TestConnectRawListener exercises the CONNECT tunnel against a bare TCP
// listener to confirm bytes flow end-to-end without TLS in the way.
func TestConnectRawListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	const reply = "PONG"
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4)
		io.ReadFull(conn, buf) // read "PING"
		io.WriteString(conn, reply)
	}()

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = port

	prox := httptest.NewServer(New([]string{host}))
	defer prox.Close()

	// Connect to the proxy and issue a raw CONNECT to the listener.
	pc, err := net.DialTimeout("tcp", prox.Listener.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer pc.Close()
	pc.SetDeadline(time.Now().Add(5 * time.Second))

	target := ln.Addr().String()
	io.WriteString(pc, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")

	// Read the proxy's status line.
	statusBuf := make([]byte, len("HTTP/1.1 200 Connection established\r\n\r\n"))
	if _, err := io.ReadFull(pc, statusBuf); err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	if got := string(statusBuf); got[:12] != "HTTP/1.1 200" {
		t.Fatalf("CONNECT status = %q, want 200", got)
	}

	// Now the tunnel is established: write PING, expect PONG.
	io.WriteString(pc, "PING")
	out := make([]byte, len(reply))
	if _, err := io.ReadFull(pc, out); err != nil {
		t.Fatalf("read tunnel reply: %v", err)
	}
	if string(out) != reply {
		t.Fatalf("tunnel reply = %q, want %q", out, reply)
	}
}
