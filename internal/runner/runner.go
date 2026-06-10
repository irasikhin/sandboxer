// Package runner executes a batch of autonomous headless agents, one per task,
// in parallel with optional resource limits (the bash `sandboxer run`).
package runner

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/registry"
	"github.com/irasikhin/sandboxer/internal/sandbox"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

const preamble = "You are in an isolated copy of the project. Act autonomously, " +
	"without asking questions. Do not go outside the current directory. Leave the " +
	"tree consistent. Task:"

// Task is one [slug] section of the tasks file.
type Task struct {
	Slug string
	Body string
}

// Options configures a batch run.
type Options struct {
	Src        string // project root (default: cwd)
	ConfigPath string // optional YAML profile
	TasksFile  string // tasks file (default <root>/sandboxer.tasks)
	Overrides  config.Overrides
	Defaults   config.Defaults
	Image      string
	MaxP       int
	Mem        string
	CPU        string
	Wall       string
	Keep       bool
	DryRun     bool
	NoSetup    bool
	Stdout     io.Writer
	Stderr     io.Writer
}

// Result reports where the batch ran so the caller can print the summary list.
// Failed counts tasks that never produced a clean run — agents that exited
// non-zero plus sandboxes that failed to materialise — so the caller can exit
// non-zero instead of masking a partial batch behind a success code.
type Result struct {
	Root   string
	Count  int // sandboxes launched
	Failed int // launched agents that exited non-zero + make-sandbox failures
}

