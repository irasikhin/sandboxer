// Package config models the sandboxer profile and resolves runtime settings.
//
// The config is a NIX file — sandboxer.nix — evaluated to JSON via the host
// nix (see EvalConfig; sandboxed, no network). It must evaluate to an attrset
// with the profile's camelCase keys: one flat profile, or
// { profiles = { <name> = {...}; }; default = "<name>"; }. Reuse between
// profiles is ordinary nix (let bindings, functions, //-merges) — there is no
// config-level inheritance. Scalar settings may also come from flags and
// SANDBOXER_* env vars; the resolved profile is serialized to JSON and stored
// under the state dir's _meta/<slug>.profile.json — that JSON is the single
// artifact the backend and the sandbox package read.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Mount is an extra host directory shared into the sandbox.
type Mount struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Mode   string `json:"mode,omitempty"`
}

// Src is one sandbox source: a git repository, optionally narrowed to a subset
// of its files and pinned to a branch. A sandbox may list several sources —
// each becomes its own host-side git worktree; only the selected files are
// visible inside the container (git itself never is).
type Src struct {
	// Src is the repository — either a local path to its top-level directory
	// ("." = the project itself; relative paths resolve against the PROJECT
	// ROOT, --src/default cwd, regardless of where the profile file lives), OR
	// a git URL (https://, ssh://, git://, file://, or scp-like
	// git@host:org/repo). A URL is cloned once into a host-side cache under the
	// state dir and worktree'd from there exactly like a local repo; the clone
	// uses the host's git credentials and never enters the container. Review and
	// push its branch on the host (git -C <worktree> push origin feat/<slug>-sb).
	Src string `json:"src"`
	// Include narrows what the CONTAINER sees of this source to the listed
	// directories — each a literal anchored repo-relative path ("/services/api",
	// trailing slash optional) or an ant-style directory pattern ("/services/*/",
	// "**/proto/" — a whole "**" segment matches any depth). Empty (or ["**"])
	// means the whole repo. The host's worktree is complete either way:
	// narrowing is enforced by mounting only the selected directories into the
	// container, so an IDE on the host still sees a full tree. Directories only
	// — see ValidateInclude for why an entry can never select files.
	Include []string `json:"include,omitempty"`
	// Branch names the branch the source is checked out on. It is REQUIRED —
	// there is no default naming — and it also names the worktree's location
	// (sandboxes/<slug>/<branch>/<repo>). A missing branch is created off HEAD.
	// A branch already checked out in a worktree of your own is adopted (git
	// allows only one worktree per branch), and linked into the sandbox at that
	// same location; a branch checked out in the REPOSITORY ITSELF or in
	// another sandbox is refused — see sandbox.checkAdoptable.
	Branch string `json:"branch,omitempty"`
	// Git opts THIS source out of the "git never enters the sandbox" default by
	// sharing its repository's git directory into the guest: "" / "off" (the
	// default — no git metadata is shared, commits happen on the host), "ro"
	// (the git dir is shared READ-ONLY: log/diff/blame/show work inside, the
	// host repository cannot be written) or "rw" (shared read-write: the agent
	// commits inside the sandbox).
	//
	// It is deliberately per-source and off by default, because sharing a git
	// dir hands over the WHOLE repository, not this branch: every branch's
	// history is readable (including files a narrowing include would have
	// withheld — which is why the two are mutually exclusive, see ValidateGit),
	// and "rw" additionally makes .git/hooks and .git/config — code the HOST's
	// git executes — writable from inside. SANDBOXER_NO_GIT=1 forces every
	// source back to off.
	Git string `json:"git,omitempty"`
}

// The values of Src.Git: no git dir shared (the default), shared read-only,
// shared read-write.
const (
	GitOff = "off"
	GitRO  = "ro"
	GitRW  = "rw"
)

// GitShared reports whether a Src.Git value shares the repository's git dir
// into the sandbox at all ("" and "off" do not).
func GitShared(mode string) bool { return mode == GitRO || mode == GitRW }

