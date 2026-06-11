package config

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestYAMLJSONTagParity pins the single assumption the schema generator makes:
// every config struct field carries the SAME camelCase name in its yaml and
// json tags, so a schema reflected from the json tags describes exactly what
// the strict yaml decoder accepts.
func TestYAMLJSONTagParity(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(Profile{}),
		reflect.TypeOf(Mount{}),
		reflect.TypeOf(Network{}),
		reflect.TypeOf(Proxy{}),
		reflect.TypeOf(ImageSpec{}),
	} {
		for i := range typ.NumField() {
			f := typ.Field(i)
			y := strings.Split(f.Tag.Get("yaml"), ",")[0]
			j := strings.Split(f.Tag.Get("json"), ",")[0]
			if y != j {
				t.Errorf("%s.%s: yaml tag %q != json tag %q", typ.Name(), f.Name, y, j)
			}
		}
	}
}

// TestSchemaDocumentParity pins the schemaDocument mirror to Document: same
// fields (minus unexported), each json tag matching Document's yaml tag.
func TestSchemaDocumentParity(t *testing.T) {
	doc := reflect.TypeOf(Document{})
	mirror := reflect.TypeOf(schemaDocument{})
	want := map[string]string{}
	for i := range doc.NumField() {
		f := doc.Field(i)
		if !f.IsExported() {
			continue
		}
		want[f.Name] = strings.Split(f.Tag.Get("yaml"), ",")[0]
	}
	got := map[string]string{}
	for i := range mirror.NumField() {
		f := mirror.Field(i)
		got[f.Name] = strings.Split(f.Tag.Get("json"), ",")[0]
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("schemaDocument drifted from Document:\nwant %v\ngot  %v", want, got)
	}
}

// TestSchemaArtifactCurrent: the committed schema/config.schema.json must
// match Schema()'s output — regenerate with `go generate ./internal/config`.
func TestSchemaArtifactCurrent(t *testing.T) {
	want, err := Schema()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../../schema/config.schema.json")
	if err != nil {
		t.Fatalf("schema artifact missing: %v (run `go generate ./internal/config`)", err)
	}
	if !bytes.Equal(want, got) {
		t.Error("schema artifact is stale — run `go generate ./internal/config`")
	}
}

// TestSchemaShape sanity-checks the rendered schema: both file shapes are
// offered, the strict-parser fields are present, and unknown keys are rejected
// (additionalProperties false — the editor-side mirror of KnownFields).
func TestSchemaShape(t *testing.T) {
	data, err := Schema()
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, frag := range []string{
		`"#/$defs/Profile"`,
		`"#/$defs/schemaDocument"`,
		`"context"`,
		`"allowedDomains"`,
		`"extraPkgs"`,
		`"additionalProperties": false`,
		SchemaURL,
	} {
		if !strings.Contains(s, frag) {
			t.Errorf("schema lacks %s", frag)
		}
	}
}
