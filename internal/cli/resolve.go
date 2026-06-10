package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/backend"
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
	noSetup bool
}

// bindExisting registers the flags used by enter/exec/pull/push/show/diff/rm.
func bindExisting(cmd *cobra.Command, f *commonFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.src, "src", "", "project root (default: cwd)")
	fl.StringVarP(&f.config, "config", "f", "", "profile: a file, a directory of profiles, or a named profile (store: ~/.config/sandboxer/profiles)")
	fl.StringVarP(&f.sandbox, "sandbox", "S", "", "sandbox slug")
	fl.StringVar(&f.model, "model", "", "model override")
	fl.StringVar(&f.agent, "agent", "", "agent override")
	fl.StringVar(&f.backend, "backend", "", "backend: podman | docker")
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
//	./.sandboxer.yaml  → auto-discovered in the cwd
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
	// A bare positional first tries to name a profile in the project's
	// .sandboxer.yaml — a multi-profile section or a flat file whose single
	// profile is named pos (project-local wins over the global store). This is
	// what lets a re-`enter <slug>` re-read an edited project file instead of the
	// frozen snapshot.
	if pos != "" && !isYAML(pos) && !inContainer() && config.FileHasProfile(config.ConfigFileName, pos) {
		return config.ConfigFileName, pos, nil
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
	if pos == "" && !inContainer() && fileExists(config.ConfigFileName) {
		return config.ConfigFileName, "", nil
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
		doc, err := config.LoadDocument(file)
		if err != nil {
			return nil, err
		}
		root = firstNonEmpty(f.src, getwd())
		if doc.Multi() {
			// Multi-profile file: the positional (or default:) names the section,
			// and that section name is the slug.
			p, err := doc.Select(pos)
			if err != nil {
				return nil, err
			}
			prof = p
			slug = p.Name
		} else {
			// Flat file: exactly one profile; the slug is its (file-derived) name.
			p, err := doc.Select("")
			if err != nil {
				return nil, err
			}
			prof = p
			slug = p.Name
		}
		profJSON, _ = prof.JSON()
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
		agents := base.Agents()
		if len(agents) > 0 {
			return nil, fmt.Errorf("no sandbox selected (have: %s) — give <slug>, -S <slug>, or `sandboxer use <slug>`", strings.Join(agents, ", "))
		}
		return nil, errors.New("no sandbox selected — give <slug>, -S <slug>, or `sandboxer use <slug>` (create one: sandboxer create)")
	}
	slug = config.Sanitize(slug)

	if prof == nil {
		prof = loadStoredProfile(base, slug)
	}
	return &target{base: base, slug: slug, profile: prof, json: profJSON}, nil
}

// runtime resolves the effective settings for a target using the flag overrides.
func (t *target) runtime(f commonFlags) (config.Runtime, error) {
	return config.ResolveRuntime(t.profile, config.LoadDefaults(), t.base.Domains, t.base.Model,
		config.Overrides{Model: f.model, Agent: f.agent, Backend: f.backend, Domains: f.domains})
}

// backendLabel reports the backend to show in the banner: for a container
// backend it is the engine that will actually be used (podman→docker auto-
// detect, an explicitly requested engine honored when installed) rather than the
// raw configured value, so the banner can never claim "podman" while docker
// runs.
func backendLabel(rt config.Runtime) string {
	return backend.EngineLabel(rt.Backend, config.LoadDefaults())
}

// configLine summarises the resolved settings so a command always tells the user
// what is actually in effect — which agent/backend/model, the egress status, the
// profile source and the dependency count — instead of leaving them to infer the
// silent defaults. backendShown is the engine label (see backendLabel) so the
// reported backend matches what runs. Printed to stderr by create/enter/exec.
func configLine(rt config.Runtime, slug string, prof *config.Profile, backendShown string) string {
	egress := "off"
	switch {
	case noEgress():
		egress = "off (SANDBOXER_NO_EGRESS)"
	case rt.HTTPProxy != "" || rt.HTTPSProxy != "":
		egress = "bypass-proxy"
	case rt.UpstreamProxy != "" && rt.Egress:
		egress = fmt.Sprintf("on→upstream (%d domains)", len(rt.Domains))
	case rt.Egress:
		egress = fmt.Sprintf("on (%d domains)", len(rt.Domains))
	}
	profile, deps := "none (defaults)", 0
	if prof != nil {
		if prof.Name != "" {
			profile = prof.Name
		} else {
			profile = "(unnamed)"
		}
		deps = len(prof.Deps)
	}
	return fmt.Sprintf("sandboxer: %s — agent=%s backend=%s model=%s egress=%s profile=%s deps=%d",
		slug, rt.Agent, backendShown, firstNonEmpty(rt.Model, "default"), egress, profile, deps)
}

// syncSnapshot refreshes the sandbox's stored profile.json from the freshly
// resolved profile, so editing .sandboxer.yaml propagates to an existing
// sandbox instead of being frozen at create time. It reports whether the stored
// snapshot actually changed (so the caller can re-pull deps only when needed).
//
// It is a no-op inside the container (the snapshot is a read-only mount and
// there is no live source) and when the target was resolved from the stored
// snapshot rather than a live file/store (t.json == nil) — there is nothing
// newer to write.
func (t *target) syncSnapshot() (changed bool, err error) {
	if inContainer() || t.json == nil {
		return false, nil
	}
	pj := t.base.ProfileJSONPath(t.slug)
	if cur, e := os.ReadFile(pj); e == nil && bytes.Equal(cur, t.json) {
		return false, nil
	}
	return true, t.base.WriteProfileJSON(t.slug, t.json)
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
