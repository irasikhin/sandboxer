package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
)

func init() { register(newComposeCmd) }

// newComposeCmd prints the container run configuration for a sandbox — either a
// ready-to-run `docker run` command (--print-run) or a docker-compose.yml — so
// users can launch or inspect the sandbox manually with their own tooling.
func newComposeCmd() *cobra.Command {
	var f commonFlags
	var printRun bool
	cmd := &cobra.Command{
		Use:   "compose [slug]",
		Short: "Emit a docker-compose.yml (or a docker run command) for a sandbox",
		Long: `Render the container run configuration sandboxer would use for a sandbox.

By default it prints a docker-compose.yml; with --print-run it prints the
equivalent docker/podman run command instead. The egress allowlist is enforced
at run time by a separate proxy sidecar (started by 'sandboxer enter/exec') and
is therefore documented rather than reproduced here.

In persistent session mode (the default) 'sandboxer enter/exec' reuses a
managed session container instead of a one-shot run: --print-run then prints
the create + exec command pair, while the YAML stays the one-shot (ephemeral)
equivalent — its purpose is "run it with your own tooling".`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := resolveTarget(f, posArg(args))
			if err != nil {
				return err
			}
			rt, err := t.runtime(f)
			if err != nil {
				return err
			}
			if err := config.ValidateBackend(rt); err != nil {
				return err
			}
			if err := config.ValidateSession(rt); err != nil {
				return err
			}
			// Fold the profile's MCP-server domains into the allowlist exactly
			// as enter/exec do (minus the config seeding), so the printed argv
			// carries the same SANDBOXER_ALLOW_DOMAINS a real run gets.
			domains, err := mcpAllowDomains(t.profile, rt.Domains)
			if err != nil {
				return err
			}
			rt.Domains = domains
			engine, err := backend.ResolveEngine(rt.Backend, config.LoadDefaults())
			if err != nil {
				return err
			}
			// The same image resolution enter/exec use, so a tools/image
			// profile's variant tag shows up in the printed configuration
			// instead of the stock default.
			image, spec, err := resolveImage(t.profile)
			if err != nil {
				return err
			}
			opts := backend.RunOpts{
				Engine:          engine,
				Image:           image,
				Spec:            spec,
				Dest:            t.base.SandboxDir(t.slug),
				Slug:            t.slug,
				BaseDir:         t.base.Dir,
				HomeDir:         t.base.HomeDir(t.slug),
				RT:              rt,
				Profile:         t.profile,
				ProfileJSONPath: t.base.ProfileJSONPath(t.slug),
				ManifestPath:    t.base.ManifestPath(t.slug),
				Interactive:     true,
				Args:            []string{"bash", "-l"},
				NoEgress:        noEgress(),
			}
			argv, err := backend.RunArgv(opts)
			if err != nil {
				return err
			}
			// In persistent mode the managed session container is what
			// enter/exec actually use; the one-shot YAML below stays the
			// ephemeral equivalent but carries a note naming it.
			session := ""
			if rt.Session == config.SessionPersistent {
				session = backend.SessionName(t.slug, t.base.Dir)
			}
			out := cmd.OutOrStdout()
			if printRun {
				if session != "" {
					// The create + exec pair, from the same builders the real
					// session lifecycle uses (no drift, same as RunArgv). The
					// hash label is computed over exactly the argv printed:
					// the dynamic egress flags are documented rather than
					// reproduced (see the long help), so a session hand-made
					// from this line is deliberately stale to a real `enter`
					// when egress is on — enter then rebuilds it with the
					// proxy wired in, never adopting a container without an
					// outbound path.
					create := backend.CreateArgv(opts, session, backend.ConfigHash(opts, "", ""))
					fmt.Fprintln(out, shellLine(append([]string{engine}, create...)))
					fmt.Fprintln(out, shellLine(append([]string{engine}, backend.ExecArgv(opts, session, opts.Args)...)))
					return nil
				}
				fmt.Fprintln(out, shellLine(append([]string{engine}, argv...)))
				return nil
			}
			doc, err := composeYAML(t.slug, rt, argv, session)
			if err != nil {
				return err
			}
			fmt.Fprint(out, doc)
			return nil
		},
	}
	bindExisting(cmd, &f)
	cmd.Flags().BoolVar(&printRun, "print-run", false, "print a docker/podman run command instead of compose YAML")
	cmd.Flags().BoolVar(&f.ephemeral, "ephemeral", false, "render the one-shot configuration even when the session mode is persistent")
	return cmd
}

