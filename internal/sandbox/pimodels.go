package sandbox

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/irasikhin/sandboxer/internal/style"
)

// PiModelsPath is pi's per-user model catalog, relative to the agent's home:
// ~/.pi/agent/models.json. pi upserts its `providers.<id>.models` entries over
// the built-in provider catalog at startup, so it is the one host-side file
// that can teach a BAKED pi release about a model published after that
// release's catalog was generated — the pi.dev overlay pi normally refreshes
// every few hours is unreachable under the sandbox's default egress allowlist.
const PiModelsPath = ".pi/agent/models.json"

// PiModels are the model definitions the toolbox image registers in pi's
// catalog. The DeepSeek V4.1 Flash entry is the day-one one: the baked pi
// release predates the model, and it runs on the deepseek provider the image
// already wires (DEEPSEEK_API_KEY auth, api.deepseek.com in the default
// allowlist). THE ID IS THE BETA ONE AND IT EXPIRES: api.deepseek.com serves
// deepseek-v4.1-flash-expires-on-0910 (verified 2026-09-09) but not the
// permanent deepseek-v4.1-flash yet (V4.1 Flash launches ~2026-09-10 Beijing
// time) — swap the id here when the permanent one goes live.
//
// The compat block restates the provider's own: pi does not inherit a
// built-in model's compat when a models.json entry defines a NEW id, and
// without it the deepseek thinking protocol would degrade. contextWindow,
// maxTokens and cost mirror deepseek-v4-flash — provisional until the pi.dev
// catalog (or a newer pi release) carries the model officially, at which
// point this entry is redundant but harmless (same id, same shape).
var PiModels = map[string]any{
	"providers": map[string]any{
		"deepseek": map[string]any{
			"models": []any{
				map[string]any{
					"id":        "deepseek-v4.1-flash-expires-on-0910",
					"name":      "DeepSeek V4.1 Flash (beta, expires 09-10)",
					"reasoning": true,
					"input":     []any{"text"},
					"cost": map[string]any{
						"input":      0.14,
						"output":     0.28,
						"cacheRead":  0.0028,
						"cacheWrite": 0.0,
					},
					"contextWindow": 1000000,
					"maxTokens":     384000,
					"thinkingLevelMap": map[string]any{
						"minimal": nil,
						"low":     "low",
						"medium":  nil,
						"high":    "high",
						"max":     "max",
					},
					"compat": map[string]any{
						"supportsStore":         false,
						"supportsDeveloperRole": false,
						"maxTokensField":        "max_tokens",
						"requiresReasoningContentOnAssistantMessages": true,
						"thinkingFormat": "deepseek",
					},
				},
			},
		},
	},
}

// EnsurePiModels registers the models the toolbox image knows about in slug's
// pi model catalog, so the sandbox's pi offers deepseek-v4.1-flash from the
// first run instead of after a manual models.json edit.
//
// It is a MERGE into whatever models.json the home already has (host-seeded
// by SeedHome, or written by pi itself): only the missing model ids are
// appended to the matching provider's `models` list, every other entry kept.
// An entry already present under the same id is never replaced — the user's
// definition wins. A models.json that does not parse is left ALONE with a
// warning, exactly like the settings merge.
//
// Like EnsurePiPackages this runs on create/enter/exec and is therefore
// self-healing rather than one-shot, and it is gated by the same profile
// opt-out (piPackages = false / SANDBOXER_NO_PI_PACKAGES=1), which disables
// all sandboxer-managed pi augmentation.
func (b *Base) EnsurePiModels(slug string, w io.Writer) {
	path := filepath.Join(b.HomeDir(slug), filepath.FromSlash(PiModelsPath))
	// readPiSettings is the shared reader: models.json has the same shape
	// contract (a JSON object) and the same leave-the-user's-bytes-alone
	// semantics, so one reader serves both files.
	models, err := readPiSettings(path)
	if err != nil {
		if w != nil {
			style.Errorf(w, "pi models ~/%s not seeded: %v", PiModelsPath, err)
		}
		return
	}
	added, err := addPiModels(models, PiModels)
	if err != nil {
		if w != nil {
			style.Errorf(w, "pi models ~/%s not seeded: %v", PiModelsPath, err)
		}
		return
	}
	if len(added) == 0 {
		return
	}
	if err := writePiSettings(path, models); err != nil {
		if w != nil {
			style.Errorf(w, "pi models ~/%s not seeded: %v", PiModelsPath, err)
		}
		return
	}
	if w != nil {
		for _, m := range added {
			style.Infof(w, "pi: %s added to the sandbox's pi model catalog", m)
		}
	}
}

// addPiModels appends the model definitions models is missing to the matching
// `providers.<id>.models` lists and returns the ids it added. want has the
// same shape as models.json itself (a `providers` attrset) — see PiModels.
// A `providers` value, provider entry or `models` list of an unexpected shape
// is an error rather than something to overwrite — the file is the user's.
func addPiModels(models map[string]any, want map[string]any) ([]string, error) {
	providers := map[string]any{}
	switch v := models["providers"].(type) {
	case nil:
	case map[string]any:
		providers = v
	default:
		return nil, fmt.Errorf("`providers` is %T, expected an object (left untouched)", v)
	}
	wantProviders, ok := want["providers"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("want has no `providers` object")
	}
	var added []string
	for pid, pw := range wantProviders {
		pwObj, ok := pw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("provider %q: want entry is %T, expected an object", pid, pw)
		}
		wantModels, ok := pwObj["models"].([]any)
		if !ok {
			return nil, fmt.Errorf("provider %q: want entry has no `models` list", pid)
		}
		provider := map[string]any{}
		switch v := providers[pid].(type) {
		case nil:
		case map[string]any:
			provider = v
		default:
			return nil, fmt.Errorf("providers.%s is %T, expected an object (left untouched)", pid, v)
		}
		var list []any
		switch v := provider["models"].(type) {
		case nil:
		case []any:
			list = v
		default:
			return nil, fmt.Errorf("providers.%s.models is %T, expected a list (left untouched)", pid, v)
		}
		var newModels []any
		for _, wantModel := range wantModels {
			wantID, err := piModelID(wantModel)
			if err != nil {
				return nil, fmt.Errorf("provider %q: %v", pid, err)
			}
			if piModelListed(list, wantID) {
				continue
			}
			list = append(list, wantModel)
			newModels = append(newModels, wantModel)
		}
		if len(newModels) == 0 {
			continue
		}
		provider["models"] = list
		providers[pid] = provider
		models["providers"] = providers
		for _, m := range newModels {
			id, _ := piModelID(m)
			added = append(added, id)
		}
	}
	return added, nil
}

// piModelID extracts a want model's id, the key the dedup and the narration
// key on. A want entry must be an object with a string id — anything else is
// a programming error in PiModels, not something the user's file did.
func piModelID(m any) (string, error) {
	obj, ok := m.(map[string]any)
	if !ok {
		return "", fmt.Errorf("model entry is %T, expected an object", m)
	}
	id, ok := obj["id"].(string)
	if !ok || id == "" {
		return "", fmt.Errorf("model entry has no string `id`")
	}
	return id, nil
}

// piModelListed reports whether a model with the given id is already in the
// provider's list, so an existing user definition is never replaced.
func piModelListed(list []any, id string) bool {
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			if v, ok := m["id"].(string); ok && v == id {
				return true
			}
		}
	}
	return false
}
