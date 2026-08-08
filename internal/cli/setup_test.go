package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

// TestRunSetupEngineFreeBranches covers the decisions runSetup makes before it
// would ever launch a container: no profile, no setup script, and the
// --no-setup skip on a pending script. None of these reach backend.Run, so the
// test needs no engine.
func TestRunSetupEngineFreeBranches(t *testing.T) {
	base, err := sandbox.ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer

	// nil profile → nothing to do.
	if err := runSetup(&target{base: base, slug: "s", profile: nil}, config.Runtime{}, "", nestedRun{}, false, &buf); err != nil {
		t.Fatalf("nil profile: %v", err)
	}
	// empty setup → not pending.
	if err := runSetup(&target{base: base, slug: "s", profile: &config.Profile{}}, config.Runtime{}, "", nestedRun{}, false, &buf); err != nil {
		t.Fatalf("empty setup: %v", err)
	}
	// pending + --no-setup → skip without running.
	tp := &target{base: base, slug: "s", profile: &config.Profile{Setup: "npm ci"}}
	if err := runSetup(tp, config.Runtime{}, "", nestedRun{}, true, &buf); err != nil {
		t.Fatalf("no-setup skip: %v", err)
	}
	if !strings.Contains(buf.String(), "skipping setup") {
		t.Errorf("expected a skip notice, got %q", buf.String())
	}
	// The skip must NOT mark the stamp — setup is still pending for a later run.
	if p, _ := base.SetupPending("s", "npm ci"); !p {
		t.Error("--no-setup must not mark setup done")
	}
}

// TestRunSetupRunsStampsAndIsIdempotent stubs the container-run seam to cover
// the run → stamp → skip-on-rerun path without a real engine.
func TestRunSetupRunsStampsAndIsIdempotent(t *testing.T) {
	base, err := sandbox.ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func(old func(backend.RunOpts) (int, error)) { backendRun = old }(backendRun)

	var gotArgs []string
	backendRun = func(o backend.RunOpts) (int, error) { gotArgs = o.Args; return 0, nil }

	tp := &target{base: base, slug: "s", profile: &config.Profile{Setup: "make build"}}
	var buf bytes.Buffer
	if err := runSetup(tp, config.Runtime{}, "podman", nestedRun{}, false, &buf); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if len(gotArgs) != 3 || gotArgs[0] != "bash" || gotArgs[1] != "-lc" || gotArgs[2] != "make build" {
		t.Errorf("setup argv = %v", gotArgs)
	}
	if p, _ := base.SetupPending("s", "make build"); p {
		t.Error("setup must be stamped done after a clean run")
	}

	called := false
	backendRun = func(o backend.RunOpts) (int, error) { called = true; return 0, nil }
	if err := runSetup(tp, config.Runtime{}, "podman", nestedRun{}, false, &buf); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("stamped setup must not run again")
	}
}