// composeService mirrors the subset of the compose spec we emit. Field order is
// the YAML output order; both docker compose and podman-compose understand it.
type composeService struct {
	Image       string            `yaml:"image"`
	WorkingDir  string            `yaml:"working_dir,omitempty"`
	User        string            `yaml:"user,omitempty"`
	CapDrop     []string          `yaml:"cap_drop,omitempty"`
	SecurityOpt []string          `yaml:"security_opt,omitempty"`
	MemLimit    string            `yaml:"mem_limit,omitempty"`
	CPUs        string            `yaml:"cpus,omitempty"`
	StdinOpen   bool              `yaml:"stdin_open,omitempty"`
	Tty         bool              `yaml:"tty,omitempty"`
	Environment map[string]string `yaml:"environment,omitempty"`
	Volumes     []string          `yaml:"volumes,omitempty"`
	Command     []string          `yaml:"command,omitempty"`
}

// composeYAML renders a single-service docker-compose document from the engine
// run argv produced by backend.RunArgv. Parsing our own well-formed argv keeps
// the run command and the compose file from drifting apart. session names the
// managed persistent session container when that mode is in effect ("" in
// ephemeral mode): the YAML is always the one-shot equivalent, so the header
// notes the container enter/exec would actually reuse.
func composeYAML(slug string, rt config.Runtime, argv []string, session string) (string, error) {
	svc := parseRunArgv(argv)
	doc := map[string]any{"services": map[string]any{slug: svc}}
	body, err := yaml.Marshal(doc)
	if err != nil {
		return "", err
	}
	egress := "(egress disabled)"
	if rt.Egress && len(rt.Domains) > 0 {
		egress = strings.Join(rt.Domains, ", ")
	}
	header := "# Generated by `sandboxer compose` for sandbox " + slug + ".\n" +
		"# NOTE: the egress allowlist is applied at run time by a separate proxy\n" +
		"# sidecar (started by `sandboxer enter/exec`); it is NOT reproduced here.\n" +
		"# Allowed domains: " + egress + "\n"
	if session != "" {
		header += "# NOTE: `sandboxer enter/exec` reuse the managed session container\n" +
			"# " + session + " — this file is the one-shot (ephemeral) equivalent.\n"
	}
	return header + string(body), nil
}

// parseRunArgv maps the engine `run …` argv onto compose service fields. It only
// has to understand the flags backend.runArgs emits.
func parseRunArgv(argv []string) composeService {
	svc := composeService{Environment: map[string]string{}}
	i := 0
	// Skip "run" and global run flags until the image.
	for i < len(argv) {
		a := argv[i]
		switch a {
		case "run", "--rm":
			i++
		case "-i":
			svc.StdinOpen = true
			i++
		case "-t":
			svc.Tty = true
			i++
		case "--cap-drop=ALL":
			svc.CapDrop = append(svc.CapDrop, "ALL")
			i++
		case "--userns=keep-id":
			i++ // podman host-id mapping; no portable compose equivalent
		case "--user", "--workdir", "--security-opt",
			"--memory", "--cpus", "--network", "--volume", "--env":
			if i+1 >= len(argv) {
				i++
				continue
			}
			val := argv[i+1]
			switch a {
			case "--user":
				svc.User = val
			case "--workdir":
				svc.WorkingDir = val
			case "--security-opt":
				svc.SecurityOpt = append(svc.SecurityOpt, val)
			case "--memory":
				svc.MemLimit = val
			case "--cpus":
				svc.CPUs = val
			case "--volume":
				svc.Volumes = append(svc.Volumes, val)
			case "--env":
				if k, v, ok := strings.Cut(val, "="); ok {
					svc.Environment[k] = v
				}
			case "--network":
				// egress network is dynamic; skip.
			}
			i += 2
		default:
			// First non-flag token is the image; the rest is the command.
			svc.Image = a
			svc.Command = append([]string(nil), argv[i+1:]...)
			i = len(argv)
		}
	}
	if len(svc.Environment) == 0 {
		svc.Environment = nil
	}
	return svc
}

// shellLine joins argv into a copy-pasteable shell command, single-quoting any
// token that contains shell-significant characters.
func shellLine(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = shellQuoteArg(a)
	}
	return strings.Join(out, " ")
}

func shellQuoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\"'\\$`*?(){}[]<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
