package config

import (
	"os"
	"strconv"
)

// StateDirName is the per-project state directory holding sandbox copies and
// metadata (the bash STATE_DIR_NAME).
const StateDirName = ".sandboxer"

// ConfigFileName is the project-local profile file auto-discovered in the cwd
// (a dotfile, so it stays out of directory listings).
const ConfigFileName = ".sandboxer.yaml"

// DefaultImage is the toolbox image reference used by the container backend.
const DefaultImage = "sandboxer-toolbox:latest"

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
	Model       string
	Agent       string
	Backend     string
	Session     string
	Domains     string
	Image       string
	Engine      string
	MaxParallel int
	Mem         string
	CPU         string
	Wall        string
}

// LoadDefaults reads the SANDBOXER_* environment.
func LoadDefaults() Defaults {
	return Defaults{
		Model:       os.Getenv("SANDBOXER_MODEL"),
		Agent:       envOr("SANDBOXER_AGENT", "claude"),
		Backend:     envOr("SANDBOXER_BACKEND", "podman"),
		Session:     os.Getenv("SANDBOXER_SESSION"),
		Domains:     envOr("SANDBOXER_DOMAINS", DefaultDomains),
		Image:       envOr("SANDBOXER_IMAGE", DefaultImage),
		Engine:      os.Getenv("SANDBOXER_ENGINE"),
		MaxParallel: envInt("SANDBOXER_MAX_PARALLEL", 4),
		Mem:         os.Getenv("SANDBOXER_MEM"),
		CPU:         os.Getenv("SANDBOXER_CPU"),
		Wall:        os.Getenv("SANDBOXER_WALL"),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
