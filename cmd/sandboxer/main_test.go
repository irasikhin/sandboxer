package main

import (
	"os"
	"testing"

	"github.com/irasikhin/sandboxer/internal/cli"
)

// withMain swaps os.Args and the exit seam, runs main(), and returns the
// captured exit code.
func withMain(t *testing.T, args ...string) int {
	t.Helper()
	oldArgs, oldExit := os.Args, exit
	t.Cleanup(func() { os.Args, exit = oldArgs, oldExit })

	code := -1
	exit = func(c int) { code = c }
	os.Args = append([]string{"sandboxer"}, args...)
	main()
	return code
}

func TestMainVersionWiring(t *testing.T) {
	code := withMain(t, "--version")
	if code != 0 {
		t.Errorf("--version exit = %d, want 0", code)
	}
	// main() must propagate the build version into the cli package.
	if cli.Version != version {
		t.Errorf("cli.Version = %q, want %q", cli.Version, version)
	}
}

func TestMainUnknownCommand(t *testing.T) {
	if code := withMain(t, "no-such-command"); code != 1 {
		t.Errorf("unknown command exit = %d, want 1", code)
	}
}
