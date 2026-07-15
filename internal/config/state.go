package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// StateDir is the per-project RUNTIME state directory: sandbox working copies,
// per-sandbox agent homes, metadata and logs. Unlike the committed config
// (sandboxer.nix, which lives beside the source and is
// meant for git), state lives OUTSIDE the repository so runtime data — and
// especially the agent homes that may hold login tokens — can never be
// committed by accident. Resolution order:
//   - $SANDBOXER_STATE (explicit override), joined with the project id;
//   - $XDG_STATE_HOME/sandboxer/<project-id>;
//   - ~/.local/state/sandboxer/<project-id>.
//
// <project-id> is the project's base name plus a short hash of its absolute
// path, so two checkouts that share a base name never collide. It returns ""
// only when no home can be determined and no override is set — callers that
// need a guaranteed directory treat that as an error.
func StateDir(projectRoot string) string {
	abs, err := filepath.Abs(projectRoot)
	if err != nil {
		abs = projectRoot
	}
	id := projectID(abs)
	if d := os.Getenv("SANDBOXER_STATE"); d != "" {
		return filepath.Join(d, id)
	}
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "sandboxer", id)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "state", "sandboxer", id)
}

// projectID is a stable, human-readable identifier for a project root: its base
// name plus a short hash of the absolute path. The hash disambiguates two
// checkouts with the same base name; the base name keeps the state directory
// recognizable when a human pokes around ~/.local/state/sandboxer.
func projectID(abs string) string {
	sum := sha256.Sum256([]byte(abs))
	short := hex.EncodeToString(sum[:])[:12]
	base := filepath.Base(abs)
	switch base {
	case "", ".", string(filepath.Separator):
		base = "root"
	}
	return base + "-" + short
}
