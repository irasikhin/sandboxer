// Package runner executes a batch of autonomous headless agents, one per task,
// in parallel with optional resource limits (the bash `sandboxer run`).
package runner

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/registry"
	"github.com/irasikhin/sandboxer/internal/sandbox"
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
	Nice       int
	Mem        string
	CPU        string
	Wall       string
	Keep       bool
	DryRun     bool
	Stdout     io.Writer
	Stderr     io.Writer
}

// Result reports where the batch ran so the caller can print the summary list.
type Result struct {
	Root  string
	Count int
}

// Run resolves the project, expands the tasks file, creates a sandbox per task
// and launches the agents in parallel.
func Run(o Options) (Result, error) {
	var profile *config.Profile
	root := o.Src
	if o.ConfigPath != "" {
		p, err := config.Load(o.ConfigPath)
		if err != nil {
			return Result{}, fmt.Errorf("load profile %s: %w", o.ConfigPath, err)
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
	rt := config.ResolveRuntime(profile, o.Defaults, base.Domains, base.Model, o.Overrides)

	if err := config.ValidateNative(rt); err != nil {
		return Result{}, err
	}
	agent, err := registry.Get(rt.Agent)
	if err != nil {
		return Result{}, err
	}

	var profileJSON []byte
	if profile != nil {
		profileJSON, _ = profile.JSON()
	}

	engine := ""
	if rt.Backend != "native" {
		engine, err = backend.DetectEngine(o.Defaults)
		if err != nil {
			return Result{}, err
		}
	}

	fmt.Fprintf(o.Stdout, "sandboxer: src=%s agent=%s backend=%s model=%s parallel=%d%s%s\n",
		base.Src, rt.Agent, rt.Backend, orDefault(rt.Model, "default"), o.MaxP,
		dryTag(o.DryRun), limitsTag(o.Mem, o.CPU, o.Wall))

	sem := make(chan struct{}, max(1, o.MaxP))
	var wg sync.WaitGroup
	for _, t := range tasks {
		if profileJSON != nil {
			_ = base.WriteProfileJSON(t.Slug, profileJSON)
		}
		if err := base.MakeSandbox(t.Slug, o.Stdout); err != nil {
			fmt.Fprintf(o.Stderr, "sandboxer: make sandbox %s: %v\n", t.Slug, err)
			continue
		}
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
				image:   o.Image,
				profile: profile,
				nice:    o.Nice,
				mem:     o.Mem,
				cpu:     o.CPU,
				wall:    o.Wall,
				dry:     o.DryRun,
				stderr:  o.Stderr,
			}
			la.run()
		}(t)
	}
	wg.Wait()
	fmt.Fprintln(o.Stdout, "sandboxer: all agents finished.")
	return Result{Root: base.Src, Count: len(tasks)}, nil
}

type launchSpec struct {
	base    *sandbox.Base
	slug    string
	prompt  string
	rt      config.Runtime
	agent   registry.Agent
	engine  string
	image   string
	profile *config.Profile
	nice    int
	mem     string
	cpu     string
	wall    string
	dry     bool
	stderr  io.Writer
}

func (s launchSpec) run() {
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
	case s.rt.Backend == "native":
		rc = s.runNative(dest, acmd, outPath, errPath)
	default:
		rc = s.runContainer(dest, acmd, outPath, errPath)
	}
	secs := int(time.Since(start).Seconds())
	_ = os.WriteFile(meta, []byte(fmt.Sprintf("exit=%d\nsecs=%d\n", rc, secs)), 0o644)
}

func (s launchSpec) runNative(dest, acmd, outPath, errPath string) int {
	of, err := os.Create(outPath)
	if err != nil {
		return 1
	}
	defer of.Close()
	ef, err := os.Create(errPath)
	if err != nil {
		return 1
	}
	defer ef.Close()

	script := "cd " + posixQuote(dest) + " || exit 97\n" + acmd
	argv := s.wrapLimits([]string{"bash", "-c", script})
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dest
	cmd.Env = append(os.Environ(), backend.ProxyEnv(s.rt)...)
	cmd.Stdout = of
	cmd.Stderr = ef
	return exitCode(cmd.Run())
}

func (s launchSpec) runContainer(dest, acmd, outPath, errPath string) int {
	of, err := os.Create(outPath)
	if err != nil {
		return 1
	}
	defer of.Close()
	ef, err := os.Create(errPath)
	if err != nil {
		return 1
	}
	defer ef.Close()
	ephDir, _ := os.MkdirTemp("", "sbx-eph-")
	defer func() { _ = os.RemoveAll(ephDir) }()

	crt := s.rt
	crt.AuthAgents = []string{s.rt.Agent} // only the chosen agent's creds
	rc, _ := backend.Run(backend.RunOpts{
		Engine:          s.engine,
		Image:           s.image,
		Dest:            dest,
		Slug:            s.slug,
		RT:              crt,
		Profile:         s.profile,
		ProfileJSONPath: s.base.ProfileJSONPath(s.slug),
		ManifestPath:    s.base.ManifestPath(s.slug),
		Interactive:     false,
		Ephemeral:       true,
		EphDir:          ephDir,
		NoEgress:        os.Getenv("SANDBOXER_NO_EGRESS") == "1",
		Mem:             s.mem,
		CPU:             s.cpu,
		Wall:            s.wall,
		Args:            []string{"bash", "-lc", acmd},
		Stdout:          of,
		Stderr:          ef,
	})
	return rc
}

// wrapLimits prepends resource-limit wrappers to argv: systemd-run --user
// --scope for memory/CPU (when a user manager is available), else nice + an
// optional wall-clock timeout.
func (s launchSpec) wrapLimits(argv []string) []string {
	nice := []string{"nice", "-n", strconv.Itoa(s.nice)}
	if (s.mem != "" || s.cpu != "") && hasExec("systemd-run") && os.Getenv("XDG_RUNTIME_DIR") != "" {
		unit := "sandboxer-" + config.Sanitize(s.slug) + "-" + strconv.Itoa(os.Getpid())
		sr := []string{"systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit", unit}
		if s.mem != "" {
			sr = append(sr, "-p", "MemoryMax="+s.mem)
		}
		if s.cpu != "" {
			sr = append(sr, "-p", "CPUQuota="+s.cpu)
		}
		sr = append(sr, "-p", "TasksMax=1024")
		return append(append(sr, nice...), argv...)
	}
	var pre []string
	if s.wall != "" && hasExec("timeout") {
		pre = append(pre, "timeout", "--signal=TERM", s.wall)
	}
	return append(append(pre, nice...), argv...)
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

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

func hasExec(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func posixQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
