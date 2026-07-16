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
	"fmt"
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
	// Src is the path to the repository's top-level directory ("." = the
	// project itself). Relative paths resolve against the PROJECT ROOT
	// (--src, default cwd) — one rule regardless of where the profile file
	// lives (project root or an explicit -f path).
	Src string `json:"src"`
	// Include are gitignore-syntax patterns (non-cone sparse-checkout:
	// "/services/api/", "*.md", "!…") selecting what the sandbox sees. Empty
	// or ["**"] means the whole repo.
	Include []string `json:"include,omitempty"`
	// Branch names the branch the source is checked out on. It is REQUIRED —
	// there is no default naming — and it also names the worktree's directory
	// (…-sandboxes/<slug>/<repo>/<branch>). An existing worktree of that
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
	// LLMAgentsRev / NixpkgsRev override the embedded flake-input pins: empty
	// keeps the embedded pin, "latest" resolves the remote head once at build
	// time, and a full 40-hex commit hash pins exactly (see ValidateImageSpec).
	LLMAgentsRev string `json:"llmAgentsRev,omitempty"`
	NixpkgsRev   string `json:"nixpkgsRev,omitempty"`
}

// Empty reports whether the spec requests no customization, i.e. the sandbox
// runs the stock toolbox image.
func (s ImageSpec) Empty() bool {
	return len(s.Packages) == 0 && len(s.Files) == 0 && len(s.Env) == 0 &&
		s.Overlay == "" && s.LLMAgentsRev == "" && s.NixpkgsRev == ""
}

var imageRevRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ValidateImageSpec rejects a malformed flake-input revision override. Each rev
// is "" (keep the embedded pin), "latest" (resolve the remote head at build
// time) or a full 40-character lowercase hex commit hash. Short prefixes are
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
	// ../<project>-sandboxes/ beside the project. The sandbox occupies
	// <worktreesDir>/<name>/<repo>/<branch>. Set it before creating the
	// sandbox — changing it later sets the existing worktrees aside under the
	// old location's _detached/ and checks out fresh ones at the new place.
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
	// Session selects how enter/exec use the container: "persistent" (the
	// default) keeps one detached session container running across invocations,
	// "ephemeral" starts a fresh one-shot container per command.
	Session string `json:"session,omitempty"`
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
	"agents":     "removed — no credentials are passed through anymore; log in or export API keys INSIDE the sandbox (its $HOME persists)",
	"extraPkgs":  "renamed — image.packages (same nixpkgs attr names; overlay-defined attrs may be listed too)",
	"hook":       "removed — put static customization in image.{packages,files,env}; anything needing pkgs is a plain nixpkgs overlay file: image.overlay = \"./overlay.nix\"",
	"nix":        "replaced by image.overlay — a file with a PLAIN nixpkgs overlay (final: prev: { … }); expose computed packages/files as overlay attrs and list them in image.packages",
	"deps":       "replaced by srcs — e.g. srcs = [ { src = \".\"; include = [ \"/some/dir/\" ]; } ] (src = path to a repo; include = gitignore-style patterns; empty include = the whole repo)",
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
