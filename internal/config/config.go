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
// artifact the container backend and the sandbox package read.
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

// Mount is an extra bind mount for the container backend.
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
	// (sandboxes/<slug>/<branch>/<repo>). An existing worktree of that
	// branch (including the repo's main checkout) is adopted as-is; a missing
	// branch is created off HEAD.
	Branch string `json:"branch,omitempty"`
}

// Egress holds the sandbox's outbound-traffic policy: whether sandboxer enforces
// a domain allowlist (Enabled), the allowlist itself, and the proxy settings.
// It is the whole "egress" attrset in the config.
type Egress struct {
	// Enabled toggles sandboxer's own egress control — the squid allowlist
	// sidecar. Default true (nil == on): sandboxer enforces AllowedDomains (and
	// Routes), and a Proxy, if set, is chained through the sidecar (http:// only).
	// false is the escape hatch: NO sidecar, the agent talks to Proxy DIRECTLY
	// (HTTP(S)_PROXY) and that proxy is trusted to police egress — AllowedDomains
	// and Routes are then IGNORED (NoProxy applies instead). Because the allowlist
	// is inert when disabled, the safe default is on; see the "Proxy handling"
	// comment below and EgressEnabled. SANDBOXER_NO_EGRESS=1 is the operator
	// kill-switch that forces it off regardless.
	Enabled *bool `json:"enabled,omitempty"`
	// AllowedDomains is the allowlist — the ONLY domains the sandbox may reach.
	// Enforced when Enabled; ignored when disabled (the proxy is trusted instead).
	AllowedDomains []string `json:"allowedDomains,omitempty"`
	// Proxy is the single proxy URL the sandbox routes through (http://host:port,
	// or https:// only when Enabled is false). Empty means no proxy. Enabled
	// decides whether it CHAINS through the allowlist sidecar (on) or the agent
	// talks to it DIRECTLY (off). A localhost/127.0.0.1 host is rewritten to the
	// host gateway at launch (see backend.ContainerProxyURL).
	Proxy string `json:"proxy,omitempty"`
	// NoProxy is the NO_PROXY list applied only when Enabled is false; it is
	// ignored when traffic is chained through the allowlist sidecar.
	NoProxy string `json:"noProxy,omitempty"`
	// Routes send specific destination domains through a dedicated upstream proxy
	// (a squid cache_peer), overriding Proxy for just those domains — e.g. bypass
	// a geo-block for one API while everything else stays direct or on the default
	// proxy. Routes only apply when Enabled; they are ignored when disabled, where
	// the agent talks to Proxy directly.
	Routes []Route `json:"routes,omitempty"`
}

// Route pins a set of destination domains to a dedicated upstream proxy. Every
// routed domain must also be in Egress.AllowedDomains (squid denies it before
// the peer otherwise), and a domain may appear in at most one route.
type Route struct {
	Domains []string `json:"domains,omitempty"`
	Proxy   string   `json:"proxy,omitempty"`
}

// Limits caps a sandbox container's resources. Every field is optional; an empty
// field means "no cap". Memory is an engine memory string (e.g. 2G, 512m), CPUs
// a core count or systemd-style quota (1.5 or 150%), and Pids the PID-count cap
// (--pids-limit) that bounds fork-bomb blast radius. Memory/CPUs override the
// SANDBOXER_MEM / SANDBOXER_CPU env defaults; Pids has no env default.
type Limits struct {
	Memory string `json:"memory,omitempty"`
	CPUs   string `json:"cpus,omitempty"`
	Pids   int    `json:"pids,omitempty"`
}

// Proxy handling: a sandbox reaches the outside world through ONE proxy URL
// (Egress.Proxy, an `http://host:port` or `https://host:port`). There is no
// separate "upstream" vs "corporate" mode — Egress.Enabled decides the trust
// model:
//
//   - Enabled (the default): the allowlist sidecar stays up and CHAINS allowed
//     traffic through the proxy (squid cache_peer). sandboxer keeps enforcing the
//     domain allowlist AND the traffic still leaves via the proxy. Only an http://
//     proxy works here (the sidecar cannot speak TLS to a parent yet).
//   - Disabled (egress.enabled = false): the agent talks to the proxy DIRECTLY
//     (HTTP(S)_PROXY env) and that proxy is trusted to police egress. http:// or
//     https:// both work.
//
// A proxy whose host is localhost/127.0.0.1 is rewritten to the host gateway
// (host.docker.internal) at container-launch time, so "a proxy on my host" works
// with the obvious URL — see backend.ContainerProxyURL.

