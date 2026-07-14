package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

func init() { register(newConfigCmd) }

// newConfigCmd groups the config-file verbs under `sandboxer config`: reading
// and editing sandboxer.yaml in place, plus the scaffold/edit/validate
// verbs that used to live under `profile`. The split: profile = the selection
// entity (use, list), config = the file.
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read and edit the profile config file",
		Long: `Work with the config file — the committed ` + config.ConfigPath() + `.

  sandboxer config get [key]          show a value (or the whole profile)
  sandboxer config set <key> <value>  write a value in place (comments preserved)
  sandboxer config unset <key>        remove a key
  sandboxer config init               scaffold a commented ` + config.ConfigPath() + `
  sandboxer config edit               edit it in $EDITOR
  sandboxer config validate           check it parses (unknown fields are errors)

get/set/unset target one profile section: -p <name>, else the active sandbox
(sandboxer use) when it names one, else the file's default:, else its sole
profile.`,
	}
	cmd.AddCommand(
		newConfigGetCmd(),
		newConfigSetCmd(),
		newConfigUnsetCmd(),
		newConfigInitCmd(),
		newConfigEditCmd(),
		newConfigValidateCmd(),
	)
	return cmd
}

// configFlags are the targeting flags shared by config get/set/unset.
type configFlags struct {
	src     string
	config  string
	profile string
}

func bindConfigTarget(cmd *cobra.Command, f *configFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.src, "src", "", "project root (default: cwd)")
	fl.StringVarP(&f.config, "config", "f", "", "config file (default: "+config.ConfigPath()+")")
	fl.StringVarP(&f.profile, "profile", "p", "", "profile section to target (default: the active sandbox, then the file's default:)")
}

// configTarget is a resolved (file, section) pair a config get/set/unset
// operates on.
type configTarget struct {
	path    string           // the config file
	section []string         // node-path prefix: nil (flat top level) or ["profiles", name]
	profile string           // selected profile name
	doc     *config.Document // parsed file
}

// label names the edited scope for messages.
func (t *configTarget) label() string {
	return "profile " + t.profile
}

// resolveConfigTarget picks the file and the profile section a config verb
// operates on. The file: -f wins, then the project config under --src/cwd.
// The section: -p wins, then the active sandbox (sandboxer use) when it names
// a section, then the file's default:, then its sole profile. A missing file
// always errors — scaffolding is `config init`'s job, not a side effect of
// one set.
func resolveConfigTarget(f configFlags) (*configTarget, error) {
	t := &configTarget{}
	if f.config != "" {
		t.path = f.config
	} else {
		t.path = config.ConfigPathIn(firstNonEmpty(f.src, getwd()))
	}
	if !fileExists(t.path) {
		return nil, fmt.Errorf("no config at %s (scaffold one: sandboxer config init)", t.path)
	}

	doc, err := config.LoadDocument(t.path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", t.path, err)
	}
	t.doc = doc

	if !doc.Multi() {
		// A flat file holds exactly one profile; LoadDocument wrapped it as a
		// one-entry document whose Default is the effective name.
		name := doc.Default
		if f.profile != "" && f.profile != name {
			return nil, fmt.Errorf("%s holds a single profile %q — there is no profile %q", t.path, name, f.profile)
		}
		t.profile = name
		return t, nil
	}

	name := f.profile
	if name == "" {
		if cur := activeSlug(firstNonEmpty(f.src, getwd())); cur != "" && doc.Has(cur) {
			name = cur
		}
	}
	if name == "" {
		name = doc.Default
	}
	if name == "" && len(doc.Profiles) == 1 {
		for k := range doc.Profiles {
			name = k
		}
	}
	if name == "" {
		return nil, fmt.Errorf("name a profile with -p (have: %s)", docNames(doc))
	}
	if !doc.Has(name) {
		return nil, fmt.Errorf("no profile %q in %s (have: %s)", name, t.path, docNames(doc))
	}
	t.profile = name
	t.section = []string{"profiles", name}
	return t, nil
}

// activeSlug returns the project's active sandbox slug (sandboxer use), or ""
// when the project has no state.
func activeSlug(root string) string {
	base, err := sandbox.OpenBase(root)
	if err != nil || base == nil {
		return ""
	}
	return base.Current()
}

