package sandbox

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/irasikhin/sandboxer/internal/style"
)

// PiSettingsPath is pi's global settings file, relative to the agent's home.
// pi reads no other global config (docs/settings.md: `~/.pi/agent/settings.json`
// for all projects, `.pi/settings.json` per project) — there is no /etc-level
// file the image could ship instead, which is why registration happens here,
// host-side, against the sandbox's private home.
const PiSettingsPath = ".pi/agent/settings.json"

// BakedPiPackages are the pi packages the toolbox image ships, at the STABLE
// guest paths images.nix symlinks them to (keep the two in sync — the store
// path behind each link moves on every image bump, these do not). A local path
// in pi's `packages` list is loaded verbatim, so a sandbox gets the package
// with no npm fetch, no network and no per-sandbox install.
var BakedPiPackages = []string{"/etc/sandboxer/pi-packages/agent-orchestrator"}

// EnsurePiPackages registers the image's baked-in pi packages in slug's pi
// settings, so pi comes up with them loaded on the first run in a fresh
// sandbox instead of after a manual `pi install`.
//
// It is a MERGE into whatever settings.json the home already has (host-seeded
// by SeedHome, or written by pi itself): the file is rewritten with the
// missing package paths appended to `packages`, every other setting kept. An
// entry already present — string form or the object form pi uses for resource
// filtering ({"source": …}) — is never duplicated. A settings.json that does
// not parse is left ALONE with a warning: a hand-edited or half-written file
// is the user's, and clobbering it would lose an agent's configuration.
//
// Like the config seed, this runs on create/enter/exec and is therefore
// self-healing rather than one-shot: a package dropped from the list comes
// back on the next enter. Profiles that do not want it set piPackages = false
// (SANDBOXER_NO_PI_PACKAGES=1 is the operator kill-switch).
func (b *Base) EnsurePiPackages(slug string, w io.Writer) {
	path := filepath.Join(b.HomeDir(slug), filepath.FromSlash(PiSettingsPath))
	settings, err := readPiSettings(path)
	if err != nil {
		if w != nil {
			style.Errorf(w, "pi settings ~/%s not registered: %v", PiSettingsPath, err)
		}
		return
	}
	added, err := addPiPackages(settings, BakedPiPackages)
	if err != nil {
		if w != nil {
			style.Errorf(w, "pi settings ~/%s not registered: %v", PiSettingsPath, err)
		}
		return
	}
	if len(added) == 0 {
		return
	}
	if err := writePiSettings(path, settings); err != nil {
		if w != nil {
			style.Errorf(w, "pi settings ~/%s not registered: %v", PiSettingsPath, err)
		}
		return
	}
	if w != nil {
		for _, p := range added {
			style.Infof(w, "pi: %s registered in the sandbox's pi settings", filepath.Base(p))
		}
	}
}

// readPiSettings loads pi's settings file as a generic object. A missing file
// is an empty object (the sandbox has never run pi), which is the case the
// registration exists for; anything else — unreadable, malformed, or a JSON
// value that is not an object — is an error the caller reports and skips on,
// so a user's file is never overwritten with our idea of it.
func readPiSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	settings := map[string]any{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("%s does not parse as JSON (left untouched): %w", path, err)
	}
	return settings, nil
}

// addPiPackages appends the packages settings is missing to its `packages`
// list, and returns the ones it added. A `packages` value of an unexpected
// shape (pi's schema says a list) is an error rather than something to
// overwrite — see readPiSettings.
func addPiPackages(settings map[string]any, want []string) ([]string, error) {
	var list []any
	switch v := settings["packages"].(type) {
	case nil:
	case []any:
		list = v
	default:
		return nil, fmt.Errorf("`packages` is %T, expected a list (left untouched)", v)
	}
	var added []string
	for _, p := range want {
		if piPackageListed(list, p) {
			continue
		}
		list = append(list, p)
		added = append(added, p)
	}
	if len(added) > 0 {
		settings["packages"] = list
	}
	return added, nil
}

// piPackageListed reports whether source is already in pi's package list,
// in either accepted spelling: the plain string, or the object form that
// carries per-resource filters ({"source": "…", "skills": […]}).
func piPackageListed(list []any, source string) bool {
	for _, e := range list {
		switch v := e.(type) {
		case string:
			if v == source {
				return true
			}
		case map[string]any:
			if s, ok := v["source"].(string); ok && s == source {
				return true
			}
		}
	}
	return false
}

// writePiSettings serializes settings back, staged-then-renamed like the home
// seed so an interrupted write never leaves pi a truncated settings file. Mode
// 0600: the file lives in the sandbox's private home and may hold provider
// configuration.
func writePiSettings(path string, settings map[string]any) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".sbxtmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
