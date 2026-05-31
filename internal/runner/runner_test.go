package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseTasksFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sandboxer.tasks")
	content := `# a comment
[alpha]
do the first thing
across two lines

[beta/two]
single line

# trailing comment
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tasks, err := parseTasksFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("want 2 tasks, got %d: %+v", len(tasks), tasks)
	}
	if tasks[0].Slug != "alpha" {
		t.Errorf("slug[0] = %q", tasks[0].Slug)
	}
	if tasks[0].Body != "do the first thing\nacross two lines\n" {
		t.Errorf("body[0] = %q", tasks[0].Body)
	}
	// '/' in a section name is sanitized to '-'.
	if tasks[1].Slug != "beta-two" {
		t.Errorf("slug[1] = %q (want beta-two)", tasks[1].Slug)
	}
}

func TestParseTasksMissingFile(t *testing.T) {
	if _, err := parseTasksFile(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("expected error for missing tasks file")
	}
}
