package config

import (
	"net/url"
	"os"
)

// HostGatewayAlias is the hostname that resolves to the HOST from inside a
// container on either engine (mapped via --add-host=…:host-gateway; see
// HostGatewayArgs). A proxy a user runs "on localhost" is really on the host,
// which is NOT the container's own loopback — so ContainerProxyURL rewrites a
// localhost proxy to this name.
//
// It lives here rather than in backend because three different container
// launchers need it — the sandbox container, the egress sidecar, and the image
// builder — and backend cannot be imported by toolbox (backend imports toolbox
// for the auto-build).
const HostGatewayAlias = "host.docker.internal"

// HostGatewayArgs maps BOTH engines' host-gateway aliases, so one hostname
// reaches a host-running service regardless of engine: podman provides
// host.containers.internal and Docker Desktop provides host.docker.internal,
// but Linux Docker resolves neither without this. Harmless on an --internal
// network (the name just resolves to an unreachable gateway). Requires
// podman >= 4 / docker >= 20.10.
func HostGatewayArgs() []string {
	return []string{
		"--add-host=" + HostGatewayAlias + ":host-gateway",
		"--add-host=host.containers.internal:host-gateway",
	}
}

// ContainerProxyURL adapts a configured proxy URL for use from inside a
// container: a host of localhost / 127.0.0.1 / ::1 is rewritten to the host
// gateway (HostGatewayAlias), since the user means "a proxy on my host", not
// the container's own loopback. Any other host (a real hostname or LAN IP) is
// left untouched. An empty or unparseable URL is returned unchanged.
func ContainerProxyURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		newHost := HostGatewayAlias
		if port := u.Port(); port != "" {
			newHost += ":" + port
		}
		u.Host = newHost
		return u.String()
	default:
		return raw
	}
}

// proxyEnvNames are the variables a containerized build must inherit to reach
// the network through a host proxy. Both cases are carried because consumers
// disagree: curl reads the lowercase forms, Go reads either, and nixpkgs'
// fetchurl only lets these exact names through into a fixed-output derivation's
// otherwise-sealed sandbox (lib.fetchers.proxyImpureEnvVars). That last one is
// why this matters for an image build: without them nix can reach neither the
// binary cache NOR the release tarballs it falls back to fetching, and the
// build dies on a five-minute curl timeout instead of a fast, clear failure.
var proxyEnvNames = []string{
	"http_proxy", "https_proxy", "ftp_proxy", "all_proxy", "no_proxy",
	"HTTP_PROXY", "HTTPS_PROXY", "FTP_PROXY", "ALL_PROXY", "NO_PROXY",
}

// HostProxyEnv returns the host's proxy configuration as "NAME=value" strings
// ready to pass to a container. Unset variables are skipped; no_proxy is passed
// through verbatim (it is a host list, not a URL). Returns nil when the host has
// no proxy configured, so a proxy-less machine's argv is unchanged.
//
// rewriteLocalhost controls the localhost→host-gateway fix-up. Pass true for a
// container on its own network, where the user's "localhost" proxy is really on
// the host. Pass FALSE when the container shares the host's network namespace
// (--network=host): there localhost already IS the host, and rewriting would
// point a perfectly good proxy at the bridge gateway instead — which is often
// unreachable anyway, since a loopback-bound proxy does not listen there and a
// host firewall may drop bridge traffic outright.
//
// Callers must not log the result: a proxy URL may embed credentials.
func HostProxyEnv(rewriteLocalhost bool) []string {
	var out []string
	for _, name := range proxyEnvNames {
		v := os.Getenv(name)
		if v == "" {
			continue
		}
		if rewriteLocalhost && name != "no_proxy" && name != "NO_PROXY" {
			v = ContainerProxyURL(v)
		}
		out = append(out, name+"="+v)
	}
	return out
}
