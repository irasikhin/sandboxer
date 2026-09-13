package config

import (
	"slices"
	"strings"
	"testing"
)

// TestDefaultAllowlistCoversLocalClusterRegistries guards the pairing between
// the toolbox image's local-kubernetes tooling and the default egress
// allowlist: the Kubernetes project's own images — metrics-server,
// ingress-nginx, the core addons a fresh cluster gets next — live on
// registry.k8s.io, so trimming that domain out of the defaults leaves a
// sandboxed cluster unable to pull them under the name-bound wall. k8s.gcr.io
// is the same registry's legacy name, still referenced by older manifests.
func TestDefaultAllowlistCoversLocalClusterRegistries(t *testing.T) {
	domains := strings.Split(DefaultDomains, ",")
	for _, want := range []string{"registry.k8s.io", "k8s.gcr.io"} {
		if !slices.Contains(domains, want) {
			t.Errorf("DefaultDomains missing %q — a sandboxed cluster cannot pull the Kubernetes project's own images", want)
		}
	}
}
