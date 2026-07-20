package config

import (
	"strings"
	"testing"
)

// TestContainerProxyURL pins the localhost→host-gateway rewrite: a proxy a
// user runs "on localhost" is on the HOST, not on the container's own
// loopback, so only those three spellings are rewritten and everything else —
// a real hostname, a LAN IP, an unparseable string — is passed through.
func TestContainerProxyURL(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"http://localhost:8888", "http://" + HostGatewayAlias + ":8888"},
		{"http://127.0.0.1:8888", "http://" + HostGatewayAlias + ":8888"},
		{"http://[::1]:8888", "http://" + HostGatewayAlias + ":8888"},
		{"http://localhost", "http://" + HostGatewayAlias},
		{"http://proxy.corp:3128", "http://proxy.corp:3128"},
		{"http://10.0.0.5:8080", "http://10.0.0.5:8080"},
		{"::not a url::", "::not a url::"},
		// SOCKS5 (e.g. a tunnel client on the host) keeps its scheme.
		{"socks5h://127.0.0.1:1080", "socks5h://" + HostGatewayAlias + ":1080"},
		{"socks5://localhost:1080", "socks5://" + HostGatewayAlias + ":1080"},
		// A real address on an overlay network is somebody else's host — leave it.
		{"socks5h://[200:abcd::1]:1080", "socks5h://[200:abcd::1]:1080"},
	} {
		if got := ContainerProxyURL(c.in); got != c.want {
			t.Errorf("ContainerProxyURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestHostProxyEnv: proxy URLs are rewritten for container use, no_proxy is a
// host LIST and must survive verbatim (rewriting it would corrupt it), unset
// variables are skipped, and a proxy-less host yields nothing at all so an
// argv stays unchanged.
func TestHostProxyEnv(t *testing.T) {
	for _, n := range proxyEnvNames {
		t.Setenv(n, "")
	}
	if got := HostProxyEnv(true); got != nil {
		t.Errorf("no proxy configured: got %v, want nil", got)
	}

	t.Setenv("http_proxy", "http://127.0.0.1:8888")
	t.Setenv("HTTPS_PROXY", "http://proxy.corp:3128")
	t.Setenv("no_proxy", "localhost,127.0.0.1,.corp")
	got := strings.Join(HostProxyEnv(true), " ")
	for _, want := range []string{
		"http_proxy=http://" + HostGatewayAlias + ":8888",
		"HTTPS_PROXY=http://proxy.corp:3128",
		"no_proxy=localhost,127.0.0.1,.corp",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("HostProxyEnv() = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "ftp_proxy") || strings.Contains(got, "ALL_PROXY") {
		t.Errorf("unset variables leaked into %q", got)
	}

	// Under host networking localhost already IS the host: rewriting would aim
	// a working proxy at the bridge gateway, where a loopback-bound proxy is
	// not listening.
	raw := strings.Join(HostProxyEnv(false), " ")
	if !strings.Contains(raw, "http_proxy=http://127.0.0.1:8888") {
		t.Errorf("HostProxyEnv(false) must not rewrite: %q", raw)
	}
}

// TestHostGatewayArgs maps BOTH engines' aliases — podman resolves
// host.containers.internal, Docker Desktop host.docker.internal, and Linux
// Docker neither without the explicit mapping.
func TestHostGatewayArgs(t *testing.T) {
	got := strings.Join(HostGatewayArgs(), " ")
	for _, want := range []string{
		"--add-host=" + HostGatewayAlias + ":host-gateway",
		"--add-host=host.containers.internal:host-gateway",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("HostGatewayArgs() = %q, missing %q", got, want)
		}
	}
}
