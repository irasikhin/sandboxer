package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// EvalConfig evaluates a sandboxer.nix config to canonical JSON via the host
// nix — the one hard host dependency of the config layer (the toolbox image
// still builds inside a container; this is evaluation only, no daemon and no
// store writes).
//
// The evaluation is SANDBOXED with restrict-eval: imports and builtins.readFile
// may only touch paths under the config's own directory (NIX_PATH is cleared
// and re-seeded with just that directory), and all network access
// (builtins.fetchurl & co) is forbidden — so a cloned repo's config cannot read
// files outside its own directory or fetch from the network at eval time. It is
// NOT a *pure* eval, though: builtins.getEnv still reads the host environment
// (restrict-eval does not gate it), so a config can pull a host env var into its
// output. That is consistent with the "config is code" model — a sandboxer.nix
// is trusted to the same degree as its setup script and extraMounts; read an
// untrusted repo's config before create/enter. See SECURITY.md.
func EvalConfig(file string) ([]byte, error) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, err
	}
	dir := filepath.Dir(abs)
	cmd := exec.Command("nix-instantiate",
		"--eval", "--strict", "--json",
		"--option", "restrict-eval", "true",
		"-I", dir,
		abs)
	cmd.Env = append(os.Environ(), "NIX_PATH=")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) && errors.Is(execErr.Err, exec.ErrNotFound) {
			return nil, errors.New("nix is required to evaluate " + ConfigFileName +
				" but is not on PATH — install it: https://nixos.org/download " +
				"(Linux/macOS; on Windows use WSL2)")
		}
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s: nix eval failed:\n%s", file, msg)
	}
	return out.Bytes(), nil
}