// Egress holds the sandbox's outbound-traffic policy: whether sandboxer enforces
// a domain allowlist (Enabled), the allowlist itself, and the proxy settings.
// It is the whole "egress" attrset in the config.
type Egress struct {
	// Enabled toggles sandboxer's own egress control — the machine-level
	// allowlist. Default true (nil == on): sandboxer enforces AllowedDomains.
	// false is the escape hatch: an open VM network, where a Proxy (if set) is
	// trusted to police egress — AllowedDomains is then IGNORED (NoProxy
	// applies instead). Because the allowlist is inert when disabled, the safe
	// default is on; see EgressEnabled. SANDBOXER_NO_EGRESS=1 is the operator
	// kill-switch that forces it off regardless.
	Enabled *bool `json:"enabled,omitempty"`
	// AllowedDomains is the allowlist — the ONLY domains the sandbox may reach.
	// Enforced when Enabled and no Proxy is set; with a Proxy the proxy is the
	// egress control point (see backend.vmNetworkArgs / msbNetworkArgs).
	AllowedDomains []string `json:"allowedDomains,omitempty"`
	// Proxy is the single proxy URL the sandbox routes through (http:// or
	// https://host:port). Empty means no proxy. With a proxy set the guest's
	// HTTP(S) clients are pointed at it over an open VM network — the proxy IS
	// the egress control point. A localhost/127.0.0.1 host is adapted for the
	// guest at launch (msb rewrites a loopback host to
	// host.microsandbox.internal).
	Proxy string `json:"proxy,omitempty"`
	// NoProxy is the NO_PROXY list applied alongside Proxy.
	NoProxy string `json:"noProxy,omitempty"`
}

// Limits caps a sandbox machine's resources. Every field is optional; an empty
// field means the microVM default size. Memory is a memory string (e.g. 2G,
// 512m), CPUs a whole vCPU count or systemd-style quota (2 or 200%), Disk a
// root-disk size string (e.g. 20G, 512M). All three override the SANDBOXER_MEM
// / SANDBOXER_CPU / SANDBOXER_DISK env defaults.
type Limits struct {
	Memory string `json:"memory,omitempty"`
	CPUs   string `json:"cpus,omitempty"`
	Disk   string `json:"disk,omitempty"`
}

// ImageSpec customizes the toolbox image a profile's sandbox runs in. Every
// key is flat data — nothing needs pkgs at config time; the one thing that
// does (an overlay) lives in its own file. An empty spec means the stock
// image.
type ImageSpec struct {
	// Ref selects a PREBUILT image for this profile — a full registry
	// reference (tag or digest form) the backend pulls and caches, e.g. a
	// pinned release of the stock toolbox or a user-published image. It is
	// the third rung of the reference precedence: profile ref > the
	// SANDBOXER_IMAGE global > the compiled default. Mutually exclusive with
	// every customization field below: customization always BUILDS a local
	// var- image from the flake — there is no mechanism to build on top of a
	// pulled ref (see ValidateImageSpec).
	Ref string `json:"ref,omitempty"`
	// Packages are nixpkgs attribute names baked into the image (dotted
	// attribute paths like nodePackages.pnpm are allowed). They resolve
	// against the OVERLAID package set, so an attr defined by Overlay is
	// listed here like any other.
	Packages []string `json:"packages,omitempty"`
	// Files are text files baked into the image at absolute paths —
	// /etc/sandboxer/rc.d/*.sh shell drop-ins, /etc/containers/registries.conf
	// mirrors, and so on. Static text only: a file whose CONTENT needs pkgs
	// (store paths) is an overlay attr via writeTextDir, listed in Packages.
	Files map[string]string `json:"files,omitempty"`
	// Env is appended to the image's OCI env (static values; the profile's
	// own env still overrides at run time).
	Env map[string]string `json:"env,omitempty"`
	// Overlay is a file containing a PLAIN nixpkgs overlay —
	// final: prev: { … } — for anything that needs pkgs at build time
	// (patched or computed packages). It may be written relative to the
	// profile file; LoadDocument resolves it to an absolute path so the
	// stored _meta/<slug>.profile.json snapshot stays self-contained.
	Overlay string `json:"overlay,omitempty"`
	// NixpkgsRev selects the image's nixpkgs flake-input rev — the single
	// input everything comes from (agents included; pi is vendored in the
	// binary). Empty and "latest" both track the remote head (the default:
	// `image build` re-resolves and rebuilds with the current agents); a full
	// 40-hex commit hash pins exactly (see ValidateImageSpec).
	NixpkgsRev string `json:"nixpkgsRev,omitempty"`
}

