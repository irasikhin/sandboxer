package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// emit runs `hook direnv --src <root>` through the public Run seam and returns
// its (exit, stdout).
func emitHook(root string) (int, string) {
	var out, errb bytes.Buffer
	code := Run([]string{"hook", "direnv", "--src", root}, strings.NewReader(""), &out, &errb)
	return code, out.String()
}

func TestHookDirenvActiveSandbox(t *testing.T) {
	project := newProject(t)
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: code=%d err=%q", code, errs)
	}
	if code, _, errs := run("use", "feat", "--src", project); code != 0 {
		t.Fatalf("use: code=%d err=%q", code, errs)
	}

	code, out := emitHook(project)
	if code != 0 {
		t.Fatalf("hook direnv exit = %d, want 0", code)
	}
	// SLUG and SRC are the floor; assert both, quoted for eval.
	wantSlug := "export SANDBOXER_SLUG='feat'\n"
	if !strings.Contains(out, wantSlug) {
		t.Errorf("missing slug export %q in:\n%s", wantSlug, out)
	}
	absProject, _ := filepath.Abs(project)
	wantSrc := "export SANDBOXER_SRC='" + absProject + "'\n"
	if !strings.Contains(out, wantSrc) {
		t.Errorf("missing src export %q in:\n%s", wantSrc, out)
	}
	// No-active-sandbox comment must not appear when one is active.
	if strings.Contains(out, "no active sandbox") {
		t.Errorf("unexpected no-active comment in:\n%s", out)
	}
}

func TestHookDirenvNoActiveSandbox(t *testing.T) {
	project := newProject(t)
	// A created-but-not-used sandbox leaves no `current` pointer.
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: code=%d err=%q", code, errs)
	}

	code, out := emitHook(project)
	if code != 0 {
		t.Fatalf("hook direnv exit = %d, want 0", code)
	}
	if strings.Contains(out, "export ") {
		t.Errorf("no exports expected without an active sandbox, got:\n%s", out)
	}
	if !strings.Contains(out, "no active sandbox") {
		t.Errorf("expected the no-active-sandbox comment, got:\n%s", out)
	}
}

func TestHookDirenvOutsideProject(t *testing.T) {
	// A bare directory with no .sandboxer state is not a sandboxer project.
	dir := t.TempDir()
	code, out := emitHook(dir)
	if code != 0 {
		t.Fatalf("hook direnv outside project exit = %d, want 0", code)
	}
	if out != "" {
		t.Errorf("expected no output outside a sandboxer project, got:\n%s", out)
	}
}

func TestHookDirenvEscapesSpecialPaths(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Setenv("SANDBOXER_PROFILES", t.TempDir())
	// A project root containing a space and a single quote must survive eval.
	parent := t.TempDir()
	project := filepath.Join(parent, "my proj's dir")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: code=%d err=%q", code, errs)
	}
	if code, _, errs := run("use", "feat", "--src", project); code != 0 {
		t.Fatalf("use: code=%d err=%q", code, errs)
	}

	code, out := emitHook(project)
	if code != 0 {
		t.Fatalf("hook direnv exit = %d, want 0", code)
	}
	absProject, _ := filepath.Abs(project)
	// The single quote is escaped as '\'' and the whole value single-quoted.
	wantSrc := "export SANDBOXER_SRC='" + strings.ReplaceAll(absProject, "'", `'\''`) + "'\n"
	if !strings.Contains(out, wantSrc) {
		t.Errorf("escaped src export missing/wrong.\nwant: %q\nin:\n%s", wantSrc, out)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"plain":            "'plain'",
		"with space":       "'with space'",
		"a'b":              `'a'\''b'`,
		"$VAR `cmd` \"x\"": "'$VAR `cmd` \"x\"'",
		"":                 "''",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
