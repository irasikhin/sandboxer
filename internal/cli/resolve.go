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
	ephemeral bool // --ephemeral: one-shot machine instead of the persistent session
	recreate  bool // --recreate: force session rebuild even if running (enter only)
}

// bindTarget registers the flags that only RESOLVE which sandbox to act on —
// everything resolveTarget reads. Commands that never launch a machine
// (reset works on the host worktrees) take these alone: binding runtime flags
// they cannot honour would accept them silently.
func bindTarget(cmd *cobra.Command, f *commonFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.src, "src", "", "project root (default: cwd)")
	fl.StringVarP(&f.config, "config", "f", "", "profile file (default: the project sandboxer.nix; pick a profiles section by name)")
	fl.StringVarP(&f.sandbox, "sandbox", "S", "", "sandbox slug")
}

// bindExisting registers the flags shared by commands that operate on an
// existing sandbox AND resolve its runtime (enter/exec/show/stop/recreate/rm)
// — the resolution flags plus the runtime overrides.
func bindExisting(cmd *cobra.Command, f *commonFlags) {
	bindTarget(cmd, f)
	fl := cmd.Flags()
	fl.StringVar(&f.backend, "backend", "", "backend: microsandbox")
	fl.StringVar(&f.domains, "allow-domains", "", "egress allowlist, csv (e.g. api.anthropic.com,github.com)")
}

// target is a resolved (base, slug, profile) tuple.
type target struct {
	base    *sandbox.Base
	slug    string
	profile *config.Profile // from the profile file, or the stored profile.json
	json    []byte          // profile JSON when loaded from a file (for storing)
}

// mounts returns the sandbox's source bind mounts, whether its <slug>/ root is
// one of them, the mounts' inode fingerprint (RunOpts.MountGen) and the same
// identities encoded for the session label (RunOpts.MountIDs). All four are
// ALWAYS derived together (see sandbox.Mounts / MountIdentities): mounting the
// root while a source is narrowed would expose every excluded file, and both
// the fingerprint and the recorded identities must cover exactly the mounts
// that end up in the argv, so the tuple must never be assembled field by field
// at a call site — a fingerprint and a label describing different mount sets
// would make the drift diagnosis lie. One stat pass feeds both. It errors when
// an include pattern resolves to nothing (or the worktree cannot be walked) —
// fail closed rather than launch a sandbox with a silently empty view.
func (t *target) mounts() (mountDest bool, srcMounts []string, mountGen, mountIDs string, err error) {
	mountDest, srcMounts, err = sandbox.Mounts(t.base.Srcs(t.slug))
	if err != nil {
		return false, nil, "", "", err
	}
	ids := sandbox.MountIdentities(srcMounts)
	return mountDest, srcMounts, sandbox.FingerprintIDs(ids), sandbox.EncodeMountIDs(ids), nil
}

// resolveProfileFile selects the profile file and returns it together with
// the leftover positional and any resolution error. Profiles live in ONE
// config file (several as its profiles: sections) — there is no directory
// scan and no global store. Precedence:
//
//	-f/--config FILE   → that file (the positional is kept as a slug override)
//	positional NAME    → the project config, when it has a section of that name
//	positional *.nix   → that file
//	sandboxer.nix      → auto-discovered under the project root (--src or cwd)
//
// Anything else leaves the positional as a bare slug ("", pos). root is the
// project root the project config is discovered under (--src, else cwd).
func resolveProfileFile(configPath, root, pos string) (string, string, error) {
	if configPath != "" {
		if isDir(configPath) {
			return "", pos, fmt.Errorf("-f %s is a directory — profiles live in one config file; "+
				"point -f at a *.nix (several profiles go under its profiles attrset)", configPath)
		}
		if isYAML(configPath) {
			return "", pos, fmt.Errorf("%s: sandboxer configs are nix now — translate the YAML to %s "+
				"by hand (same camelCase keys)", configPath, config.ConfigFileName)
		}
		return configPath, pos, nil
	}
	// A bare positional first tries to name a profile in the project's
	// sandboxer.nix — a multi-profile section or a flat file whose single
	// profile is named pos. This is what lets a re-`enter <slug>` re-read an
	// edited project file instead of the frozen snapshot.
	if pos != "" && !isNix(pos) && !isYAML(pos) && !inContainer() && config.FileHasProfile(config.ConfigPathIn(root), pos) {
		return config.ConfigPathIn(root), pos, nil
	}
	if pos != "" && isYAML(pos) {
		return "", pos, fmt.Errorf("%s: sandboxer configs are nix now — translate the YAML to %s "+
			"by hand (same camelCase keys)", pos, config.ConfigFileName)
	}
	if pos != "" && isNix(pos) && fileExists(pos) {
		return pos, "", nil
	}
	if pos == "" && !inContainer() && fileExists(config.ConfigPathIn(root)) {
		return config.ConfigPathIn(root), "", nil
	}
	return "", pos, nil
}