// Run resolves the project, expands the tasks file, creates a sandbox per task
// and launches the agents in parallel.
func Run(o Options) (Result, error) {
	if err := validateLimits(o.Mem, o.CPU, o.Wall); err != nil {
		return Result{}, err
	}
	var profile *config.Profile
	root := o.Src
	if o.ConfigPath != "" {
		doc, err := config.LoadDocument(o.ConfigPath)
		if err != nil {
			return Result{}, fmt.Errorf("load profile %s: %w", o.ConfigPath, err)
		}
		// A batch run uses the file's default (or sole) profile; a multi-profile
		// file with several sections and no `default:` is an error here.
		p, err := doc.Select("")
		if err != nil {
			return Result{}, fmt.Errorf("profile %s: %w", o.ConfigPath, err)
		}
		profile = p
		if o.Overrides.Agent == "" && p.Agent != "" {
			o.Overrides.Agent = p.Agent
		}
	}
	if root == "" {
		root, _ = os.Getwd()
	}
	tasksFile := o.TasksFile
	if tasksFile == "" {
		tasksFile = filepath.Join(root, "sandboxer.tasks")
	}
	tasks, err := parseTasksFile(tasksFile)
	if err != nil {
		return Result{}, fmt.Errorf("tasks file not found: %s (see sandboxer.tasks.example)", tasksFile)
	}
	if len(tasks) == 0 {
		return Result{}, errors.New("no tasks in the tasks file")
	}

	if !o.Keep {
		_ = os.RemoveAll(filepath.Join(root, config.StateDirName))
	}
	base, err := sandbox.ResolveBase(root)
	if err != nil {
		return Result{}, err
	}
	if o.Overrides.Domains != "" {
		if err := base.SetDomains(o.Overrides.Domains); err != nil {
			return Result{}, err
		}
	}
	rt, err := config.ResolveRuntime(profile, o.Defaults, base.Domains, base.Model, o.Overrides)
	if err != nil {
		return Result{}, err
	}

	if err := config.ValidateBackend(rt); err != nil {
		return Result{}, err
	}
	agent, err := registry.Get(rt.Agent)
	if err != nil {
		return Result{}, err
	}

	// A dry run never launches a container, so it does not need an engine
	// installed — resolve one only for a real run. Resolved BEFORE the image:
	// pinning a "latest" input rev below may need the engine for a one-time
	// remote resolve.
	engine := ""
	backendShown := rt.Backend
	if !o.DryRun {
		engine, err = backend.ResolveEngine(rt.Backend, o.Defaults)
		if err != nil {
			return Result{}, err
		}
		backendShown = engine
	}

	// Per-profile image customization (`tools:` packs / `image:` section):
	// resolve the content-addressed variant spec once for the whole batch,
	// pinning any "latest" input rev to a concrete commit (a dry run has no
	// engine — a cold pins cache then fails with build-image guidance); an
	// empty spec keeps the configured image, any customization selects the
	// variant tag (built on demand by the backend).
	spec, err := toolbox.ResolveSpec(profile)
	if err != nil {
		return Result{}, err
	}
	spec, err = toolbox.PinSpec(spec, engine, false, o.Stderr)
	if err != nil {
		return Result{}, err
	}
	image := o.Image
	if !spec.Empty() {
		image = spec.Tag()
	}

	// MCP servers: fold their domains into the batch allowlist once; the
	// per-sandbox config is seeded at launch.
	var mcpServers map[string]registry.MCPServer
	if profile != nil && len(profile.MCP) > 0 {
		servers, domains, mErr := registry.ResolveMCP(profile.MCP)
		if mErr != nil {
			return Result{}, mErr
		}
		mcpServers = servers
		rt.Domains = append(rt.Domains, domains...)
	}

	var profileJSON []byte
	if profile != nil {
		profileJSON, _ = profile.JSON()
	}

	fmt.Fprintf(o.Stdout, "sandboxer: src=%s agent=%s backend=%s model=%s parallel=%d%s%s\n",
		base.Src, rt.Agent, backendShown, orDefault(rt.Model, "default"), o.MaxP,
		dryTag(o.DryRun), limitsTag(o.Mem, o.CPU, o.Wall))

	sem := make(chan struct{}, max(1, o.MaxP))
	var wg sync.WaitGroup
	var mu sync.Mutex
	launched := 0
	makeFailed := 0  // sandboxes that never materialised
	agentFailed := 0 // launched agents that exited non-zero (written under mu)
	for _, t := range tasks {
		if profileJSON != nil {
			if err := base.WriteProfileJSON(t.Slug, profileJSON); err != nil {
				fmt.Fprintf(o.Stderr, "sandboxer: write profile %s: %v\n", t.Slug, err)
			}
		}
		if err := base.MakeSandbox(t.Slug, o.Stdout); err != nil {
			fmt.Fprintf(o.Stderr, "sandboxer: make sandbox %s: %v\n", t.Slug, err)
			makeFailed++
			continue
		}
		launched++
		wg.Add(1)
		sem <- struct{}{}
		go func(t Task) {
			defer wg.Done()
			defer func() { <-sem }()
			la := launchSpec{
				base:    base,
				slug:    t.Slug,
				prompt:  t.Body,
				rt:      rt,
				agent:   agent,
				engine:  engine,
				image:   image,
				spec:    spec,
				mcp:     mcpServers,
				profile: profile,
				mem:     o.Mem,
				cpu:     o.CPU,
				wall:    o.Wall,
				dry:     o.DryRun,
				noSetup: o.NoSetup,
				stderr:  o.Stderr,
			}
			if rc := la.run(); rc != 0 {
				mu.Lock()
				agentFailed++
				mu.Unlock()
			}
		}(t)
	}
	wg.Wait()
	failed := makeFailed + agentFailed
	fmt.Fprintf(o.Stdout, "sandboxer: all agents finished — %d ok, %d failed.\n", launched-agentFailed, failed)
	return Result{Root: base.Src, Count: launched, Failed: failed}, nil
}

