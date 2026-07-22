package sandbox

import (
	"strings"
	"testing"
)

// TestFindSource: a source is addressed by its name (the repo-level leaf of
// its worktree) or, as a convenience, its repo basename; an unknown name
// fails and lists the real sources rather than guessing.
func TestFindSource(t *testing.T) {
	srcs := []Source{
		{RepoRoot: "/home/u/api", Path: "/sb/feat/devops/x/api", Branch: "devops/x", Managed: true},
		{RepoRoot: "/home/u/web", Path: "/sb/feat/devops/x/web", Branch: "devops/x", Managed: true},
	}
	if s, err := FindSource(srcs, "api"); err != nil || s.Path != "/sb/feat/devops/x/api" {
		t.Fatalf("FindSource api = %+v, %v", s, err)
	}
	if s, err := FindSource(srcs, "web"); err != nil || s.RepoRoot != "/home/u/web" {
		t.Fatalf("FindSource web = %+v, %v", s, err)
	}
	_, err := FindSource(srcs, "db")
	if err == nil || !strings.Contains(err.Error(), "api") || !strings.Contains(err.Error(), "web") {
		t.Fatalf("FindSource db err = %v, want it to list api, web", err)
	}
}

// TestFindSourceAdopted: an adopted source (mounted checkout, path unrelated
// to any branch layout) answers to its checkout's dir name.
func TestFindSourceAdopted(t *testing.T) {
	srcs := []Source{
		{RepoRoot: "/home/u/api", Path: "/home/u/api", Branch: "main"},
	}
	if s, err := FindSource(srcs, "api"); err != nil || s.Path != "/home/u/api" {
		t.Fatalf("FindSource adopted = %+v, %v", s, err)
	}
}

// TestFindSourceDeduped: two repos sharing a basename get distinct repo-level
// leaf names — each is reachable by its unique leaf name, while the bare
// basename is reported ambiguous rather than resolving to an arbitrary one.
func TestFindSourceDeduped(t *testing.T) {
	srcs := []Source{
		{RepoRoot: "/a/api", Path: "/sb/feat/devops/x/api", Branch: "devops/x", Managed: true},
		{RepoRoot: "/b/api", Path: "/sb/feat/devops/x/api-1f2e", Branch: "devops/x", Managed: true},
	}
	if s, err := FindSource(srcs, "api-1f2e"); err != nil || s.RepoRoot != "/b/api" {
		t.Fatalf("FindSource api-1f2e = %+v, %v", s, err)
	}
	if _, err := FindSource(srcs, "api"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("FindSource api (deduped) err = %v, want ambiguous", err)
	}
}
