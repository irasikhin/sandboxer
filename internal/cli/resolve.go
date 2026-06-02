package cli

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

// commonFlags are shared across commands that operate on an existing sandbox.
type commonFlags struct {
	src     string
	config  string
	sandbox string
	model   string
	agent   string
	backend string
	domains string
	force   bool
}

// bindExisting registers the flags used by enter/exec/pull/push/show/diff/rm.
func bindExisting(cmd *cobra.Command, f *commonFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.src, "src", "", "project root (default: cwd)")
	fl.StringVar(&f.config, "config", "", "profile file (sandboxer.yaml)")
	fl.StringVarP(&f.sandbox, "sandbox", "S", "", "sandbox slug")
	fl.StringVar(&f.model, "model", "", "model override")
	fl.StringVar(&f.agent, "agent", "", "agent override")
	fl.StringVar(&f.backend, "backend", "", "backend: native | podman | docker")
	fl.StringVar(&f.domains, "allow-domains", "", "egress allowlist, csv (e.g. api.anthropic.com,github.com)")
}

// target is a resolved (base, slug, profile) tuple.
type target struct {
	base    *sandbox.Base
	slug    string
	profile *config.Profile // from the profile file, or the stored profile.json
	json    []byte          // profile JSON when loaded from a file (for storing)
}

// resolveProfileFile mirrors the bash precedence: --config, else a positional
// *.yaml file, else an auto-discovered sandboxer.yaml in the cwd. It returns
// the chosen file (or "") and the leftover positional.
func resolveProfileFile(configPath, pos string) (string, string) {
	if configPath != "" {
		return configPath, pos
	}
	if pos != "" && isYAML(pos) && fileExists(pos) {
		return pos, ""
	}
	if pos == "" && !inContainer() && fileExists("sandboxer.yaml") {
		return "sandboxer.yaml", ""
	}
	return "", pos
}

// resolveTarget reproduces the bash resolve_target + base resolution: it picks
// the profile (if any), the project root and the sandbox slug, and loads the
// base state.
func resolveTarget(f commonFlags, pos string) (*target, error) {
	file, pos := resolveProfileFile(f.config, pos)

	var prof *config.Profile
	var profJSON []byte
	var slug, root string

	if file != "" {
		p, err := config.Load(file)
		if err != nil {
			return nil, err
		}
		prof = p
		profJSON, _ = p.JSON()
		slug = p.Name
		root = firstNonEmpty(f.src, getwd())
		if slug == "" {
			return nil, errors.New("profile has no name")
		}
	} else {
		root = firstNonEmpty(f.src, getwd())
		slug = firstNonEmpty(f.sandbox, pos)
	}

	base, err := sandbox.ResolveBase(root)
	if err != nil {
		return nil, err
	}

	if slug == "" {
		slug = base.Current()
	}
	if slug == "" {
		if agents := base.Agents(); len(agents) == 1 {
			slug = agents[0]
		}
	}
	if slug == "" {
		return nil, errors.New("no sandbox selected: give <slug>, -S <slug>, or `sandboxer use <slug>` (list: sandboxer list)")
	}
	slug = config.Sanitize(slug)

	if prof == nil {
		prof = loadStoredProfile(base, slug)
	}
	return &target{base: base, slug: slug, profile: prof, json: profJSON}, nil
}

// runtime resolves the effective settings for a target using the flag overrides.
func (t *target) runtime(f commonFlags) config.Runtime {
	return config.ResolveRuntime(t.profile, config.LoadDefaults(), t.base.Domains, t.base.Model,
		config.Overrides{Model: f.model, Agent: f.agent, Backend: f.backend, Domains: f.domains})
}

// loadStoredProfile reads the profile.json saved for a sandbox (nil if absent).
func loadStoredProfile(base *sandbox.Base, slug string) *config.Profile {
	data, err := os.ReadFile(base.ProfileJSONPath(slug))
	if err != nil {
		return nil
	}
	var p config.Profile
	if json.Unmarshal(data, &p) != nil {
		return nil
	}
	return &p
}

func inContainer() bool { return os.Getenv("SANDBOXER_IN_CONTAINER") != "" }

func isYAML(p string) bool {
	return strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func getwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func posArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}
