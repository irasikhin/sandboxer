package runner

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestRunNative drives a real (non-dry) native run with a fake `claude` agent
// on PATH, covering launchSpec.runNative end to end.
func TestRunNative(t *testing.T) {
	requireExec(t, "bash", "nice", "sh")

	bin := t.TempDir()
	writeScript(t, filepath.Join(bin, "claude"), `echo '{"result":"native-ok"}'`+"\n")
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	root := t.TempDir()
	writeTasks(t, root)

	var out, errb bytes.Buffer
	res, err := Run(Options{
		Src:      root,
		Defaults: config.Defaults{Agent: "claude", Backend: "native"},
		MaxP:     1,
		Stdout:   &out,
		Stderr:   &errb,
	})
	if err != nil {
		t.Fatalf("Run native: %v\n%s", err, errb.String())
	}
	if res.Count != 2 {
		t.Fatalf("count = %d, want 2", res.Count)
	}
	for _, slug := range []string{"alpha", "beta"} {
		log, _ := os.ReadFile(filepath.Join(root, config.StateDirName, "_logs", slug+".json"))
		if !strings.Contains(string(log), "native-ok") {
			t.Errorf("native result for %s = %q", slug, log)
		}
		meta, _ := os.ReadFile(filepath.Join(root, config.StateDirName, "_meta", slug+".meta"))
		if !strings.Contains(string(meta), "exit=0") {
			t.Errorf("meta for %s = %q", slug, meta)
		}
	}
}

// TestRunNativeAgentFailureAggregated: when agents exit non-zero, the batch
// reports them in Result.Failed (so the CLI can exit non-zero) while still
// recording each per-agent exit in its meta file.
func TestRunNativeAgentFailureAggregated(t *testing.T) {
	requireExec(t, "bash", "nice", "sh")

	bin := t.TempDir()
	writeScript(t, filepath.Join(bin, "claude"), "exit 3\n") // every agent fails
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	root := t.TempDir()
	writeTasks(t, root) // alpha, beta

	var out, errb bytes.Buffer
	res, err := Run(Options{
		Src:      root,
		Defaults: config.Defaults{Agent: "claude", Backend: "native"},
		MaxP:     2,
		Stdout:   &out,
		Stderr:   &errb,
	})
	if err != nil {
		t.Fatalf("Run native: %v\n%s", err, errb.String())
	}
	if res.Count != 2 {
		t.Fatalf("Count = %d, want 2", res.Count)
	}
	if res.Failed != 2 {
		t.Errorf("Failed = %d, want 2 (both agents exited non-zero)", res.Failed)
	}
	if !strings.Contains(out.String(), "0 ok, 2 failed") {
		t.Errorf("summary line missing failure tally: %q", out.String())
	}
	for _, slug := range []string{"alpha", "beta"} {
		meta, _ := os.ReadFile(filepath.Join(root, config.StateDirName, "_meta", slug+".meta"))
		if !strings.Contains(string(meta), "exit=3") {
			t.Errorf("meta for %s = %q (want exit=3)", slug, meta)
		}
	}
}

// TestRunContainer drives a non-dry container run with a fake `podman` engine,
// covering launchSpec.runContainer.
func TestRunContainer(t *testing.T) {
	requireExec(t, "sh")
	t.Setenv("HOME", t.TempDir()) // no real ~/.claude to copy
	t.Setenv("SANDBOXER_NO_EGRESS", "1")

	bin := t.TempDir()
	writeScript(t, filepath.Join(bin, "podman"), "exit 0\n")
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	root := t.TempDir()
	writeTasks(t, root)

	var out, errb bytes.Buffer
	res, err := Run(Options{
		Src:       root,
		Defaults:  config.Defaults{Agent: "claude", Backend: "podman"},
		Overrides: config.Overrides{Backend: "podman"},
		Image:     "toolbox:latest",
		MaxP:      2,
		Stdout:    &out,
		Stderr:    &errb,
	})
	if err != nil {
		t.Fatalf("Run container: %v\n%s", err, errb.String())
	}
	if res.Count != 2 {
		t.Fatalf("count = %d, want 2", res.Count)
	}
	for _, slug := range []string{"alpha", "beta"} {
		meta, _ := os.ReadFile(filepath.Join(root, config.StateDirName, "_meta", slug+".meta"))
		if !strings.Contains(string(meta), "exit=0") {
			t.Errorf("meta for %s = %q (want exit=0 from fake podman)", slug, meta)
		}
	}
}
