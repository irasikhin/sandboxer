package config

import (
	"os"
	"path/filepath"
)

// ConfigFileName is the project config, committed at the project root next
// to the code it configures. It is a NIX file, evaluated by the host nix
// under restricted eval (see EvalConfig) — nix on the host is a hard
// requirement of the CLI.
const ConfigFileName = "sandboxer.nix"

// LegacyYAMLConfigFileName is the retired YAML-era config (v0.24–v0.32). It
// is no longer read; discovery only prints a translate-by-hand hint when it
// is present but sandboxer.nix is not.
const LegacyYAMLConfigFileName = "sandboxer.yaml"

// ConfigPath is the cwd-relative location of the project config —
// sandboxer.nix — used for display and as the default when no project root is
// known.
func ConfigPath() string { return ConfigFileName }

// ConfigPathIn is the project config under a given project root (absolute or
// relative): <root>/sandboxer.nix. Discovery and scaffolding use it so --src
// (or any explicit project root) locates the config, not just the cwd.
func ConfigPathIn(root string) string {
	return filepath.Join(root, ConfigFileName)
}

// LegacyStateDirName is the pre-relocation .sandboxer/ project directory that
// held the committed config (config.yaml + image.nix) and, before the
// config/data split, the runtime state. It is no longer read; only migration
// hints and doctor's leftover checks look at it.
const LegacyStateDirName = ".sandboxer"

// LegacyConfigDirPath is the pre-relocation committed profile path
// (.sandboxer/config.yaml). Discovery only uses it to print a one-line
// migration hint when it is present but sandboxer.nix is not.
func LegacyConfigDirPath() string { return filepath.Join(LegacyStateDirName, "config.yaml") }

// LegacyConfigFileName is the ancient pre-consolidation root-level profile
// path. It is no longer read; discovery only uses it to print a one-line
// migration hint when it is present but the new location is not.
const LegacyConfigFileName = ".sandboxer.yaml"

// DefaultImage is the toolbox image reference the backend boots: the PREBUILT
// stock image, published to GHCR by .github/workflows/image.yml (nightly, so
// `latest` tracks the agents' releases, plus a :vX.Y.Z tag per release). msb
// pulls and caches it host-side on first create; `sandboxer image pull`
// refreshes a moved `latest`, and `sandboxer image build` builds the same
// image locally under this ref (customized profiles, offline hosts).
// SANDBOXER_IMAGE overrides it as ever.
const DefaultImage = "ghcr.io/irasikhin/sandboxer-toolbox:latest"

// DefaultDomains is the egress allowlist used when none is configured: AI API
// endpoints, common package registries across ecosystems, and the container
// registries the in-sandbox rootless podman pulls from. The Anthropic set
// covers the API plus the auth/config endpoints Claude Code v2.x reaches on
// startup (platform.claude.com, console.anthropic.com) — omitting them leaves
// the CLI unable to connect even though api.anthropic.com is allowed.
//
// cloudfront.net is where registry BLOBS actually come from: public.ecr.aws
// redirects every blob there unconditionally and docker.io does so for some
// regions/accounts, so without it a pull 403s halfway through the manifest.
// The entry admits any CloudFront distribution — a real allowlist widening,
// called out in SECURITY.md; drop it in egress.allowedDomains if the ECR/Hub
// blob path is not worth that.
const DefaultDomains = "api.anthropic.com,platform.claude.com,console.anthropic.com," +
	"api.openai.com,api.deepseek.com," +
	"generativelanguage.googleapis.com,openrouter.ai,registry.npmjs.org,pypi.org," +
	"files.pythonhosted.org,repo.maven.apache.org,repo1.maven.org,central.sonatype.com," +
	"plugins.gradle.org,services.gradle.org,crates.io,static.crates.io,index.crates.io," +
	"proxy.golang.org,sum.golang.org,rubygems.org,github.com,codeload.github.com," +
	"raw.githubusercontent.com,objects.githubusercontent.com,api.github.com," +
	"docker.io,registry-1.docker.io,auth.docker.io,index.docker.io," +
	"production.cloudflare.docker.com,mirror.gcr.io,ghcr.io," +
	"pkg-containers.githubusercontent.com,quay.io,cdn01.quay.io,cdn02.quay.io,cdn03.quay.io," +
	"public.ecr.aws,cloudfront.net"

// Defaults holds the env-derived defaults (SANDBOXER_*), the lowest-precedence
// layer below profile values and command flags.
type Defaults struct {
	Backend string
	Session string
	Domains string
	Image   string
	Proxy   string // SANDBOXER_PROXY — global proxy URL, lowest precedence
	NoProxy string // SANDBOXER_NO_PROXY — NO_PROXY for direct mode
	Mem     string
	CPU     string
	// Ports is the SANDBOXER_PORTS default — a csv of the same specs the
	// profile's `ports` takes, for the forward you want in EVERY sandbox
	// (export it once). Lowest precedence: a profile's own `ports` replaces
	// it, and an explicitly empty `ports = [ ]` means no forwards rather than
	// "fall back to the env".
	Ports string
	// NoResume (SANDBOXER_NO_RESUME=1) is the operator kill-switch for the
	// session restore's agent auto-resume — it wins over a profile's
	// autoResume, like SANDBOXER_NO_EGRESS over egress.enabled.
	NoResume bool
	// NoPiPackages (SANDBOXER_NO_PI_PACKAGES=1) is the same kind of kill-switch
	// for registering the image's baked-in pi packages in a sandbox's pi
	// settings — it wins over a profile's piPackages.
	NoPiPackages bool
	// NoPorts (SANDBOXER_NO_PORTS=1) drops every published port — the operator
	// kill-switch for the one inbound path into a sandbox, alongside
	// SANDBOXER_NO_EGRESS for the outbound one.
	NoPorts bool
}

// LoadDefaults reads the SANDBOXER_* environment.
func LoadDefaults() Defaults {
	return Defaults{
		Backend:      envOr("SANDBOXER_BACKEND", "microsandbox"),
		Session:      os.Getenv("SANDBOXER_SESSION"),
		Domains:      envOr("SANDBOXER_DOMAINS", DefaultDomains),
		Image:        envOr("SANDBOXER_IMAGE", DefaultImage),
		Proxy:        os.Getenv("SANDBOXER_PROXY"),
		NoProxy:      os.Getenv("SANDBOXER_NO_PROXY"),
		Mem:          os.Getenv("SANDBOXER_MEM"),
		CPU:          os.Getenv("SANDBOXER_CPU"),
		Ports:        os.Getenv("SANDBOXER_PORTS"),
		NoResume:     os.Getenv("SANDBOXER_NO_RESUME") == "1",
		NoPiPackages: os.Getenv("SANDBOXER_NO_PI_PACKAGES") == "1",
		NoPorts:      os.Getenv("SANDBOXER_NO_PORTS") == "1",
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