var (
	memRe  = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[bBkKmMgGtT]?$`)
	cpuRe  = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?%?$`)
	wallRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[smhd]?$`)
)

// validateLimits rejects malformed --mem/--cpu/--wall up front instead of
// letting the container engine or systemd-run fail asynchronously inside a
// worker goroutine (where the error is easy to miss). Empty values mean "no
// limit" and always pass.
func validateLimits(mem, cpu, wall string) error {
	if mem != "" && !memRe.MatchString(mem) {
		return fmt.Errorf("invalid --mem %q (want a size like 512M or 2G)", mem)
	}
	if cpu != "" && !cpuRe.MatchString(cpu) {
		return fmt.Errorf("invalid --cpu %q (want a core count like 1.5 or a percentage like 100%%)", cpu)
	}
	if wall != "" && !wallRe.MatchString(wall) {
		return fmt.Errorf("invalid --wall %q (want seconds like 1800, optionally suffixed s/m/h/d)", wall)
	}
	return nil
}

type launchSpec struct {
	base    *sandbox.Base
	slug    string
	prompt  string
	rt      config.Runtime
	agent   registry.Agent
	engine  string
	image   string
	spec    toolbox.Spec
	mcp     map[string]registry.MCPServer
	profile *config.Profile
	mem     string
	cpu     string
	wall    string
	dry     bool
	noSetup bool
	stderr  io.Writer
}

// backendRun is the container-run seam, overridable in tests so the setup
// orchestration can be exercised without a real engine.
var backendRun = backend.Run

// runSetup runs the profile's one-time setup before the agent, gated by the
// per-sandbox stamp so re-running the batch does not repeat it. It returns an
// exit code: 0 = ran-and-stamped or nothing-to-do; non-zero = setup failed and
// the task should be counted failed. Setup shares the agent's isolation and
// egress allowlist (crt) and streams its output to the per-sandbox err log (ef).
func (s launchSpec) runSetup(dest, errPath string, crt config.Runtime, ef io.Writer) int {
	if s.noSetup || s.profile == nil {
		return 0
	}
	pending, hash := s.base.SetupPending(s.slug, s.profile.Setup)
	if !pending {
		return 0
	}
	fmt.Fprintf(s.stderr, "sandboxer: %s: running setup…\n", s.slug)
	sc, serr := backendRun(backend.RunOpts{
		Engine:          s.engine,
		Image:           s.image,
		Spec:            s.spec,
		Dest:            dest,
		Slug:            s.slug,
		HomeDir:         s.base.HomeDir(s.slug),
		RT:              crt,
		Profile:         s.profile,
		ProfileJSONPath: s.base.ProfileJSONPath(s.slug),
		ManifestPath:    s.base.ManifestPath(s.slug),
		Interactive:     false,
		Args:            []string{"bash", "-lc", s.profile.Setup},
		NoEgress:        os.Getenv("SANDBOXER_NO_EGRESS") == "1",
		Stdout:          ef,
		Stderr:          ef,
	})
	if serr != nil {
		fmt.Fprintf(s.stderr, "sandboxer: %s: setup: %v\n", s.slug, serr)
		return 1
	}
	if sc != 0 {
		fmt.Fprintf(s.stderr, "sandboxer: %s: setup exited %d (see %s)\n", s.slug, sc, errPath)
		return 1
	}
	_ = s.base.MarkSetupDone(s.slug, hash)
	return 0
}

// run executes one agent and returns its exit code (0 = success). The code is
// also persisted to the meta file; the caller aggregates it for the batch's
// overall exit status.
func (s launchSpec) run() int {
	meta := s.base.MetaFilePath(s.slug)
	_ = os.WriteFile(meta, []byte("exit=RUNNING\nsecs=0\n"), 0o644)
	full := preamble + "\n" + s.prompt
	acmd := s.agent.HeadlessCmd(s.rt.Model, s.rt.Domains, full)
	dest := s.base.SandboxDir(s.slug)
	outPath := s.base.LogPath(s.slug, "json")
	errPath := s.base.LogPath(s.slug, "err")

	start := time.Now()
	rc := 0
	switch {
	case s.dry:
		_ = os.WriteFile(outPath, []byte(`{"result":"(dry-run)"}`), 0o644)
	default:
		rc = s.runContainer(dest, acmd, outPath, errPath)
	}
	secs := int(time.Since(start).Seconds())
	_ = os.WriteFile(meta, []byte(fmt.Sprintf("exit=%d\nsecs=%d\n", rc, secs)), 0o644)
	return rc
}

func (s launchSpec) runContainer(dest, acmd, outPath, errPath string) int {
	of, err := os.Create(outPath)
	if err != nil {
		fmt.Fprintf(s.stderr, "sandboxer: %s: create log %s: %v\n", s.slug, outPath, err)
		return 1
	}
	defer of.Close()
	ef, err := os.Create(errPath)
	if err != nil {
		fmt.Fprintf(s.stderr, "sandboxer: %s: create log %s: %v\n", s.slug, errPath, err)
		return 1
	}
	defer ef.Close()
	if err := s.base.EnsureHome(s.slug); err != nil {
		fmt.Fprintf(s.stderr, "sandboxer: %s: prepare agent home: %v\n", s.slug, err)
		return 1
	}
	if _, err := registry.SeedMCP(s.rt.Agent, s.base.HomeDir(s.slug), s.mcp); err != nil {
		fmt.Fprintf(s.stderr, "sandboxer: %s: seed mcp: %v\n", s.slug, err)
		return 1
	}

	crt := s.rt
	crt.AuthAgents = []string{s.rt.Agent} // only the chosen agent's auth env

	// One-time setup before the agent runs; a failed setup fails the task so we
	// never launch an agent into a half-prepared tree.
	if rc := s.runSetup(dest, errPath, crt, ef); rc != 0 {
		return rc
	}

	rc, err := backend.Run(backend.RunOpts{
		Engine:          s.engine,
		Image:           s.image,
		Spec:            s.spec,
		Dest:            dest,
		Slug:            s.slug,
		HomeDir:         s.base.HomeDir(s.slug),
		RT:              crt,
		Profile:         s.profile,
		ProfileJSONPath: s.base.ProfileJSONPath(s.slug),
		ManifestPath:    s.base.ManifestPath(s.slug),
		Interactive:     false,
		NoEgress:        os.Getenv("SANDBOXER_NO_EGRESS") == "1",
		Mem:             s.mem,
		CPU:             s.cpu,
		Wall:            s.wall,
		Args:            []string{"bash", "-lc", acmd},
		Stdout:          of,
		Stderr:          ef,
	})
	if err != nil {
		// Setup failures (egress proxy, credential copy, …) previously vanished
		// here — surface them and count the task as failed.
		fmt.Fprintf(s.stderr, "sandboxer: %s: %v\n", s.slug, err)
		return 1
	}
	return rc
}

// parseTasksFile splits a tasks file into [slug] sections (the bash
// parse_tasks): lines starting with # are comments; a `[name]` header starts a
// new section whose body is the following lines until the next header.
func parseTasksFile(file string) ([]Task, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var tasks []Task
	var slug string
	var body strings.Builder
	emit := func() {
		if slug != "" {
			tasks = append(tasks, Task{Slug: slug, Body: body.String()})
		}
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") && len(trimmed) > 2 {
			emit()
			slug = config.Sanitize(trimmed[1 : len(trimmed)-1])
			body.Reset()
		} else if slug != "" {
			if body.Len() > 0 {
				body.WriteByte('\n')
			}
			body.WriteString(line)
		}
	}
	emit()
	return tasks, sc.Err()
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func dryTag(dry bool) string {
	if dry {
		return " DRY-RUN"
	}
	return ""
}

func limitsTag(mem, cpu, wall string) string {
	var sb strings.Builder
	if mem != "" {
		fmt.Fprintf(&sb, " mem=%s", mem)
	}
	if cpu != "" {
		fmt.Fprintf(&sb, " cpu=%s", cpu)
	}
	if wall != "" {
		fmt.Fprintf(&sb, " wall=%ss", wall)
	}
	return sb.String()
}