// docNames renders a document's sorted profile names for error messages.
func docNames(d *config.Document) string {
	out := make([]string, 0, len(d.Profiles))
	for k := range d.Profiles {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func newConfigGetCmd() *cobra.Command {
	var f configFlags
	cmd := &cobra.Command{
		Use:   "get [key]",
		Short: "Show a config value (or the whole profile)",
		Long: `Print one key's value — or, with no key, the whole profile as YAML.
Values come from the selected profile section (YAML merge keys resolved) —
exactly what create/enter would use. A key that is not set exits 1.`,
		Example: `  sandboxer config get                  # the whole profile
  sandboxer config get network.proxy
  sandboxer config get limits.memory -p web`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := resolveConfigTarget(f)
			if err != nil {
				return err
			}
			p, err := t.doc.Select(t.profile)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "sandboxer: %s — %s\n", t.label(), t.path)
				b, err := yaml.Marshal(p)
				if err != nil {
					return err
				}
				_, err = cmd.OutOrStdout().Write(b)
				return err
			}
			key := args[0]
			if _, err := config.LookupKey(key); err != nil {
				return err
			}
			v, ok := config.ProfileValue(p, key)
			if !ok {
				return fmt.Errorf("%s: not set (%s)", key, t.label())
			}
			switch v.(type) {
			case string, bool, int:
				fmt.Fprintln(cmd.OutOrStdout(), v)
			default:
				b, err := yaml.Marshal(v)
				if err != nil {
					return err
				}
				_, err = cmd.OutOrStdout().Write(b)
				return err
			}
			return nil
		},
	}
	bindConfigTarget(cmd, &f)
	return cmd
}

func newConfigSetCmd() *cobra.Command {
	var f configFlags
	cmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a key in the profile config (preserves comments)",
		Long: `Set one key in the config file, editing it in place — comments and key
order are preserved (long lines may re-wrap once). The value is YAML for
structured keys ('[a, b]', '[{source: /x, target: /y}]', 'false') and taken
verbatim for string keys. The edited file is strictly re-validated in memory
first, so a bad key or type never lands on disk.

Keys are dotted paths into the profile — backend, network.proxy,
network.allowedDomains, limits.memory, image.extraPkgs, env.<NAME>, deps, …
Lists are set whole. Existing sandboxes pick the change up on their next
enter/exec (the stored snapshot refreshes); deps changes take effect on
'sandboxer recreate'.`,
		Example: `  sandboxer config set backend podman
  sandboxer config set network.proxy http://localhost:3128
  sandboxer config set network.allowedDomains '[api.anthropic.com, github.com]'
  sandboxer config set env.NODE_ENV production
  sandboxer config set limits.memory 4G -p web`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, raw := args[0], args[1]
			k, err := config.LookupKey(key)
			if err != nil {
				return err
			}
			t, err := resolveConfigTarget(f)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(t.path)
			if err != nil {
				return err
			}
			ed, err := config.ParseEditable(data)
			if err != nil {
				return fmt.Errorf("%s: %w", t.path, err)
			}
			val, err := config.ParseValue(raw, k)
			if err != nil {
				return err
			}
			nodePath := append(append([]string{}, t.section...), strings.Split(key, ".")...)
			if err := ed.Set(nodePath, val); err != nil {
				return err
			}
			out, err := ed.Bytes()
			if err != nil {
				return err
			}
			if err := validateEdited(out, t, k); err != nil {
				return err
			}
			if err := os.WriteFile(t.path, out, fileModeOf(t.path)); err != nil {
				return err
			}
			if len(t.section) == 2 {
				if a := ed.Anchor(t.section); a != "" {
					fmt.Fprintf(cmd.ErrOrStderr(),
						"sandboxer: note — profile %s is anchored (&%s); profiles inheriting it via *%s see this change too\n",
						t.profile, a, a)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "set %s (%s) in %s\n", key, t.label(), t.path)
			return nil
		},
	}
	bindConfigTarget(cmd, &f)
	return cmd
}

