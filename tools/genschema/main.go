// Command genschema writes the config JSON Schema artifact (see
// config.Schema). It is wired via go:generate in internal/config/schema.go;
// TestSchemaArtifactCurrent fails when the committed artifact is stale.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/irasikhin/sandboxer/internal/config"
)

func main() {
	out := flag.String("o", "schema/config.schema.json", "output path")
	flag.Parse()
	data, err := config.Schema()
	if err != nil {
		fmt.Fprintln(os.Stderr, "genschema:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "genschema:", err)
		os.Exit(1)
	}
}
