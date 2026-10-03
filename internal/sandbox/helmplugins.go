package sandbox

import (
	"io"
	"os"
	"path/filepath"

	"github.com/irasikhin/sandboxer/internal/style"
)

// HelmPluginsPath is helm's plugin directory, relative to the agent's home.
// helm has no /etc-level plugin directory: HELM_PLUGINS names ONE directory and
// its default is the XDG data path (~/.local/share/helm/plugins — the guest
// sets no XDG_DATA_HOME, so that is where helm looks). The image therefore
// cannot make helm find a plugin by dropping a file at a system path; it ships
// the plugin at a stable path and this links it into the home.
const HelmPluginsPath = ".local/share/helm/plugins"

// bakedHelmPluginsRoot is where images.nix exposes the baked helm plugins in the
// guest (keep in sync — the store path behind each link moves on every image
// bump, this does not). It is a GUEST path: the links below live in the
// host-side sandbox home, which mounts as $HOME in the sandbox, and they are
// only ever resolved there — the host never dereferences them.
const bakedHelmPluginsRoot = "/etc/sandboxer/helm-plugins"

// BakedHelmPlugins are the helm plugins the toolbox image ships, by leaf name:
// each is a symlink under bakedHelmPluginsRoot in the guest. helm-diff is the
// one `helmfile diff`/`helmfile apply` shells out to, so linking it is what
// makes those work with no `helm plugin install` (which would need network and
// a writable plugin dir).
var BakedHelmPlugins = []string{"helm-diff"}

// EnsureHelmPlugins links the image's baked helm plugins into slug's home, so
// helm resolves them with no per-sandbox install. Like the config seed and the
// pi augmentation it runs on create/enter/exec and is therefore self-healing: a
// link deleted inside the sandbox comes back on the next enter.
//
// An existing entry — whatever it is — is left untouched: a user-installed or
// hand-pinned plugin in the home is the user's, and a dangling symlink is still
// an entry to respect, not one to overwrite. Failures are reported and skipped:
// a plugin link is a convenience, never a reason to fail the enter that was
// actually asked for. Opt out with SANDBOXER_NO_HELM_PLUGINS=1.
func (b *Base) EnsureHelmPlugins(slug string, w io.Writer) {
	dir := filepath.Join(b.HomeDir(slug), filepath.FromSlash(HelmPluginsPath))
	for _, name := range BakedHelmPlugins {
		target := filepath.Join(dir, name)
		// Lstat, not Stat: a symlink counts as taken even when its target is
		// not visible from here (the host has no /etc/sandboxer).
		if _, err := os.Lstat(target); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			helmPluginError(w, name, err)
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			helmPluginError(w, name, err)
			continue
		}
		if err := os.Symlink(bakedHelmPluginsRoot+"/"+name, target); err != nil {
			helmPluginError(w, name, err)
			continue
		}
		if w != nil {
			style.Infof(w, "helm: baked plugin %s linked into the sandbox home", name)
		}
	}
}

// helmPluginError reports one plugin that could not be linked, naming the
// plugin so a partial failure says which one is missing.
func helmPluginError(w io.Writer, name string, err error) {
	if w != nil {
		style.Errorf(w, "helm: baked plugin %s not linked: %v", name, err)
	}
}