func newConfigUnsetCmd() *cobra.Command {
	var f configFlags
	cmd := &cobra.Command{
		Use:   "unset <key>",
		Short: "Remove a key from the profile config (preserves comments)",
		Long: `Remove one key from the config file, editing it in place. A key the
profile only inherits via a YAML merge key (<<:) is not in the section
itself — unset reports it as not set; override it with 'sandboxer config set'
instead.`,
		Example: `  sandboxer config unset network.proxy
  sandboxer config unset env.NODE_ENV -p web`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			if _, err := config.LookupKey(key); err != nil {
				return err
			}
			t, err := resolveConfigTarget(f)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(t.path)
			if err != nil {
				return err
			}
			ed, err := config.ParseEditable(data)
			if err != nil {
				return fmt.Errorf("%s: %w", t.path, err)
			}
			nodePath := append(append([]string{}, t.section...), strings.Split(key, ".")...)
			ok, err := ed.Unset(nodePath)
			if err != nil {
				return err
			}
			if !ok {
				msg := fmt.Sprintf("%s is not set in %s (%s)", key, t.label(), t.path)
				if p, perr := t.doc.Select(t.profile); perr == nil {
					if _, inherited := config.ProfileValue(p, key); inherited {
						msg += " — it is inherited via a YAML merge key (<<:); override it with 'sandboxer config set'"
					}
				}
				return errors.New(msg)
			}
			out, err := ed.Bytes()
			if err != nil {
				return err
			}
			if _, err := config.LoadDocumentBytes(out, t.path); err != nil {
				return fmt.Errorf("refusing to write %s: %w", t.path, err)
			}
			if err := os.WriteFile(t.path, out, fileModeOf(t.path)); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "unset %s (%s) in %s\n", key, t.label(), t.path)
			return nil
		},
	}
	bindConfigTarget(cmd, &f)
	return cmd
}

// validateEdited is set's pre-write gate: the edited bytes must strict-decode
// (an unknown key or a wrong type never lands on disk), and the two
// field-local validators run when their key was the one edited. Cross-field
// checks (routes vs allowlist, proxy vs egress) stay at create/enter time —
// running them here would block legitimate intermediate states.
func validateEdited(out []byte, t *configTarget, k config.Key) error {
	doc, err := config.LoadDocumentBytes(out, t.path)
	if err != nil {
		return fmt.Errorf("refusing to write %s: %w", t.path, err)
	}
	p, err := doc.Select(t.profile)
	if err != nil {
		return err
	}
	switch {
	case k.Path == "network.allowedDomains":
		return config.ValidateDomains(p.Network.AllowedDomains)
	case strings.HasPrefix(k.Path, "image."):
		return config.ValidateImageSpec(p.Image)
	}
	return nil
}

// fileModeOf keeps the existing file's permissions on rewrite (0644 for a new
// file).
func fileModeOf(path string) fs.FileMode {
	if fi, err := os.Stat(path); err == nil {
		return fi.Mode().Perm()
	}
	return 0o644
}

// newConfigEditCmd opens sandboxer.yaml in $EDITOR, scaffolding the
// fully-annotated starter config first when the file does not exist — so a
// user always edits a concrete, documented file rather than a blank one.
func newConfigEditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit",
		Short: "Edit " + config.ConfigPath() + " in $EDITOR (scaffolds it if missing)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := config.ConfigPath()
			if !fileExists(path) {
				name := config.Sanitize(filepath.Base(getwd()))
				if name == "" {
					name = "feat"
				}
				if err := os.WriteFile(path, []byte(starterProfile(name, config.LoadDefaults())), 0o644); err != nil {
					return err
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "sandboxer: scaffolded %s\n", path)
			}
			return openInEditor(cmd, path)
		},
	}
	return cmd
}

// newConfigValidateCmd parses a profile config through the strict decoder
// (config.LoadDocument with KnownFields(true)), so a typo'd field name is
// reported as an error here instead of being silently ignored at run time.
// This gives "is my config valid, and what's wrong?" a precise, dedicated
// answer.
func newConfigValidateCmd() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "validate [file]",
		Short: "Validate a profile config (strict: unknown fields are errors)",
		Long: `Parse a profile config strictly and report the first problem precisely.

Unknown field names are rejected (not silently ignored), so a typo like
'allowedDomain' surfaces here rather than quietly doing nothing at run time.
Defaults to ` + config.ConfigPath() + `; pass a file or -f to check another.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := firstNonEmpty(configPath, posArg(args), config.ConfigPath())
			if !fileExists(path) {
				return fmt.Errorf("no config at %s (scaffold one: sandboxer config init)", path)
			}
			if _, err := config.LoadDocument(path); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: ok\n", path)
			return nil
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "f", "", "config file to validate (default: "+config.ConfigPath()+")")
	return cmd
}
