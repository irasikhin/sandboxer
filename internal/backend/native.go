package backend

import (
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/registry"
)

// NativeEnter opens an interactive shell in the sandbox copy with the proxy
// environment set. OS-level isolation only kicks in when the agent itself runs
// (e.g. `claude --settings '{sandbox…}'`); the shell is a plain shell in the
// copy.
func NativeEnter(dest string, rt config.Runtime, stdin io.Reader, stdout, stderr io.Writer) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "bash"
	}
	cmd := exec.Command(shell)
	cmd.Dir = dest
	cmd.Env = append(os.Environ(), ProxyEnv(rt)...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// NativeExec runs a command in the sandbox copy. A `claude` invocation is
// wrapped with the native sandbox --settings (egress allowlist) and --model.
func NativeExec(dest string, rt config.Runtime, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 {
		return 2, fmt.Errorf("no command given")
	}
	var c *exec.Cmd
	if args[0] == "claude" {
		ca := []string{"--settings", registry.SettingsJSON(rt.Domains)}
		if rt.Model != "" {
			ca = append(ca, "--model", rt.Model)
		}
		ca = append(ca, args[1:]...)
		c = exec.Command("claude", ca...)
	} else {
		c = exec.Command(args[0], args[1:]...)
	}
	c.Dir = dest
	c.Env = append(os.Environ(), ProxyEnv(rt)...)
	c.Stdin = stdin
	c.Stdout = stdout
	c.Stderr = stderr
	return exitCode(c.Run()), nil
}

// ProxyEnv returns the HTTP(S)_PROXY/NO_PROXY environment entries (both upper
// and lower case) for the runtime, omitting empty values.
func ProxyEnv(rt config.Runtime) []string {
	var e []string
	add := func(k, v string) {
		if v != "" {
			e = append(e, k+"="+v)
		}
	}
	add("HTTP_PROXY", rt.HTTPProxy)
	add("http_proxy", rt.HTTPProxy)
	add("HTTPS_PROXY", rt.HTTPSProxy)
	add("https_proxy", rt.HTTPSProxy)
	add("NO_PROXY", rt.NoProxy)
	add("no_proxy", rt.NoProxy)
	return e
}
