package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// piModels reads back the sandbox's pi models.json for assertions.
func piModels(t *testing.T, b *Base, slug string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(b.HomeDir(slug), filepath.FromSlash(PiModelsPath)))
	if err != nil {
		t.Fatalf("read pi models: %v", err)
	}
	var models map[string]any
	if err := json.Unmarshal(data, &models); err != nil {
		t.Fatalf("pi models do not parse: %v (%s)", err, data)
	}
	return models
}

// deepseekModelIDs returns the ids of the deepseek provider's model list.
func deepseekModelIDs(t *testing.T, models map[string]any) []string {
	t.Helper()
	providers, ok := models["providers"].(map[string]any)
	if !ok {
		t.Fatalf("providers = %#v, want an object", models["providers"])
	}
	deepseek, ok := providers["deepseek"].(map[string]any)
	if !ok {
		t.Fatalf("providers.deepseek = %#v, want an object", providers["deepseek"])
	}
	list, ok := deepseek["models"].([]any)
	if !ok {
		t.Fatalf("providers.deepseek.models = %#v, want a list", deepseek["models"])
	}
	var out []string
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("model entry %#v has an unexpected shape", e)
		}
		id, ok := m["id"].(string)
		if !ok {
			t.Fatalf("model entry %#v has no string id", e)
		}
		out = append(out, id)
	}
	return out
}

// TestEnsurePiModelsWritesFreshModels: a sandbox that has never run pi has no
// models.json at all — the v4.1 entry must still land, or the baked pi would
// not know the model exists until someone edited models.json by hand.
func TestEnsurePiModelsWritesFreshModels(t *testing.T) {
	b := newPiBase(t)
	var progress strings.Builder
	b.EnsurePiModels("s", &progress)

	got := deepseekModelIDs(t, piModels(t, b, "s"))
	if len(got) != 1 || got[0] != "deepseek-v4.1-flash-expires-on-0910" {
		t.Errorf("deepseek models = %v, want [deepseek-v4.1-flash-expires-on-0910]", got)
	}
	if !strings.Contains(progress.String(), "deepseek-v4.1-flash-expires-on-0910") {
		t.Errorf("registration not narrated: %q", progress.String())
	}
	// The home may hold provider configuration — it is not world-readable.
	fi, err := os.Stat(filepath.Join(b.HomeDir("s"), filepath.FromSlash(PiModelsPath)))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("models mode = %v (err=%v), want 0600", fi.Mode(), err)
	}
}

// TestEnsurePiModelsMergesExisting: the models file is the user's (seeded
// from the host, or written by pi itself) — seeding must ADD to the deepseek
// list, never replace it, and leave other providers alone.
func TestEnsurePiModelsMergesExisting(t *testing.T) {
	b := newPiBase(t)
	path := filepath.Join(b.HomeDir("s"), filepath.FromSlash(PiModelsPath))
	writeFile(t, path, `{"providers":{"deepseek":{"models":[{"id":"deepseek-chat","name":"DeepSeek Chat"}]},"ollama":{"baseUrl":"http://host:11434"}}}`)

	b.EnsurePiModels("s", nil)

	models := piModels(t, b, "s")
	if got := deepseekModelIDs(t, models); len(got) != 2 || got[0] != "deepseek-chat" || got[1] != "deepseek-v4.1-flash-expires-on-0910" {
		t.Errorf("deepseek models = %v, want the existing entry kept and deepseek-v4.1-flash-expires-on-0910 appended", got)
	}
	providers := models["providers"].(map[string]any)
	ollama, ok := providers["ollama"].(map[string]any)
	if !ok || ollama["baseUrl"] != "http://host:11434" {
		t.Errorf("providers.ollama = %#v, want the unrelated provider kept", providers["ollama"])
	}
}

// TestEnsurePiModelsNeverDuplicates: the seed runs on every create/enter/exec
// — a model already present must be recognized by id, or every enter would
// append a copy. An existing entry wins: the user's definition is never
// overwritten, and a no-op run never rewrites the file.
func TestEnsurePiModelsNeverDuplicates(t *testing.T) {
	existing := `{"providers":{"deepseek":{"models":[{"id":"deepseek-v4.1-flash-expires-on-0910","name":"my v4.1"}]}}}`
	b := newPiBase(t)
	path := filepath.Join(b.HomeDir("s"), filepath.FromSlash(PiModelsPath))
	writeFile(t, path, existing)

	var progress strings.Builder
	b.EnsurePiModels("s", &progress)
	b.EnsurePiModels("s", &progress)

	if got := deepseekModelIDs(t, piModels(t, b, "s")); len(got) != 1 {
		t.Errorf("deepseek models = %v, want the single existing entry", got)
	}
	// Nothing changed, so nothing is announced either.
	if progress.Len() != 0 {
		t.Errorf("no-op seed narrated: %q", progress.String())
	}
	// An untouched file keeps its bytes — the merge must not rewrite (and
	// reformat) models it had nothing to add to.
	if data, err := os.ReadFile(path); err != nil || string(data) != existing {
		t.Errorf("models rewritten: %q (err=%v)", data, err)
	}
}

// TestEnsurePiModelsLeavesUnparsableModels: a models file that does not parse
// is a hand edit — it belongs to the user, so the seed warns and leaves the
// bytes alone. Same for a `providers` value, provider entry or `models` list
// of an unexpected shape.
func TestEnsurePiModelsLeavesUnparsableModels(t *testing.T) {
	for name, existing := range map[string]string{
		"malformed json":        `{"providers": {`,
		"providers string":      `{"providers":"deepseek"}`,
		"provider string":       `{"providers":{"deepseek":"x"}}`,
		"deepseek models wrong": `{"providers":{"deepseek":{"models":"deepseek-chat"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			b := newPiBase(t)
			path := filepath.Join(b.HomeDir("s"), filepath.FromSlash(PiModelsPath))
			writeFile(t, path, existing)

			var progress strings.Builder
			b.EnsurePiModels("s", &progress)

			if data, err := os.ReadFile(path); err != nil || string(data) != existing {
				t.Errorf("models = %q (err=%v), want them left untouched", data, err)
			}
			if !strings.Contains(progress.String(), "not seeded") {
				t.Errorf("skip not warned about: %q", progress.String())
			}
		})
	}
}

// TestEnsurePiModelsSurvivesUnwritableHome: an unwritable home must warn and
// move on — the model seed is a convenience, never a reason to fail the enter
// that was actually asked for.
func TestEnsurePiModelsSurvivesUnwritableHome(t *testing.T) {
	b := newPiBase(t)
	home := b.HomeDir("s")
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })

	var progress strings.Builder
	b.EnsurePiModels("s", &progress)

	if !strings.Contains(progress.String(), "not seeded") {
		t.Errorf("failure not warned about: %q", progress.String())
	}
}
