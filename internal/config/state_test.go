package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigLocations pins the path helpers discovery and the egress sidecar
// rely on: the per-root config path, the legacy migration-hint location, and
// the proxy-image reference with its env override.
func TestConfigLocations(t *testing.T) {
	if got, want := ConfigPathIn("/proj"), filepath.Join("/proj", ConfigFileName); got != want {
		t.Errorf("ConfigPathIn = %q, want %q", got, want)
	}
	if got, want := LegacyConfigDirPath(), filepath.Join(LegacyStateDirName, "config.yaml"); got != want {
		t.Errorf("LegacyConfigDirPath = %q, want %q", got, want)
	}
	t.Setenv("SANDBOXER_PROXY_IMAGE", "")
	if got := ProxyImage(); got != DefaultProxyImage {
		t.Errorf("ProxyImage = %q, want the default %q", got, DefaultProxyImage)
	}
	t.Setenv("SANDBOXER_PROXY_IMAGE", "my-proxy:1")
	if got := ProxyImage(); got != "my-proxy:1" {
		t.Errorf("ProxyImage override = %q, want my-proxy:1", got)
	}
}

// TestStateDir pins the resolution order: SANDBOXER_STATE override, then
// $XDG_STATE_HOME, then ~/.local/state — and "" when no home can be found.
func TestStateDir(t *testing.T) {
	proj := "/work/myproj"
	id := projectID(proj)

	t.Setenv("SANDBOXER_STATE", "/explicit")
	t.Setenv("XDG_STATE_HOME", "/xdg")
	t.Setenv("HOME", "/home/u")
	if got, want := StateDir(proj), filepath.Join("/explicit", id); got != want {
		t.Errorf("override: StateDir = %q, want %q", got, want)
	}

	t.Setenv("SANDBOXER_STATE", "")
	if got, want := StateDir(proj), filepath.Join("/xdg", "sandboxer", id); got != want {
		t.Errorf("xdg: StateDir = %q, want %q", got, want)
	}

	t.Setenv("XDG_STATE_HOME", "")
	if got, want := StateDir(proj), filepath.Join("/home/u", ".local", "state", "sandboxer", id); got != want {
		t.Errorf("home: StateDir = %q, want %q", got, want)
	}

	t.Setenv("HOME", "")
	if got := StateDir(proj); got != "" {
		t.Errorf("no home: StateDir = %q, want \"\"", got)
	}
}

// TestProjectID: the id embeds the base name (readable) and a short path hash
// (so same-named checkouts in different paths never collide).
func TestProjectID(t *testing.T) {
	a := projectID("/a/myproj")
	b := projectID("/b/myproj")
	if a == b {
		t.Errorf("same-named projects at different paths collided: %q", a)
	}
	if !strings.HasPrefix(a, "myproj-") {
		t.Errorf("projectID = %q, want it to start with the base name", a)
	}
	// Stable for the same path.
	if projectID("/a/myproj") != a {
		t.Error("projectID is not deterministic")
	}
	// A root-ish path still yields a usable id.
	if id := projectID("/"); !strings.HasPrefix(id, "root-") {
		t.Errorf("projectID(/) = %q, want it to start with root-", id)
	}
}