// ImageSpec customizes the toolbox image a profile's sandbox runs in. Every
// key is flat data — nothing needs pkgs at config time; the one thing that
// does (an overlay) lives in its own file. An empty spec means the stock
// image.
type ImageSpec struct {
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
	// LLMAgentsRev / NixpkgsRev select the image's flake-input revs. Empty and
	// "latest" both track the remote head (the default: `image build`
	// re-resolves and rebuilds with the current agents); a full 40-hex commit
	// hash pins exactly (see ValidateImageSpec).
	LLMAgentsRev string `json:"llmAgentsRev,omitempty"`
	NixpkgsRev   string `json:"nixpkgsRev,omitempty"`
}

// Empty reports whether the spec requests no customization, i.e. the sandbox
// runs the stock toolbox image. A tracking rev ("" or "latest") is the stock
// image's own behavior, not a customization; only a concrete pin is.
func (s ImageSpec) Empty() bool {
	return len(s.Packages) == 0 && len(s.Files) == 0 && len(s.Env) == 0 &&
		s.Overlay == "" && isTrackingRev(s.LLMAgentsRev) && isTrackingRev(s.NixpkgsRev)
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

// ValidateSrcs checks every source's include patterns. Path/branch validation
// needs the repo on disk and lives in sandbox.resolveSrcs; this half is pure
// and runs wherever a profile is resolved, so `config validate` catches a bad
// pattern without touching git. (The src/branch-required checks live in the
// CLI's validateProfile, NOT here: resolveSrcs calls this first, and a generic
// message would shadow its richer errors — the recorded-branch hint.)
func ValidateSrcs(srcs []Src) error {
	for _, s := range srcs {
		if err := ValidateInclude(s.Include); err != nil {
			return err
		}
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
	for path := range s.Files {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("image.files key %q must be an absolute in-image path", path)
		}
	}
	for _, r := range []struct{ field, rev string }{
		{"image.llmAgentsRev", s.LLMAgentsRev},
		{"image.nixpkgsRev", s.NixpkgsRev},
	} {
		if r.rev == "" || r.rev == "latest" || imageRevRe.MatchString(r.rev) {
			continue
		}
		return fmt.Errorf("invalid %s %q — use latest or a full 40-char hex commit hash", r.field, r.rev)
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
	// NestedContainers lets the sandbox run its own ROOTLESS podman (the image
	// ships one). It is opt-in because it costs isolation: creating a user
	// namespace needs the engine's seccomp filter off and /proc unmasked, so a
	// profile that does not ask for it keeps the stock posture. Capabilities
	// stay dropped and no-new-privileges stays on either way — see
	// backend.nestedContainerArgs and SECURITY.md.
	NestedContainers bool `json:"nestedContainers,omitempty"`
	// HostConfigs wires the HOST's agent identity into the sandbox, two
	// halves under one opt-in:
	//   - config seed: the registry seed paths (~/.claude + ~/.claude.json,
	//     ~/.codex, ~/.gemini, opencode/crush/aider — settings, skills,
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
	// Limits caps the sandbox container's memory/cpus/pids; empty fields inherit
	// the SANDBOXER_MEM/SANDBOXER_CPU env defaults (memory/cpus) or stay uncapped.
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
	"network":    "renamed to egress — use egress.allowedDomains / egress.proxy / egress.noProxy / egress.routes (and egress.enabled = false to disable the allowlist)",
	"agent":      "removed — a sandbox is not bound to one agent (choose per exec); use agents: for credential passthrough and egress.routes to route an API domain through a proxy",
	"agentProxy": "removed — route by destination instead: egress.routes",
	"roots":      "removed — sandboxes are git worktrees now (no copy mode); mount other trees with extraMounts",
	"context":    "removed — a git-worktree sandbox already contains the repo's files (nothing is copied in)",
	"agents":     "removed — set hostConfigs = true to seed the sandbox home from the host's agent configs (credentials included), or log in / export API keys INSIDE the sandbox (its $HOME persists)",
	"extraPkgs":  "renamed — image.packages (same nixpkgs attr names; overlay-defined attrs may be listed too)",
	"hook":       "removed — put static customization in image.{packages,files,env}; anything needing pkgs is a plain nixpkgs overlay file: image.overlay = \"./overlay.nix\"",
	"nix":        "replaced by image.overlay — a file with a PLAIN nixpkgs overlay (final: prev: { … }); expose computed packages/files as overlay attrs and list them in image.packages",
	"deps":       "replaced by srcs — e.g. srcs = [ { src = \".\"; include = [ \"/some/dir/\" ]; } ] (src = path to a repo; include = directory paths/patterns; empty include = the whole repo)",
	"defaults":   "removed — profiles are self-contained; share between them with ordinary nix (let base = { ... }; in { profiles.web = base // { ... }; })",
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