// TestRunSetupLogsOutput: the script's output is tee'd into
// _logs/<slug>.setup.log so a failure that scrolled away stays debuggable,
// and the failure hint names the file.
func TestRunSetupLogsOutput(t *testing.T) {
	base, err := sandbox.ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func(old func(backend.RunOpts) (int, error)) { backendRun = old }(backendRun)
	backendRun = func(o backend.RunOpts) (int, error) {
		fmt.Fprintln(o.Stdout, "npm ERR! boom")
		return 3, nil
	}
	var buf bytes.Buffer

	tp := &target{base: base, slug: "s", profile: &config.Profile{Setup: "npm ci"}}
	serr := runSetup(tp, config.Runtime{}, "podman", nestedRun{}, false, &buf)
	if serr == nil {
		t.Fatal("non-zero setup exit must error")
	}
	logPath := base.LogPath("s", "setup.log")
	if !strings.Contains(serr.Error(), logPath) {
		t.Errorf("setup error should name the saved log, got %q", serr)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil || !strings.Contains(string(data), "npm ERR! boom") {
		t.Errorf("setup log = (%q, %v), want the script output captured", data, rerr)
	}
	if !strings.Contains(buf.String(), "npm ERR! boom") {
		t.Error("the terminal must still see the script output")
	}
}

// TestRunSetupFailures covers the non-zero-exit and failed-to-start branches;
// neither stamps the sandbox, so setup stays pending.
func TestRunSetupFailures(t *testing.T) {
	base, err := sandbox.ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func(old func(backend.RunOpts) (int, error)) { backendRun = old }(backendRun)
	var buf bytes.Buffer

	backendRun = func(o backend.RunOpts) (int, error) { return 2, nil }
	tp := &target{base: base, slug: "s", profile: &config.Profile{Setup: "false"}}
	if err := runSetup(tp, config.Runtime{}, "podman", nestedRun{}, false, &buf); err == nil {
		t.Error("non-zero setup exit must error")
	}
	if p, _ := base.SetupPending("s", "false"); !p {
		t.Error("failed setup must stay pending")
	}

	backendRun = func(o backend.RunOpts) (int, error) { return 0, errors.New("boom") }
	if err := runSetup(tp, config.Runtime{}, "podman", nestedRun{}, false, &buf); err == nil {
		t.Error("failed-to-start must error")
	}
}

// TestPrepareNestedIDs covers the generation gate: only a nestedContainers
// profile generates; a host without subordinate ranges comes back empty with
// the warning only where multi-uid WOULD have worked (podman engine); and a
// generation failure is a notice, never a fatal — the sandbox still enters,
// single-uid.
func TestPrepareNestedIDs(t *testing.T) {
	base, err := sandbox.ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func(old func(*sandbox.Base, string) (bool, error)) { sandboxWriteNestedIDs = old }(sandboxWriteNestedIDs)

	var buf bytes.Buffer
	called := false
	sandboxWriteNestedIDs = func(b *sandbox.Base, slug string) (bool, error) { called = true; return true, nil }

	// Profiles that did not opt in never generate.
	for _, p := range []*config.Profile{nil, {}} {
		if got := prepareNestedIDs(&target{base: base, slug: "s", profile: p}, "podman", &buf); got != (backend.NestedIDFiles{}) {
			t.Errorf("profile %+v = %+v, want zero", p, got)
		}
	}
	if called {
		t.Error("generation ran for a profile that did not opt in")
	}

	tp := &target{base: base, slug: "s", profile: &config.Profile{NestedContainers: true}}

	// Ranges found: the four _meta paths come back for RunOpts.
	if got, want := prepareNestedIDs(tp, "podman", &buf), backend.NestedIDFiles(base.NestedIDFiles("s")); got != want {
		t.Errorf("prepareNestedIDs = %+v, want %+v", got, want)
	}
	if buf.Len() != 0 {
		t.Errorf("successful generation must be silent, got %q", buf.String())
	}

	// No ranges on a podman engine: empty, and the actionable warning.
	sandboxWriteNestedIDs = func(b *sandbox.Base, slug string) (bool, error) { return false, nil }
	if got := prepareNestedIDs(tp, "podman", &buf); got != (backend.NestedIDFiles{}) {
		t.Errorf("no-ranges = %+v, want zero", got)
	}
	if !strings.Contains(buf.String(), "subordinate uid/gid ranges") {
		t.Errorf("expected the no-ranges warning, got %q", buf.String())
	}

	// Same on docker: empty, and the limit is named up front (a silent docker
	// engine let the user meet it as a postgres EINVAL inside the sandbox)
	// — but never the host-subuid advice, which fixes nothing there.
	buf.Reset()
	if got := prepareNestedIDs(tp, "docker", &buf); got != (backend.NestedIDFiles{}) {
		t.Errorf("docker no-ranges = %+v, want zero", got)
	}
	if !strings.Contains(buf.String(), "single-uid on a docker engine") {
		t.Errorf("docker engine must name the single-uid limit, got %q", buf.String())
	}
	if strings.Contains(buf.String(), "subordinate uid/gid ranges") {
		t.Errorf("docker engine must not advise host subuids, got %q", buf.String())
	}

	// Generation failure: a notice, an empty set, no error escapes.
	buf.Reset()
	sandboxWriteNestedIDs = func(b *sandbox.Base, slug string) (bool, error) { return false, errors.New("disk full") }
	if got := prepareNestedIDs(tp, "podman", &buf); got != (backend.NestedIDFiles{}) {
		t.Errorf("failed generation = %+v, want zero", got)
	}
	if !strings.Contains(buf.String(), "disk full") {
		t.Errorf("expected the failure notice, got %q", buf.String())
	}
}

// TestPrepareNestedSeccomp covers the profile-file gate: only a
// nestedContainers profile materializes it; the env escape hatch drops back to
// unconfined with a loud notice; and — unlike the never-fatal id files — a
// failed WRITE is a hard error, because entering anyway would silently run the
// sandbox with no syscall filter.
func TestPrepareNestedSeccomp(t *testing.T) {
	base, err := sandbox.ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func(old func(*sandbox.Base) (string, error)) { sandboxEnsureSeccomp = old }(sandboxEnsureSeccomp)

	var buf bytes.Buffer
	called := false
	sandboxEnsureSeccomp = func(b *sandbox.Base) (string, error) { called = true; return "ignored", nil }

	// Profiles that did not opt in never write.
	for _, p := range []*config.Profile{nil, {}} {
		got, err := prepareNestedSeccomp(&target{base: base, slug: "s", profile: p}, &buf)
		if got != "" || err != nil {
			t.Errorf("profile %+v = (%q, %v), want empty", p, got, err)
		}
	}
	if called {
		t.Error("profile write ran for a profile that did not opt in")
	}

	tp := &target{base: base, slug: "s", profile: &config.Profile{NestedContainers: true}}

	// Opted in: the content-addressed _meta path comes back, silently, and it
	// is byte-identical to what the read-only twin computes for show/compose.
	got, err := prepareNestedSeccomp(tp, &buf)
	if err != nil {
		t.Fatal(err)
	}
	want, err := base.SeccompProfilePath()
	if err != nil || got != want {
		t.Errorf("prepareNestedSeccomp = (%q, %v), want %q", got, err, want)
	}
	if pure, _ := nestedSeccompPath(tp); pure != got {
		t.Errorf("read-only twin = %q, prepare = %q — show/compose would hash a different argv", pure, got)
	}
	if !called {
		t.Error("opted-in profile must write the file")
	}
	if buf.Len() != 0 {
		t.Errorf("success must be silent, got %q", buf.String())
	}

	// The escape hatch: no path (→ seccomp=unconfined) and a loud notice.
	t.Setenv(nestedSeccompEnv, "unconfined")
	got, err = prepareNestedSeccomp(tp, &buf)
	if got != "" || err != nil {
		t.Errorf("escape hatch = (%q, %v), want empty", got, err)
	}
	if !strings.Contains(buf.String(), "NO syscall filter") {
		t.Errorf("expected the unconfined notice, got %q", buf.String())
	}
	if pure, _ := nestedSeccompPath(tp); pure != "" {
		t.Errorf("read-only twin must honor the escape hatch too, got %q", pure)
	}

	// Any other value is ignored — the profile stays on.
	t.Setenv(nestedSeccompEnv, "please")
	if got, _ = prepareNestedSeccomp(tp, &buf); got != want {
		t.Errorf("unknown env value must not disable the profile, got %q", got)
	}
	t.Setenv(nestedSeccompEnv, "")

	// A failed write is fatal.
	sandboxEnsureSeccomp = func(b *sandbox.Base) (string, error) { return "", errors.New("disk full") }
	if _, err := prepareNestedSeccomp(tp, &buf); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("failed write = %v, want the wrapped disk error", err)
	}

	// prepareNested carries both halves and surfaces the fatal one.
	defer func(old func(*sandbox.Base, string) (bool, error)) { sandboxWriteNestedIDs = old }(sandboxWriteNestedIDs)
	sandboxWriteNestedIDs = func(b *sandbox.Base, slug string) (bool, error) { return true, nil }
	if _, err := prepareNested(tp, "podman", &buf); err == nil {
		t.Error("prepareNested must surface the seccomp write failure")
	}
	sandboxEnsureSeccomp = func(b *sandbox.Base) (string, error) { return "ignored", nil }
	n, err := prepareNested(tp, "podman", &buf)
	if err != nil {
		t.Fatal(err)
	}
	if n.Seccomp != want || n.IDs != backend.NestedIDFiles(base.NestedIDFiles("s")) {
		t.Errorf("prepareNested = %+v, want ids+seccomp populated", n)
	}
}
