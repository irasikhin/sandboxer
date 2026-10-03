package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// helmPluginLink returns the plugin entry's link target as recorded on disk —
// read with Readlink, which does NOT follow the link: the link names a GUEST
// path (/etc/sandboxer/…) that does not exist on the host by design.
func helmPluginLink(t *testing.T, b *Base, name string) string {
	t.Helper()
	path := filepath.Join(b.HomeDir("s"), filepath.FromSlash(HelmPluginsPath), name)
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("read link %s: %v", path, err)
	}
	return target
}

// TestEnsureHelmPluginsLinksFreshHome: a sandbox that has never installed a helm
// plugin has no plugin dir at all — the baked plugin must still be linked in, or
// `helmfile diff`/`apply` would fail with "plugin diff not found" until someone
// ran `helm plugin install` (which needs network and a writable plugin dir).
func TestEnsureHelmPluginsLinksFreshHome(t *testing.T) {
	b := newPiBase(t)
	var progress strings.Builder
	b.EnsureHelmPlugins("s", &progress)

	for _, name := range BakedHelmPlugins {
		if got, want := helmPluginLink(t, b, name), bakedHelmPluginsRoot+"/"+name; got != want {
			t.Errorf("%s -> %q, want %q", name, got, want)
		}
		if !strings.Contains(progress.String(), name) {
			t.Errorf("link not narrated: %q", progress.String())
		}
	}
	// The parent dirs (.local/share/helm/plugins) are created on the way.
	fi, err := os.Stat(filepath.Join(b.HomeDir("s"), filepath.FromSlash(HelmPluginsPath)))
	if err != nil || !fi.IsDir() {
		t.Fatalf("plugin dir not created: %v (err=%v)", fi, err)
	}
}

// TestEnsureHelmPluginsIsIdempotent: the link runs on every create/enter/exec.
// A second call must leave the existing entry exactly as it is — a rewrite would
// be pointless churn, and a symlink replaced on every enter would break a plugin
// the user had swapped for their own.
func TestEnsureHelmPluginsIsIdempotent(t *testing.T) {
	b := newPiBase(t)
	b.EnsureHelmPlugins("s", nil)
	before := helmPluginLink(t, b, BakedHelmPlugins[0])

	var progress strings.Builder
	b.EnsureHelmPlugins("s", &progress)

	if after := helmPluginLink(t, b, BakedHelmPlugins[0]); after != before {
		t.Errorf("link rewritten: %q -> %q", before, after)
	}
	// Nothing changed, so nothing is announced either.
	if progress.Len() != 0 {
		t.Errorf("no-op link narrated: %q", progress.String())
	}
}

// TestEnsureHelmPluginsKeepsUserEntry: an entry that already exists is the
// user's — a hand-installed plugin, a pinned version, or a link that no longer
// resolves (the host cannot see the guest's /etc/sandboxer, so a link written by
// a PREVIOUS run is indistinguishable from a broken one here). Any of them must
// survive untouched.
func TestEnsureHelmPluginsKeepsUserEntry(t *testing.T) {
	name := BakedHelmPlugins[0]

	t.Run("user plugin dir", func(t *testing.T) {
		b := newPiBase(t)
		dir := filepath.Join(b.HomeDir("s"), filepath.FromSlash(HelmPluginsPath), name)
		marker := filepath.Join(dir, "marker.txt")
		writeFile(t, marker, "user-installed")

		var progress strings.Builder
		b.EnsureHelmPlugins("s", &progress)

		if data, err := os.ReadFile(marker); err != nil || string(data) != "user-installed" {
			t.Errorf("user plugin dir clobbered: %q (err=%v)", data, err)
		}
		if progress.Len() != 0 {
			t.Errorf("existing entry narrated: %q", progress.String())
		}
	})

	t.Run("user symlink", func(t *testing.T) {
		b := newPiBase(t)
		dir := filepath.Join(b.HomeDir("s"), filepath.FromSlash(HelmPluginsPath))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("elsewhere", filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}

		b.EnsureHelmPlugins("s", nil)

		if got := helmPluginLink(t, b, name); got != "elsewhere" {
			t.Errorf("user symlink replaced: -> %q, want %q", got, "elsewhere")
		}
	})
}

// TestEnsureHelmPluginsNilWriter: the link runs on paths (create/exec) that have
// no progress writer — that must be a silent success, not a panic.
func TestEnsureHelmPluginsNilWriter(t *testing.T) {
	b := newPiBase(t)
	b.EnsureHelmPlugins("s", nil)

	if got, want := helmPluginLink(t, b, BakedHelmPlugins[0]), bakedHelmPluginsRoot+"/"+BakedHelmPlugins[0]; got != want {
		t.Errorf("link = %q, want %q", got, want)
	}
}

// TestEnsureHelmPluginsSurvivesUnwritableHome: an unwritable home must warn per
// plugin and move on — the links are a convenience, never a reason to fail the
// enter that was actually asked for.
func TestEnsureHelmPluginsSurvivesUnwritableHome(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere — the permission trap needs an unprivileged user")
	}
	b := newPiBase(t)
	home := b.HomeDir("s")
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })

	var progress strings.Builder
	b.EnsureHelmPlugins("s", &progress)

	if !strings.Contains(progress.String(), "not linked") {
		t.Errorf("failure not warned about: %q", progress.String())
	}
}