// Empty reports whether the spec requests no customization, i.e. the sandbox
// runs the stock toolbox image. A tracking rev ("" or "latest") is the stock
// image's own behavior, not a customization; only a concrete pin is.
func (s ImageSpec) Empty() bool {
	return len(s.Packages) == 0 && len(s.Files) == 0 && len(s.Env) == 0 &&
		s.Overlay == "" && isTrackingRev(s.NixpkgsRev)
}

// isTrackingRev reports whether an image input rev tracks the remote head —
// the empty default and the explicit "latest" spelling mean the same thing.
func isTrackingRev(rev string) bool {
	return rev == "" || rev == "latest"
}

// WholeRepo reports whether include selects the whole repository — no patterns,
// or the single catch-all "**" — so the container gets the source's worktree
// whole and needs no per-directory view mounts.
func WholeRepo(include []string) bool {
	return len(include) == 0 || (len(include) == 1 && include[0] == "**")
}

// ValidateInclude rejects an include entry a container mount cannot honor.
//
// Narrowing is enforced by bind-mounting ONLY the selected directories into the
// container (the host worktree stays complete, so an IDE can open it). An entry
// is either a literal anchored repo-relative directory path ("/src/proto/",
// trailing slash optional) or an ant-style DIRECTORY pattern: segments may use
// *, ? and [...] (path.Match syntax, matched against directory names), and a
// whole "**" segment matches any number of directories, including zero — so
// "/**/proto/" selects every proto/ directory at any depth. "**/proto/"
// (first segment "**") is accepted as shorthand for that; any other unanchored
// entry stays rejected — under gitignore semantics it would mean "any depth",
// and that now has an explicit spelling. Only a WHOLE "**" segment recurses:
// "a**" inside a segment is an ordinary glob (same as "a*").
//
// A mount names directories, never files: the expansion (sandbox.expandInclude)
// walks directories only, so a pattern can never select a file set — a
// file-granular bind mount breaks atomic saves (write-temp + rename over the
// mountpoint fails with EBUSY), which is how editors and agents write files.
// A negation ("!/vendor/") has no meaning for a mount set, so it stays rejected.
//
// Whether a literal path is actually a directory, and whether a pattern matches
// anything, is NOT checked here — that needs the repo on disk and lives in
// sandbox.checkViewDirs / the expansion, which reject with an actionable
// message. So this stays pure syntax.
func ValidateInclude(include []string) error {
	if WholeRepo(include) {
		return nil
	}
	for _, p := range include {
		switch {
		case p == "":
			return errors.New("srcs include: empty pattern — remove it, or use a directory like \"/src/proto/\"")
		case strings.HasPrefix(p, "!"):
			return fmt.Errorf("srcs include %q: negation is not supported — narrowing mounts the listed "+
				"directories, so list what to EXPOSE rather than what to exclude", p)
		case p == "/" || p == "//":
			return errors.New("srcs include \"/\": that is the whole repo — drop include entirely instead")
		}
		segs := strings.Split(strings.Trim(p, "/"), "/")
		if !strings.HasPrefix(p, "/") && segs[0] != "**" {
			return fmt.Errorf("srcs include %q: must be anchored at the repo root — write \"/%s\" "+
				"(or \"**/%s\" to match the directory at any depth)", p, p, p)
		}
		if len(segs) == 1 && segs[0] == "**" {
			// "/**", "/**/", "**/" — a lone "**" entry is WholeRepo and never
			// gets here, so this only trips alongside other entries.
			return fmt.Errorf("srcs include %q: that is the whole repo — drop include entirely instead", p)
		}
		for _, seg := range segs {
			switch {
			case seg == "" || seg == "." || seg == "..":
				return fmt.Errorf("srcs include %q: must be a plain repo-relative directory path "+
					"(no empty, \".\" or \"..\" segments)", p)
			case seg == "**":
				// the recursive wildcard — any number of directories, incl. zero
			case strings.ContainsAny(seg, `*?[\`):
				if _, err := path.Match(seg, "x"); err != nil {
					return fmt.Errorf("srcs include %q: bad pattern segment %q — segments may use *, ? and "+
						"[...] (or a whole \"**\" segment); close the bracket or name the directory literally", p, seg)
				}
			}
		}
	}
	return nil
}

// ValidateSrcs checks every source's include patterns and git mode. Path/branch
// validation needs the repo on disk and lives in sandbox.resolveSrcs; this half
// is pure and runs wherever a profile is resolved, so `config validate` catches
// a bad pattern without touching git. (The src/branch-required checks live in
// the CLI's validateProfile, NOT here: resolveSrcs calls this first, and a
// generic message would shadow its richer errors — the recorded-branch hint.)
func ValidateSrcs(srcs []Src) error {
	for _, s := range srcs {
		if err := ValidateInclude(s.Include); err != nil {
			return err
		}
		if err := ValidateGit(s.Git, s.Include); err != nil {
			return err
		}
	}
	return nil
}

// ValidateGit checks a source's git mode and refuses the one combination that
// would be a lie: git together with a narrowing include.
//
// include narrows what the sandbox can reach by mounting only the listed
// directories — the excluded files exist on the host but not inside. A shared
// git dir carries the complete history of every branch, so `git show
// HEAD:excluded/file` reconstitutes exactly what the include withheld: the two
// keys claim opposite things about the same source, and silently letting the
// weaker one win is how a narrowed sandbox ends up leaking its whole repo.
// Refusing makes the user pick which wall they meant.
func ValidateGit(mode string, include []string) error {
	switch mode {
	case "", GitOff, GitRO, GitRW:
	default:
		return fmt.Errorf("srcs: git = %q is not a mode — use %q (the default: no git metadata in the sandbox), "+
			"%q (share the repository's git dir read-only: log/diff/blame inside, the host repo stays untouched) "+
			"or %q (share it read-write: the agent can commit)", mode, GitOff, GitRO, GitRW)
	}
	if GitShared(mode) && !WholeRepo(include) {
		return fmt.Errorf("srcs: git = %q cannot be combined with include — include narrows the sandbox by "+
			"mounting only %v, but a shared git dir carries the FULL history of every branch, so the excluded "+
			"files come back through `git show HEAD:<path>`; keep include (and commit on the host) or drop it "+
			"and keep git", mode, include)
	}
	return nil
}

var imageRevRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ValidateImageSpec rejects a malformed flake-input revision override. Each rev
// is "" or "latest" (track the remote head — the default) or a full
// 40-character lowercase hex commit hash that pins it. Short prefixes are
// rejected: the same commit as 7- and 40-hex would mint two different variant
// tags, and nix treats a non-40-hex rev in a github: flakeref as a ref needing
// a GitHub API resolve — a network dependency a pin must not have.
func ValidateImageSpec(s ImageSpec) error {
	if s.Ref != "" {
		if !s.Empty() {
			return errors.New("image.ref and image customization are mutually exclusive: " +
				"customization (packages/files/env/overlay/nixpkgsRev, tools) always builds a " +
				"local var- image from the flake — a prebuilt ref cannot be customized on top; " +
				"drop the ref or the customization")
		}
		if strings.ContainsAny(s.Ref, " \t\n\r") {
			return fmt.Errorf("invalid image.ref %q — an image reference carries no whitespace", s.Ref)
		}
	}
	for path := range s.Files {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("image.files key %q must be an absolute in-image path", path)
		}
	}
	if rev := s.NixpkgsRev; rev != "" && rev != "latest" && !imageRevRe.MatchString(rev) {
		return fmt.Errorf("invalid image.nixpkgsRev %q — use latest or a full 40-char hex commit hash", rev)
	}
	return nil
}

// resolveImageNix makes a relative Image.Overlay absolute against dir (the
// profile file's directory), so the path survives being snapshotted to _meta
// and read from another working directory. Absolute paths pass through; an
// empty field is a no-op.
func (p *Profile) resolveImageNix(dir string) {
	ov := p.Image.Overlay
	if ov == "" || filepath.IsAbs(ov) {
		return
	}
	ov = filepath.Join(dir, ov)
	if abs, err := filepath.Abs(ov); err == nil {
		ov = abs
	}
	p.Image.Overlay = ov
}

// Profile is a sandbox configuration. Scalar fields are optional (they come
// from flags/env/defaults), but srcs must be listed to create a sandbox —
// there is no implicit source. Each srcs entry is backed by a host-side git
// worktree; git metadata never enters the container, so the container sees
// exactly the files the include patterns select and nothing else.
type Profile struct {
	Name    string `json:"name,omitempty"`
	Backend string `json:"backend,omitempty"`
	Egress  Egress `json:"egress,omitempty"`
	// Srcs are the sandbox's sources — repositories (whole, or narrowed by
	// gitignore-style include patterns) exposed inside the container. ALWAYS
	// explicit: an empty list is rejected at sandbox creation (the scaffolded
	// config seeds an explicit src + branch); there is no implicit default.
	Srcs []Src `json:"srcs,omitempty"`
	// WorktreesDir overrides where this sandbox's worktrees live. Absolute,
	// ~-prefixed, or relative to the PROJECT ROOT; empty = the default
	// ./sandboxes inside the project (auto-added to the project's
	// .gitignore, as is any in-project override). The sandbox occupies
	// <worktreesDir>/<name>/<branch>/<repo>. Set it before creating the
	// sandbox — changing it on an existing sandbox is REFUSED (an in-place
	// relocation would need a cross-filesystem worktree move); rebuild at the
	// new location with `sandboxer recreate` instead, which keeps the branches.
	WorktreesDir string            `json:"worktreesDir,omitempty"`
	ExtraMounts  []Mount           `json:"extraMounts,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	// Ports publishes guest ports on the host, so a server started INSIDE the
	// sandbox (a dev server, dsh's browser UI) can be opened from the host's
	// browser. Specs are microsandbox's own grammar — "3080", "8080:3080",
	// "0.0.0.0:8080:3080", "5353:53/udp" — and bind to 127.0.0.1 unless the
	// spec names another address (see ParsePorts). Each forward also opens the
	// ONE ingress rule its port needs in the machine's default-deny wall;
	// nothing else about the egress allowlist changes. Empty = no inbound path
	// into the sandbox at all, which is the default.
	// SANDBOXER_NO_PORTS=1 is the operator kill-switch.
	Ports []string `json:"ports,omitempty"`
	// Setup is a one-time shell script run inside the sandbox (bash -lc) before
	// the user/agent takes over — e.g. `npm ci`, a build, a DB seed. It runs
	// once per sandbox (re-run only when the script changes) under the same
	// isolation and egress allowlist as the sandbox.
	Setup string `json:"setup,omitempty"`
	// Tools names language/runtime tool packs (see registry/tools.json: node,
	// python, go, rust, …) baked into a per-profile toolbox image variant,
	// built on demand and cached by tool-set hash.
	Tools []string `json:"tools,omitempty"`
	// Image customizes the toolbox image variant this profile's sandbox runs
	// in; an empty spec keeps the stock image. See ImageSpec.
	Image ImageSpec `json:"image,omitempty"`
	// HostConfigs wires the HOST's agent identity into the sandbox, two
	// halves under one opt-in:
	//   - config seed: the registry seed paths (~/.claude + ~/.claude.json,
	//     ~/.codex, ~/.gemini, ~/.dsh, opencode/crush — settings, skills,
	//     memory) copied into the sandbox's private home. Claude's rotating
	//     OAuth pair (.claude/.credentials.json) is deliberately NOT copied:
	//     a copy dies on the next refresh-token rotation either side
	//     performs — and can hijack the host's session;
	//   - auth env: the registry agents' auth vars set on the host
	//     (CLAUDE_CODE_OAUTH_TOKEN from `claude setup-token`, API keys)
	//     passed into the container env — the durable way to start
	//     authenticated. Always a COPY into the per-sandbox home (never a mount: the
	// sandbox cannot touch the host's real config, and parallel sandboxes
	// cannot race), applied on create/enter/exec as a per-FILE merge: files
	// the sandbox home lacks are added (so new host skills or a later host
	// login flow into existing sandboxes too), files it has are never
	// overwritten — an in-sandbox login/logout/edit always wins. Opt-in
	// because it hands the sandbox live credentials; the scaffolded config
	// enables it. See sandbox.SeedHome and SECURITY.md.
	HostConfigs bool `json:"hostConfigs,omitempty"`
	// Session selects how enter/exec use the container: "persistent" (the
	// default) keeps one detached session container running across invocations,
	// "ephemeral" starts a fresh one-shot container per command.
	Session string `json:"session,omitempty"`
	// AutoResume relaunches a cataloged agent (its registry resume argv, e.g.
	// `claude --continue`) in every restored pane that was running one when the
	// session layout was captured. Default true; nil means true (the
	// egress.enabled pattern). It never enters the container's create argv, so
	// toggling it can NOT read as a changed profile and never rebuilds a session.
	AutoResume *bool `json:"autoResume,omitempty"`
	// PiPackages registers the pi packages baked into the toolbox image — pi's
	// multi-agent orchestration package today — in the sandbox's pi settings,
	// so `pi` starts with them loaded instead of after a manual `pi install`.
	// Default true; nil means true (the egress.enabled pattern). It only ever
	// writes the sandbox's own ~/.pi/agent/settings.json (never the host's) and
	// never enters the machine's create argv, so toggling it cannot read as a
	// changed profile and never rebuilds a session. See sandbox.EnsurePiPackages.
	PiPackages *bool `json:"piPackages,omitempty"`
	// Limits caps the sandbox machine's memory/cpus/disk; empty fields inherit
	// the SANDBOXER_MEM/SANDBOXER_CPU/SANDBOXER_DISK env defaults.
	Limits Limits `json:"limits,omitempty"`
}

// decodeProfileJSON strictly decodes one profile from evaluated-config JSON
// (unknown fields are rejected, catching typos — the same posture the YAML
// era had with KnownFields).
func decodeProfileJSON(data []byte) (*Profile, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var p Profile
	if err := dec.Decode(&p); err != nil {
		return nil, annotateRemovedKeys(err)
	}
	return &p, nil
}

// removedKeys maps a profile key that used to be valid to migration guidance.
// A removed key trips the strict decoder's "field <key> not found" the same way
// a typo does; annotateRemovedKeys turns that terse message into an actionable
// hint. The table grows as knobs are retired.
var removedKeys = map[string]string{
	"model":      "removed — set the agent's own env var instead, e.g. env: { ANTHROPIC_MODEL: opus }",
	"proxy":      "moved under egress: — use egress.proxy",
	"noProxy":    "moved under egress: — use egress.noProxy",
	"network":    "renamed to egress — use egress.allowedDomains / egress.proxy / egress.noProxy (and egress.enabled = false to disable the allowlist)",
	"agent":      "removed — a sandbox is not bound to one agent (choose per exec); use hostConfigs for credential passthrough",
	"agentProxy": "removed — use egress.proxy (the proxy is the egress control point)",
	"nestedContainers": "removed with the container backend — a microVM runs container engines natively, " +
		"so docker/podman work inside every sandbox with no opt-in",
	"routes": "removed with the container backend — per-domain upstream proxies were a proxy-chaining " +
		"feature; use a single egress.proxy that routes by destination itself",
	"pids": "removed with the container backend — the microVM backends have no PID-count cap " +
		"(limits.memory / limits.cpus bound the machine instead)",
	"roots":        "removed — sandboxes are git worktrees now (no copy mode); mount other trees with extraMounts",
	"context":      "removed — a git-worktree sandbox already contains the repo's files (nothing is copied in)",
	"agents":       "removed — set hostConfigs = true to seed the sandbox home from the host's agent configs (credentials included), or log in / export API keys INSIDE the sandbox (its $HOME persists)",
	"extraPkgs":    "renamed — image.packages (same nixpkgs attr names; overlay-defined attrs may be listed too)",
	"hook":         "removed — put static customization in image.{packages,files,env}; anything needing pkgs is a plain nixpkgs overlay file: image.overlay = \"./overlay.nix\"",
	"nix":          "replaced by image.overlay — a file with a PLAIN nixpkgs overlay (final: prev: { … }); expose computed packages/files as overlay attrs and list them in image.packages",
	"deps":         "replaced by srcs — e.g. srcs = [ { src = \".\"; include = [ \"/some/dir/\" ]; } ] (src = path to a repo; include = directory paths/patterns; empty include = the whole repo)",
	"defaults":     "removed — profiles are self-contained; share between them with ordinary nix (let base = { ... }; in { profiles.web = base // { ... }; })",
	"llmAgentsRev": "removed — agents come straight from nixpkgs now (pi is vendored in sandboxer); pin image.nixpkgsRev instead",
}

// annotateRemovedKeys upgrades the strict decoder's `unknown field "<key>"`
// error into a migration hint when <key> is a knob that was intentionally
// removed, so an upgrading user is told what to do instead of thinking they
// typo'd. Any other decode error passes through unchanged. Modeled on the
// advisory legacyConfigHint: it only reshapes the message, never behavior.
func annotateRemovedKeys(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	var hints []string
	for key, hint := range removedKeys {
		if strings.Contains(msg, "unknown field "+strconv.Quote(key)) {
			hints = append(hints, fmt.Sprintf("  `%s` %s", key, hint))
		}
	}
	// `egress` used to be a top-level bool; it is now the egress attrset, so an
	// old `egress = false` trips a type error (bool into struct) rather than an
	// unknown-field error. Give the same actionable migration hint.
	if strings.Contains(msg, "Go struct field Profile.egress of type config.Egress") {
		hints = append(hints, "  `egress` is now an attrset — set egress.enabled = false (was egress = false); omit it for the default allowlist (was egress = true)")
	}
	// A source's `git` is the one key whose name invites a bool — "give this
	// source git" reads as true/false — while the value is a MODE, because
	// read-only and read-write are very different grants. Say so, instead of
	// letting the decoder's type error stand as the answer.
	if strings.Contains(msg, "Go struct field Src.srcs.git of type string") {
		hints = append(hints, "  `git` on a source takes a MODE, not a bool — git = \"ro\" shares the repository's "+
			"git dir READ-ONLY (log/diff/blame work inside; the host repo cannot be written), git = \"rw\" shares it "+
			"writable (the agent can commit, and .git/hooks becomes host-side code it can edit); omit it (or \"off\") "+
			"to keep git out of the sandbox")
	}
	if len(hints) == 0 {
		return err
	}
	sort.Strings(hints)
	return fmt.Errorf("%w\n%s", err, strings.Join(hints, "\n"))
}

// JSON serializes the profile to the camelCase JSON stored under _meta and
// mounted into the container.
func (p *Profile) JSON() ([]byte, error) {
	return json.MarshalIndent(p, "", "  ")
}

// EgressEnabled reports whether the container egress allowlist sidecar should
// run. Default true; an explicit `egress.enabled = false` disables it (the agent
// talks to egress.proxy directly and that proxy polices egress).
func (p *Profile) EgressEnabled() bool {
	return p.Egress.Enabled == nil || *p.Egress.Enabled
}

// AutoResumeEnabled reports whether restored panes relaunch their recorded
// agents. Default true; an explicit `autoResume = false` restores layout only.
func (p *Profile) AutoResumeEnabled() bool {
	return p.AutoResume == nil || *p.AutoResume
}

// PiPackagesEnabled reports whether the image's baked-in pi packages are
// registered in the sandbox's pi settings. Default true; an explicit
// `piPackages = false` leaves pi's settings alone.
func (p *Profile) PiPackagesEnabled() bool {
	return p.PiPackages == nil || *p.PiPackages
}

var sanitizeRe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)
var dashTrimRe = regexp.MustCompile(`^-+|-+$`)

// Sanitize turns an arbitrary name into a safe slug (mirrors the bash
// sanitize(): collapse non [A-Za-z0-9_.-] runs to a single '-', trim leading
// and trailing dashes).
func Sanitize(s string) string {
	s = sanitizeRe.ReplaceAllString(s, "-")
	s = dashTrimRe.ReplaceAllString(s, "")
	return s
}

// ValidSlug rejects a sandbox name that is unsafe to use as a path component.
// A slug names the sandbox's worktree directory under the worktrees root
// (sandbox.SandboxDir) plus several files under the state dir, all of which
// teardown removes — so "", "." or ".." would resolve those destructive
// operations onto the worktrees root or, via filepath.Join(root, ".."), the
// PROJECT ROOT itself. Sanitize collapses "/" to "-", so these pure-dot forms
// are the only path-traversal values that survive it; callers Sanitize first,
// then reject these explicitly.
func ValidSlug(slug string) error {
	switch slug {
	case "":
		return errors.New("empty sandbox name — give a name like \"feat\"")
	case ".", "..":
		return fmt.Errorf("invalid sandbox name %q — a sandbox name cannot be %q "+
			"(it would name the worktrees root or the project directory itself)", slug, slug)
	}
	return nil
}
