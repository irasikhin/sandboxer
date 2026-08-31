package toolbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/irasikhin/sandboxer/internal/style"
)

// Pin is one stamped flake-input resolution: the remote ref a "latest" rev
// means and the commit it resolved to. ResolvedAt records when the stamp was
// taken, for human inspection of the pins file — nothing reads it back.
type Pin struct {
	Ref        string `json:"ref"`
	Rev        string `json:"rev"`
	ResolvedAt string `json:"resolvedAt,omitempty"`
}

// Pins maps a flake-input name (nixpkgs) to its stamped pin.
type Pins map[string]Pin

// pinsFileName is the pins file under the per-user sandboxer cache dir.
const pinsFileName = "image-pins.json"

// PinsPath is the stamped-pins location: <user cache>/sandboxer/image-pins.json
// (os.UserCacheDir honors XDG_CACHE_HOME).
func PinsPath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate cache dir for image pins: %w", err)
	}
	return filepath.Join(dir, "sandboxer", pinsFileName), nil
}

// LoadPins reads the stamped pins. A missing file is a cold cache (empty pins,
// no error); a malformed file is an error naming the path, so the user can
// inspect or delete it rather than silently re-resolving over it.
func LoadPins() (Pins, error) {
	path, err := PinsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Pins{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read image pins: %w", err)
	}
	var p Pins
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse image pins %s: %w", path, err)
	}
	return p, nil
}

// SavePins stamps the pins atomically (temp file + rename inside the cache
// dir), so a concurrent reader never sees a torn file.
func SavePins(p Pins) error {
	path, err := PinsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create image pins dir: %w", err)
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), pinsFileName+".tmp-")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// pinInput describes one resolvable flake input: the pins-file key (also the
// rev.<name> file the resolver container writes), the git remote, and the ref
// whose head a "latest" rev means.
type pinInput struct {
	name string
	url  string
	ref  string
}

// pinInputs are the toolbox flake's resolvable inputs: nixpkgs, tracking the
// nixos-unstable branch (the embedded pin's channel). Agents ride the same
// input — prebuilt nixpkgs packages, plus the vendored pi grafted by our
// overlay — so there is nothing else to resolve.
func pinInputs() []pinInput {
	return []pinInput{
		{name: "nixpkgs", url: "https://github.com/NixOS/nixpkgs", ref: "refs/heads/nixos-unstable"},
	}
}

// revHexRe is the only shape a resolved rev may take — a full 40-hex commit.
var revHexRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// resolveRevsHostGit resolves every input's remote head with the host's `git`
// ls-remote — the same operation the old resolver ran inside a container, on
// the host because git is already a hard requirement (every source is a git
// worktree). No engine, no guest, no builder image: a cold pins cache now
// resolves on a container-less host instead of failing closed. Fail-closed on
// the VALUE: a missing ref or a non-40-hex rev is an error, never a silent
// fallback to the embedded pin.
func resolveRevsHostGit(inputs []pinInput) (map[string]string, error) {
	revs := make(map[string]string, len(inputs))
	for _, in := range inputs {
		out, err := exec.Command("git", "ls-remote", in.url, in.ref).Output()
		if err != nil {
			return nil, fmt.Errorf("resolve latest %s rev (git ls-remote %s %s): %w",
				in.name, in.url, in.ref, err)
		}
		// `git ls-remote` prints one "<sha>\t<ref>" line per match; take the sha.
		rev := ""
		if fields := strings.Fields(string(out)); len(fields) > 0 {
			rev = fields[0]
		}
		if !revHexRe.MatchString(rev) {
			return nil, fmt.Errorf("resolve latest %s rev: got %q, want a 40-hex commit", in.name, rev)
		}
		revs[in.name] = rev
	}
	return revs, nil
}

// ResolveLatest resolves every input's remote head with the host's git (see
// resolveRevsHostGit) and returns the freshly stamped pins.
func ResolveLatest(stderr io.Writer) (Pins, error) {
	if stderr == nil {
		stderr = io.Discard
	}
	inputs := pinInputs()
	style.Infof(stderr, "resolving latest flake-input revs via host git…")
	revs, err := resolveRevsHostGit(inputs)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	pins := make(Pins, len(inputs))
	for _, in := range inputs {
		pins[in.name] = Pin{Ref: in.ref, Rev: revs[in.name], ResolvedAt: now}
	}
	return pins, nil
}

// PinSpec replaces a spec's tracking input revs — "" and "latest" both mean
// "the remote head" — with concrete commits so the spec can be tagged and
// built. Concrete (40-hex) revs pass through untouched. A tracking rev reads
// the stamped pins cache; a miss (or refresh) resolves the remote heads once
// via ResolveLatest (host git — no engine, no guest) and stamps the result —
// so enter/exec never re-resolve a warm cache, only `image build` (which
// refreshes by default) moves the pins. A miss stamps ONLY the inputs that
// were missing (an existing stamp another profile relies on never moves as a
// side effect); refresh re-stamps everything. A cold cache resolves on a
// container-less host exactly as it does on one with docker/podman — there is
// no engine anywhere in this path.
func PinSpec(s Spec, refresh bool, stderr io.Writer) (Spec, error) {
	if !isLatestRev(s.NixpkgsRev) {
		return s, nil
	}
	pins, err := LoadPins()
	if err != nil {
		return Spec{}, err
	}
	if _, have := pins["nixpkgs"]; refresh || !have {
		resolved, err := ResolveLatest(stderr)
		if err != nil {
			return Spec{}, err
		}
		for name, pin := range resolved {
			if _, ok := pins[name]; !ok || refresh {
				pins[name] = pin
			}
		}
		if err := SavePins(pins); err != nil {
			return Spec{}, err
		}
	}
	s.NixpkgsRev = pins["nixpkgs"].Rev
	return s, nil
}
