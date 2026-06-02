package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
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
	fl.StringVarP(&f.config, "config", "f", "", "profile: a file, a directory of profiles, or a named profile (store: ~/.config/sandboxer/profiles)")
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

// resolveProfileFile selects the profile to load and returns it together with
// the leftover positional and any resolution error. Precedence:
//
//	-f/--config FILE   → that file (the positional is kept as a slug override)
//	-f/--config DIR    → the profile the positional names inside DIR
//	-f/--config NAME   → a named profile from the global store
//	positional NAME    → a named profile from the global store
//	positional *.yaml  → that file
//	./sandboxer.yaml   → auto-discovered in the cwd
//
// Anything else leaves the positional as a bare slug ("", pos).
func resolveProfileFile(configPath, pos string) (string, string, error) {
	if configPath != "" {
		if isDir(configPath) {
			file, err := selectFromDir(configPath, pos)
			return file, "", err
		}
		// Not an existing file and not a *.yaml path → try the named store.
		if !fileExists(configPath) && !isYAML(configPath) {
			file, err := config.FindProfile(config.ProfilesDir(), configPath)
			if err != nil {
				return "", pos, err
			}
			if file != "" {
				return file, pos, nil
			}
		}
		return configPath, pos, nil
	}
	if pos != "" && !isYAML(pos) {
		file, err := config.FindProfile(config.ProfilesDir(), pos)
		if err != nil {
			return "", pos, err
		}
		if file != "" {
			return file, "", nil
		}
	}
	if pos != "" && isYAML(pos) && fileExists(pos) {
		return pos, "", nil
	}
	if pos == "" && !inContainer() && fileExists("sandboxer.yaml") {
		return "sandboxer.yaml", "", nil
	}
	return "", pos, nil
}

// selectFromDir picks a profile by name from a directory of profiles. An empty
// name is allowed only when the directory holds exactly one profile; otherwise
// it errors with the available names.
func selectFromDir(dir, name string) (string, error) {
	refs := config.ListProfilesIn(dir)
	if len(refs) == 0 {
		return "", fmt.Errorf("no profiles (*.yaml) in %s", dir)
	}
	if name == "" {
		if len(refs) == 1 {
			return refs[0].Path, nil
		}
		return "", fmt.Errorf("name a profile in %s (have: %s)", dir, profileNames(refs))
	}
	file, err := config.FindProfile(dir, name)
	if err != nil {
		return "", err
	}
	if file == "" {
		return "", fmt.Errorf("no profile %q in %s (have: %s)", name, dir, profileNames(refs))
	}
	return file, nil
}

// profileNames renders the sorted profile names for an error/listing message.
func profileNames(refs []config.ProfileRef) string {
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.Name
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// resolveTarget reproduces the bash resolve_target + base resolution: it picks
// the profile (if any), the project root and the sandbox slug, and loads the
// base state.
func resolveTarget(f commonFlags, pos string) (*target, error) {
	file, pos, err := resolveProfileFile(f.config, pos)
	if err != nil {
		return nil, err
	}

	var prof *config.Profile
	var profJSON []byte
	var slug, root string

	if file != "" {
		p, err := config.Load(file)
		if err != nil {
			return nil, err
		}
		prof = p
		// An unnamed profile takes its slug from the file's base name.
		if prof.Name == "" {
			prof.Name = config.ProfileName(file, nil)
		}
		profJSON, _ = prof.JSON()
		slug = prof.Name
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

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
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
