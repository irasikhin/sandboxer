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
)

// Pin is one stamped flake-input resolution: the remote ref a "latest" rev
// means and the commit it resolved to. ResolvedAt records when the stamp was
// taken, for human inspection of the pins file — nothing reads it back.
type Pin struct {
	Ref        string `json:"ref"`
	Rev        string `json:"rev"`
	ResolvedAt string `json:"resolvedAt,omitempty"`
}

// Pins maps a flake-input name (nixpkgs, llm-agents) to its stamped pin.
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

// pinInputs are the toolbox flake's resolvable inputs: nixpkgs tracks the
// nixos-unstable branch (the embedded pin's channel), llm-agents tracks the
// default branch (HEAD).
func pinInputs() []pinInput {
	return []pinInput{
		{name: "nixpkgs", url: "https://github.com/NixOS/nixpkgs", ref: "refs/heads/nixos-unstable"},
		{name: "llm-agents", url: "https://github.com/numtide/llm-agents.nix", ref: "HEAD"},
	}
}

// resolveRevsArgv builds the engine `run` argv for the one-shot resolver
// container: a `git ls-remote` per input inside the nix builder image (the
// same no-host-nix stance as the build itself), each head rev written to the
// bind-mounted outDir as rev.<name>. Pure: no exec, env or filesystem —
// asserted directly in tests.
func resolveRevsArgv(nixImage, outDir string, inputs []pinInput) []string {
	var script strings.Builder
	script.WriteString("set -e")
	for _, in := range inputs {
		fmt.Fprintf(&script, "; git ls-remote %s %s | cut -f1 > /out/rev.%s", in.url, in.ref, in.name)
	}
	return []string{
		"run", "--rm",
		"--volume", outDir + ":/out:rw",
		nixImage, "sh", "-lc", script.String(),
	}
}

// revHexRe is the only shape a resolved rev may take — a full 40-hex commit.
var revHexRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ResolveLatest resolves every input's remote head in one quick resolver run
// (well before any image build) and returns the freshly stamped pins.
// Fail-closed: a missing or non-40-hex rev file is an error, never a silent
// fallback to the embedded pin.
func ResolveLatest(engine, nixImage string, stderr io.Writer) (Pins, error) {
	if engine == "" {
		return nil, errors.New("no container engine to resolve latest image revs")
	}
	if nixImage == "" {
		nixImage = NixImage
	}
	if stderr == nil {
		stderr = io.Discard
	}
	outDir, err := os.MkdirTemp("", "sandboxer-pins-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(outDir) }()

	inputs := pinInputs()
	fmt.Fprintf(stderr, "sandboxer: resolving latest flake-input revs via %s…\n", nixImage)
	resolve := exec.Command(engine, resolveRevsArgv(nixImage, outDir, inputs)...)
	resolve.Stdout = stderr
	resolve.Stderr = stderr
	if err := resolve.Run(); err != nil {
		return nil, fmt.Errorf("resolve latest input revs: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	pins := make(Pins, len(inputs))
	for _, in := range inputs {
		data, err := os.ReadFile(filepath.Join(outDir, "rev."+in.name))
		if err != nil {
			return nil, fmt.Errorf("resolve latest %s rev: %w", in.name, err)
		}
		rev := strings.TrimSpace(string(data))
		if !revHexRe.MatchString(rev) {
			return nil, fmt.Errorf("resolve latest %s rev: got %q, want a 40-hex commit", in.name, rev)
		}
		pins[in.name] = Pin{Ref: in.ref, Rev: rev, ResolvedAt: now}
	}
	return pins, nil
}

// PinSpec replaces a spec's "latest" input revs with concrete commits so the
// spec can be tagged and built. Concrete and empty revs pass through
// untouched. "latest" reads the stamped pins cache; a miss (or refresh)
// resolves the remote heads once via ResolveLatest and stamps the result —
// so enter/exec never re-resolve a warm cache, only `build-image --refresh`
// moves the pins. With no engine (a dry run) a cold cache is a fail-closed
// error pointing at build-image instead of a guessing fallback.
func PinSpec(s Spec, engine string, refresh bool, stderr io.Writer) (Spec, error) {
	latestNixpkgs := s.NixpkgsRev == "latest"
	latestLLMAgents := s.LLMAgentsRev == "latest"
	if !latestNixpkgs && !latestLLMAgents {
		return s, nil
	}
	pins, err := LoadPins()
	if err != nil {
		return Spec{}, err
	}
	_, haveNixpkgs := pins["nixpkgs"]
	_, haveLLMAgents := pins["llm-agents"]
	if refresh || (latestNixpkgs && !haveNixpkgs) || (latestLLMAgents && !haveLLMAgents) {
		if engine == "" {
			return Spec{}, errors.New(`unresolved "latest" image revs and no container engine to ` +
				`resolve them — run 'sandboxer build-image' once to resolve and stamp the pins`)
		}
		resolved, err := ResolveLatest(engine, "", stderr)
		if err != nil {
			return Spec{}, err
		}
		for name, pin := range resolved {
			pins[name] = pin
		}
		if err := SavePins(pins); err != nil {
			return Spec{}, err
		}
	}
	if latestNixpkgs {
		s.NixpkgsRev = pins["nixpkgs"].Rev
	}
	if latestLLMAgents {
		s.LLMAgentsRev = pins["llm-agents"].Rev
	}
	return s, nil
}
