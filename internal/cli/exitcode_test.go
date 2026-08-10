package cli

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/backend"
)

// sandboxer is a launcher: the status of the thing it launched has to reach the
// caller, or `sandboxer exec box -- make test` can never gate a script or a CI
// job. exitErr carries it out of enter/exec and Run returns it — these pin that
// end to end, at the Run() seam the process entry point actually returns.
//
// The contract lives in a seam the integration suite cannot see: it already
// proves backend.Run reports a child's 7 intact, and the loss (or gain) happens
// entirely above that, on the way out of the CLI. Verified against real engines
// too — docker, podman, smolvm and microsandbox all return 7 for `exit 7`.

// TestRunReturnsChildExitCode covers exec and enter over a representative set of
// statuses, including the one a shell reports for an interrupt (130).
func TestRunReturnsChildExitCode(t *testing.T) {
	for _, name := range []string{"exec", "enter"} {
		for _, want := range []int{1, 2, 7, 42, 130, 255} {
			t.Run(name+"/"+strconv.Itoa(want), func(t *testing.T) {
				project := newProject(t)
				fakeMsb(t)
				stubSessionSeams(t, backend.SessionInfo{}, "h")
				// Both seams: exec with no live session takes the one-shot run,
				// enter attaches through the session. Either way the status the
				// user's shell/command produced is what has to come back out.
				backendRun = func(o backend.RunOpts) (int, error) { return want, nil }
				backendExecSession = func(o backend.RunOpts, name string, args []string) (int, error) {
					return want, nil
				}

				if code, _, errs := run("create", "feat", "--src", project); code != 0 {
					t.Fatalf("create: %d %s", code, errs)
				}
				args := []string{name, "feat", "--src", project, "--backend", "microsandbox"}
				if name == "exec" {
					args = append(args, "--", "false")
				}
				if code, _, errs := run(args...); code != want {
					t.Errorf("%s exit = %d, want the child's %d\n%s", name, code, want, errs)
				}
			})
		}
	}
}

// TestRunZeroExitStaysZero is the other half: a child that succeeds must not be
// turned into a failure by the passthrough.
func TestRunZeroExitStaysZero(t *testing.T) {
	project := newProject(t)
	fakeMsb(t)
	stubSessionSeams(t, backend.SessionInfo{}, "h")
	backendRun = func(o backend.RunOpts) (int, error) { return 0, nil }

	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	if code, _, errs := run("exec", "feat", "--src", project, "--backend", "microsandbox", "--", "true"); code != 0 {
		t.Errorf("exit = %d, want 0\n%s", code, errs)
	}
}

// TestRunChildExitPrintsNothing keeps the child's status silent: the command it
// ran already said whatever it had to say, so sandboxer must not append a
// diagnostic of its own on top.
func TestRunChildExitPrintsNothing(t *testing.T) {
	project := newProject(t)
	fakeMsb(t)
	stubSessionSeams(t, backend.SessionInfo{}, "h")
	backendRun = func(o backend.RunOpts) (int, error) { return 7, nil }

	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	_, _, errOut := run("exec", "feat", "--src", project, "--backend", "microsandbox", "--", "false")
	if strings.Contains(errOut, "exited 7") {
		t.Errorf("sandboxer printed the child's status: %q", errOut)
	}
}

// TestRunOrdinaryErrorIsStillOne pins that the passthrough did not turn every
// failure into one: a sandboxer-level error is still exit 1.
func TestRunOrdinaryErrorIsStillOne(t *testing.T) {
	project := newProject(t)
	fakeMsb(t)
	if code, _, _ := run("exec", "no-such-sandbox", "--src", project, "--", "true"); code != 1 {
		t.Errorf("exit = %d, want 1 for a sandboxer-level error", code)
	}
}

// TestExitErrIsRecognizedWhenWrapped pins the lookup Run performs: the code has
// to stay reachable by errors.As even if the error is ever wrapped on its way
// up, which is what keeps a future `fmt.Errorf("...: %w", err)` from silently
// collapsing every status back to 1.
func TestExitErrIsRecognizedWhenWrapped(t *testing.T) {
	var xe exitErr
	if !errors.As(exitErr{7}, &xe) || xe.code != 7 {
		t.Fatalf("bare exitErr not recognized: %+v", xe)
	}
	if !errors.As(silentErr{exitErr{7}}, &xe) || xe.code != 7 {
		t.Errorf("exit code not reachable through a silentErr wrapper")
	}
}
