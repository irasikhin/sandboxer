package config

import (
	"os"
	"path/filepath"
)

// StateDirName is the per-project state directory holding sandbox copies and
// metadata (the bash STATE_DIR_NAME).
const StateDirName = ".sandboxer"

// ConfigFileName is the project-local profile file auto-discovered under the
// state dir (see ConfigPath). It is committed alongside the image hook via the
// allowlisting .sandboxer/.gitignore.
const ConfigFileName = "config.yaml"

// ConfigPath is the cwd-relative location of the project profile —
// .sandboxer/config.yaml — used for display and as the default when no project
// root is known.
func ConfigPath() string { return filepath.Join(StateDirName, ConfigFileName) }

// ConfigPathIn is the project profile under a given project root (absolute or
// relative): <root>/.sandboxer/config.yaml. Discovery and scaffolding use it so
// --src (or any explicit project root) locates the config, not just the cwd.
func ConfigPathIn(root string) string {
	return filepath.Join(root, StateDirName, ConfigFileName)
}

// LegacyConfigFileName is the pre-consolidation root-level profile path. It is
// no longer read; discovery only uses it to print a one-line migration hint
// when it is present but the new location is not.
const LegacyConfigFileName = ".sandboxer.yaml"

// DefaultImage is the toolbox image reference used by the container backend.
const DefaultImage = "sandboxer-toolbox:latest"

// DefaultProxyImage is the egress-proxy image: a minimal squid that enforces the
// domain allowlist for a sandbox. It is built locally beside the toolbox image
// (sandboxer image build) and runs as the egress sidecar — the sandboxer binary
// is never in the network path.
const DefaultProxyImage = "sandboxer-proxy:latest"

// ProxyImage returns the egress-proxy image reference (SANDBOXER_PROXY_IMAGE
// override, else the built-in default).
func ProxyImage() string { return envOr("SANDBOXER_PROXY_IMAGE", DefaultProxyImage) }

// DefaultDomains is the egress allowlist used when none is configured: AI API
// endpoints plus common package registries across ecosystems.
const DefaultDomains = "api.anthropic.com,api.openai.com,api.deepseek.com," +
	"generativelanguage.googleapis.com,openrouter.ai,registry.npmjs.org,pypi.org," +
	"files.pythonhosted.org,repo.maven.apache.org,repo1.maven.org,central.sonatype.com," +
	"plugins.gradle.org,services.gradle.org,crates.io,static.crates.io,index.crates.io," +
	"proxy.golang.org,sum.golang.org,rubygems.org,github.com,codeload.github.com," +
	"raw.githubusercontent.com,objects.githubusercontent.com,api.github.com"

// Defaults holds the env-derived defaults (SANDBOXER_*), the lowest-precedence
// layer below profile values and command flags.
type Defaults struct {
	Model   string
	Agent   string
	Backend string
	Session string
	Domains string
	Image   string
	Engine  string
	Proxy   string // SANDBOXER_PROXY — global proxy URL, lowest precedence
	NoProxy string // SANDBOXER_NO_PROXY — NO_PROXY for direct mode
	Mem     string
	CPU     string
}

// LoadDefaults reads the SANDBOXER_* environment.
func LoadDefaults() Defaults {
	return Defaults{
		Model:   os.Getenv("SANDBOXER_MODEL"),
		Agent:   envOr("SANDBOXER_AGENT", "claude"),
		Backend: envOr("SANDBOXER_BACKEND", "docker"),
		Session: os.Getenv("SANDBOXER_SESSION"),
		Domains: envOr("SANDBOXER_DOMAINS", DefaultDomains),
		Image:   envOr("SANDBOXER_IMAGE", DefaultImage),
		Engine:  os.Getenv("SANDBOXER_ENGINE"),
		Proxy:   os.Getenv("SANDBOXER_PROXY"),
		NoProxy: os.Getenv("SANDBOXER_NO_PROXY"),
		Mem:     os.Getenv("SANDBOXER_MEM"),
		CPU:     os.Getenv("SANDBOXER_CPU"),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
