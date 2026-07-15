package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

// commonFlags are shared across commands that operate on an existing sandbox.
type commonFlags struct {
	src       string
	config    string
	sandbox   string
	backend   string
	domains   string
	noSetup   bool
	ephemeral bool // --ephemeral: one-shot container instead of the persistent session
}

// bindExisting registers the flags shared by commands that operate on an
// existing sandbox (enter/exec/show/stop/recreate/rm/compose).
func bindExisting(cmd *cobra.Command, f *commonFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.src, "src", "", "project root (default: cwd)")
	fl.StringVarP(&f.config, "config", "f", "", "profile file (default: the project sandboxer.yaml; pick a profiles: section by name)")
	fl.StringVarP(&f.sandbox, "sandbox", "S", "", "sandbox slug")
	fl.StringVar(&f.backend, "backend", "", "backend: docker | podman")
	fl.StringVar(&f.domains, "allow-domains", "", "egress allowlist, csv (e.g. api.anthropic.com,github.com)")
}

// target is a resolved (base, slug, profile) tuple.
type target struct {
	base    *sandbox.Base
	slug    string
	profile *config.Profile // from the profile file, or the stored profile.json
	json    []byte          // profile JSON when loaded from a file (for storing)
}

// resolveProfileFile selects the profile file and returns it together with
// the leftover positional and any resolution error. Profiles live in ONE
// config file (several as its profiles: sections) — there is no directory
// scan and no global store. Precedence:
//
//	-f/--config FILE   → that file (the positional is kept as a slug override)
//	positional NAME    → the project config, when it has a section of that name
//	positional *.yaml  → that file
//	sandboxer.yaml     → auto-discovered under the project root (--src or cwd)
//
// Anything else leaves the positional as a bare slug ("", pos). root is the
// project root the project config is discovered under (--src, else cwd).
func resolveProfileFile(configPath, root, pos string) (string, string, error) {
	if configPath != "" {
		if isDir(configPath) {
			return "", pos, fmt.Errorf("-f %s is a directory — profiles live in one config file; "+
				"point -f at a *.yaml (several profiles go under its profiles: map)", configPath)
		}
		return configPath, pos, nil
	}
	// A bare positional first tries to name a profile in the project's
	// sandboxer.yaml — a multi-profile section or a flat file whose single
	// profile is named pos. This is what lets a re-`enter <slug>` re-read an
	// edited project file instead of the frozen snapshot.
	if pos != "" && !isYAML(pos) && !inContainer() && config.FileHasProfile(config.ConfigPathIn(root), pos) {
		return config.ConfigPathIn(root), pos, nil
	}
	if pos != "" && isYAML(pos) && fileExists(pos) {
		return pos, "", nil
	}
	if pos == "" && !inContainer() && fileExists(config.ConfigPathIn(root)) {
		return config.ConfigPathIn(root), "", nil
	}
	return "", pos, nil
}

// legacyConfigHint reports a one-line migration notice when a config exists at
// a retired location under root but the current sandboxer.yaml does not — so
// an upgrading user isn't silently met with the no-profile defaults. Two
// retired generations are recognized: .sandboxer/config.yaml (pre-relocation)
// and the ancient root-level .sandboxer.yaml. Purely advisory (the old paths
// are no longer read) and a no-op inside the container.
func legacyConfigHint(w io.Writer, root string) {
	if inContainer() {
		return
	}
	if fileExists(config.ConfigPathIn(root)) {
		return
	}
	if prev := filepath.Join(root, config.LegacyConfigDirPath()); fileExists(prev) {
		fmt.Fprintf(w, "sandboxer: found legacy %s — the config moved to the project root: "+
			"git mv %s %s (and %s to %s if present; it is no longer read here)\n",
			prev, config.LegacyConfigDirPath(), config.ConfigFileName,
			filepath.Join(config.LegacyStateDirName, "image.nix"), config.ImageNixFileName)
		return
	}
	if legacy := filepath.Join(root, config.LegacyConfigFileName); fileExists(legacy) {
		fmt.Fprintf(w, "sandboxer: found legacy %s — move it to %s (it is no longer read here)\n",
			legacy, config.ConfigPathIn(root))
	}
}

// resolveTarget reproduces the bash resolve_target + base resolution: it picks
// the profile (if any), the project root and the sandbox slug, and loads the
// base state.
func resolveTarget(f commonFlags, pos string) (*target, error) {
	root := firstNonEmpty(f.src, getwd())
	file, pos, err := resolveProfileFile(f.config, root, pos)
	if err != nil {
		return nil, err
	}

	var prof *config.Profile
	var profJSON []byte
	var slug string

	if file != "" {
		doc, err := config.LoadDocument(file)
		if err != nil {
			return nil, err
		}
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
// The boolean --ephemeral maps onto the session override here, so the session
// mode stays a single resolution chain inside ResolveRuntime.
func (t *target) runtime(f commonFlags) (config.Runtime, error) {
	session := ""
	if f.ephemeral {
		session = config.SessionEphemeral
	}
	return config.ResolveRuntime(t.profile, config.LoadDefaults(), t.base.Domains,
		config.Overrides{Backend: f.backend, Session: session, Domains: f.domains})
}

// backendLabel reports the backend to show in the banner: for a container
// backend it is the engine that will actually be used (docker→podman auto-
// detect, an explicitly requested engine honored when installed) rather than the
// raw configured value, so the banner can never claim "docker" while podman
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
	case rt.Egress && rt.Proxy != "":
		egress = fmt.Sprintf("on→proxy (%d domains)", len(rt.Domains))
	case rt.Egress:
		egress = fmt.Sprintf("on (%d domains)", len(rt.Domains))
	case rt.Proxy != "":
		egress = "off → proxy (direct)"
	}
	profile, srcs := "none (defaults)", 0
	if prof != nil {
		if prof.Name != "" {
			profile = prof.Name
		} else {
			profile = "(unnamed)"
		}
		srcs = len(prof.Srcs)
	}
	return fmt.Sprintf("sandboxer: %s — backend=%s egress=%s profile=%s srcs=%d",
		slug, backendShown, egress, profile, srcs)
}

// syncSnapshot refreshes the sandbox's stored profile.json from the freshly
// resolved profile, so editing sandboxer.yaml propagates to an existing
// sandbox (its resolved settings) instead of being frozen at create time.
//
// It is a no-op inside the container (the snapshot is a read-only mount and
// there is no live source) and when the target was resolved from the stored
// snapshot rather than a live file/store (t.json == nil) — there is nothing
// newer to write.
func (t *target) syncSnapshot() error {
	if inContainer() || t.json == nil {
		return nil
	}
	pj := t.base.ProfileJSONPath(t.slug)
	if cur, e := os.ReadFile(pj); e == nil && bytes.Equal(cur, t.json) {
		return nil
	}
	return t.base.WriteProfileJSON(t.slug, t.json)
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
