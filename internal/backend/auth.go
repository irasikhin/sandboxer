package backend

import (
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/irasikhin/sandboxer/internal/config"
)

// extraMountsAndEnv adds the profile's extraMounts and env injections.
func extraMountsAndEnv(p *config.Profile) []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, m := range p.ExtraMounts {
		mode := m.Mode
		if mode == "" {
			mode = "rw"
		}
		out = append(out, "--volume", fmt.Sprintf("%s:%s:%s", m.Source, m.Target, mode))
	}
	// Sorted keys: the argv is fingerprinted (ConfigHash) and shown (compose),
	// so map iteration order must not leak into it.
	for _, k := range slices.Sorted(maps.Keys(p.Env)) {
		out = append(out, "--env", k+"="+p.Env[k])
	}
	return out
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