// legacyConfigHint reports a one-line migration notice when a config exists
// at a retired location/format under root but the current sandboxer.nix does
// not — so an upgrading user isn't silently met with the no-profile defaults.
// Recognized: the YAML-era sandboxer.yaml, the pre-relocation
// .sandboxer/config.yaml and the ancient root-level .sandboxer.yaml. Purely
// advisory (the old files are no longer read) and a no-op in the container.
func legacyConfigHint(w io.Writer, root string) {
	if inContainer() {
		return
	}
	if fileExists(config.ConfigPathIn(root)) {
		return
	}
	for _, legacy := range []string{
		filepath.Join(root, config.LegacyYAMLConfigFileName),
		filepath.Join(root, config.LegacyConfigDirPath()),
		filepath.Join(root, config.LegacyConfigFileName),
	} {
		if fileExists(legacy) {
			fmt.Fprintf(w, "sandboxer: found legacy %s — the config is a nix file now: translate it "+
				"by hand to %s (same camelCase keys; several profiles = { profiles = {...}; default = \"...\"; })\n",
				legacy, config.ConfigPathIn(root))
			return
		}
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
		pj, jerr := prof.JSON()
		if jerr != nil {
			return nil, fmt.Errorf("serialize profile %q: %w", slug, jerr)
		}
		profJSON = pj
		if slug == "" {
			return nil, errors.New("profile has no name")
		}
	} else {
		slug = firstNonEmpty(f.sandbox, pos)
	}

	// A token shaped like a host-wide id (what `list` prints) that is NOT a
	// sandbox of the project we stand in resolves through the id index instead:
	// the sandbox is acted on in ITS project, with no cd and no --src — the only
	// way to reach one whose project directory is gone. A slug of the current
	// project always wins the tie (a slug is a name, an id is just a handle),
	// and -f/--src mean a project was already named, so the lookup is skipped
	// there. An unknown token falls through and is reported as a missing slug.
	if file == "" && f.src == "" && sandbox.LooksLikeID(slug) && !projectHasSlug(root, slug) {
		switch ref, ferr := sandbox.FindByID(slug); {
		case ferr == nil:
			return idTarget(ref)
		case !errors.Is(ferr, sandbox.ErrNoSuchID):
			return nil, ferr
		}
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
	// Guard every command against a slug that would escape the sandbox tree:
	// SandboxDir("..") is the project root and RemoveState os.RemoveAll's it.
	if err := config.ValidSlug(slug); err != nil {
		return nil, err
	}

	if prof == nil {
		prof = loadStoredProfile(base, slug)
	}
	return &target{base: base, slug: slug, profile: prof, json: profJSON}, nil
}

// idTarget builds the target for a sandbox resolved by id. Its profile can only
// come from the stored snapshot: we are not standing in that project, so its
// sandboxer.nix is not the config in scope — and t.json stays nil, so nothing
// this command does can overwrite the snapshot with a foreign profile.
func idTarget(ref sandbox.Ref) (*target, error) {
	if err := config.ValidSlug(ref.Slug); err != nil {
		return nil, err
	}
	return &target{base: ref.Base, slug: ref.Slug, profile: loadStoredProfile(ref.Base, ref.Slug)}, nil
}

// projectHasSlug reports whether root's project already holds a sandbox by that
// name. It opens the state read-only (OpenBase, not ResolveBase): merely
// checking must never mint a state directory for whatever cwd we happen to be
// standing in.
func projectHasSlug(root, slug string) bool {
	base, err := sandbox.OpenBase(root)
	if err != nil || base == nil {
		return false
	}
	for _, a := range base.Agents() {
		if a == slug {
			return true
		}
	}
	return false
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

// backendLabel reports the backend to show in the banner, naming the runner
// binary beside the backend so the banner matches what actually runs.
func backendLabel(rt config.Runtime) string {
	if rt.Backend == "microsandbox" {
		return "microsandbox (msb)"
	}
	return rt.Backend
}

// configLine summarises the resolved settings so a command always tells the user
// what is actually in effect — the binary VERSION (so cross-machine skew is
// visible at a glance), which backend, the egress status, the profile source
// and the source count — instead of leaving them to infer the silent
// defaults. backendShown is the engine label (see backendLabel) so the
// reported backend matches what runs. Printed to stderr by create/enter/exec.
func configLine(rt config.Runtime, slug string, prof *config.Profile, backendShown string) string {
	profile, srcs := "none (defaults)", 0
	if prof != nil {
		if prof.Name != "" {
			profile = prof.Name
		} else {
			profile = "(unnamed)"
		}
		srcs = len(prof.Srcs)
	}
	return fmt.Sprintf("sandboxer %s: %s — backend=%s egress=%s profile=%s srcs=%d",
		Version, slug, backendShown, egressLabel(rt), profile, srcs)
}

// egressLabel renders the resolved egress posture for the configLine. It names
// the one state with no outbound wall at all — no allowlist sidecar AND no
// proxy — distinctly as OPEN, so an unrestricted network can never hide behind
// the same "off" the trusted-proxy (direct) case uses. See networkOpen.
func egressLabel(rt config.Runtime) string {
	sidecar := rt.Egress && !noEgress()
	switch {
	case sidecar && rt.Proxy != "":
		return fmt.Sprintf("on→proxy (%d domains)", len(rt.Domains))
	case sidecar:
		return fmt.Sprintf("on (%d domains)", len(rt.Domains))
	case rt.Proxy != "":
		if noEgress() {
			return "off (SANDBOXER_NO_EGRESS) → proxy (direct)"
		}
		return "off → proxy (direct)"
	default:
		if noEgress() {
			return "OPEN — unrestricted outbound (SANDBOXER_NO_EGRESS)"
		}
		return "OPEN — unrestricted outbound"
	}
}

// networkOpen reports whether the resolved settings leave the container on an
// unrestricted network: no allowlist sidecar (egress off, or the NO_EGRESS
// kill-switch) AND no proxy to route through — the one egress state with no
// outbound wall. Kept in lockstep with egressLabel's OPEN branch.
func networkOpen(rt config.Runtime) bool {
	return (!rt.Egress || noEgress()) && rt.Proxy == ""
}

// srcLine renders one resolved source the way create's and enter's banners and
// show's sources block report it: the repo, the branch it is on, any include
// narrowing, and where the worktree actually lives on the host —
// "repo → branch [inc] (/path)". An ADOPTED source names both of its places:
// the slot it occupies inside the sandbox and the checkout that slot links to,
// because those differ and the difference is exactly what confuses people.
// Shared so the banners can never drift.
func srcLine(s sandbox.Source) string {
	scope := ""
	if len(s.Include) > 0 {
		scope = " [" + strings.Join(s.Include, " ") + "]"
	}
	where := s.Path
	if !s.Managed {
		where = fmt.Sprintf("%s → %s, adopted", s.Link, s.Path)
	}
	return fmt.Sprintf("%s → %s%s (%s)", filepath.Base(s.RepoRoot), s.Branch, scope, where)
}

// syncSnapshot refreshes the sandbox's stored profile.json from the freshly
// resolved profile, so editing sandboxer.nix propagates to an existing
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

func isNix(p string) bool { return strings.HasSuffix(p, ".nix") }

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
