package config

import (
	"encoding/json"

	"github.com/invopop/jsonschema"
)

//go:generate go run ../../tools/genschema -o ../../schema/config.schema.json

// SchemaURL is where the committed schema artifact is published; the scaffolded
// profile's yaml-language-server header points editors at it for completion and
// typo-checking before the strict parser ever runs.
const SchemaURL = "https://raw.githubusercontent.com/irasikhin/sandboxer/main/schema/config.schema.json"

// schemaDocument mirrors Document with json tags for schema reflection only:
// Document itself carries yaml tags (it is never serialized to JSON at
// runtime), and the reflector reads json tags. TestSchemaDocumentParity pins
// the mirror to Document field by field.
type schemaDocument struct {
	Defaults Profile            `json:"defaults,omitempty"`
	Profiles map[string]Profile `json:"profiles,omitempty"`
	Default  string             `json:"default,omitempty"`
}

// Schema renders the JSON Schema for the two file shapes the strict parser
// accepts — a flat Profile, or a profiles: document. It is generated from the
// SAME Go structs the decoder uses (yaml/json tags are identical camelCase,
// pinned by TestYAMLJSONTagParity), so the schema cannot drift from the
// parser; TestSchemaArtifactCurrent keeps the committed artifact in sync.
func Schema() ([]byte, error) {
	r := new(jsonschema.Reflector)
	doc := r.Reflect(&schemaDocument{})
	root := &jsonschema.Schema{
		Version:     jsonschema.Version,
		ID:          jsonschema.ID(SchemaURL),
		Title:       "sandboxer profile",
		Description: "A sandboxer .sandboxer/config.yaml: either a single flat profile or a document with defaults:/profiles:.",
		AnyOf: []*jsonschema.Schema{
			{Ref: "#/$defs/Profile"},
			{Ref: "#/$defs/schemaDocument"},
		},
		Definitions: doc.Definitions,
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
