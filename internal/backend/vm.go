package backend

import (
	"maps"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
)

// This file builds the argv for the microVM backend: sandboxer shells out to
// `smolvm` (libkrun — KVM on Linux, HVF on macOS) exactly as it shells out to
// docker/podman, so the whole backend stays a set of pure argv builders that a
// golden test pins without a hypervisor. The mapping mirrors commonArgs flag
// for flag, translated to smolvm's vocabulary; where the microVM makes a
// container flag meaningless (the user namespace, the capability drop, the
// seccomp profile — the VM boundary subsumes them all) the flag is simply
// absent, which is the point of moving to a real machine.
//
// smolvm gives no session labels, so unlike the container path nothing here
// stamps the ConfigHash/slug/base into the machine — that identity lives in a
// host-side sidecar (see vm_state.go). The machine's own hash is therefore
// private to this backend and free to evolve until the backend ships; the
// golden tests only pin that the builders are deterministic.

// smolvmEngine is the engine name the microvm backend resolves to (the smolvm
// CLI binary). SANDBOXER_SMOLVM overrides the looked-up path.
const smolvmEngine = "smolvm"

// vmDefaultMemMiB / vmDefaultCPUs size a machine when the profile sets no
// limit. A microVM must be given a size (unlike a container's unbounded
// default), and sandboxer's workload is SEVERAL parallel agents, so the
// defaults are deliberately smaller than smolvm's own 4 vCPU / 8 GiB — a
// profile raises them through the ordinary `limits` (Mem/CPU) knobs.
const (
	vmDefaultMemMiB = "4096"
	vmDefaultCPUs   = "2"
)

// vmCreateArgv assembles the smolvm argv that creates the named persistent
// machine for o: `machine create --name N -I IMAGE <common>`. No keepalive
// command — a smolvm machine boots to a bare agent and stays up on its own (no
// `sleep infinity` analogue) — and no credentials, which travel per exec (see
// vmExecArgv), exactly as the container session keeps them off the create argv.
func vmCreateArgv(o RunOpts, name string) []string {
	args := []string{"machine", "create", "--name", name, "-I", o.Image}
	return append(args, vmCommonArgs(o)...)
}

// vmStartArgv starts an already-created machine.
func vmStartArgv(name string) []string {
	return []string{"machine", "start", "--name", name}
}

// vmStopArgv stops a running machine, keeping its config and volumes so a later
// start resumes it.
func vmStopArgv(name string) []string {
	return []string{"machine", "stop", "--name", name}
}

// vmRemoveArgv deletes a machine non-interactively (-f skips smolvm's
// confirmation prompt, which a programmatic teardown can never answer).
func vmRemoveArgv(name string) []string {
	return []string{"machine", "delete", "--name", name, "-f"}
}

// vmListArgv lists machines as JSON for scripting (name/state/… — no labels,
// hence the sidecar state).
func vmListArgv() []string {
	return []string{"machine", "ls", "--json"}
}

// vmExecArgv runs cmdArgs inside the running machine name — the microVM
// counterpart of execArgv. -w pins the workdir to the sandbox root; -t only
// with a real TTY (same rule as the container path); TERM rides along so
// full-screen TUIs render; and the agents' auth env travels as `--secret-env
// KEY=KEY` references — smolvm reads the value from the host process env at
// launch and never persists it, so a rotated token reaches the sandbox on the
// next exec with no rebuild, and no credential ever lands in argv (ps) or in
// the machine record. The value is put on the child process env by the caller.
func vmExecArgv(o RunOpts, name string, cmdArgs []string) []string {
	args := []string{"machine", "exec", "--name", name, "-i"}
	if IsTerminal(o.Stdin) && IsTerminal(o.Stdout) {
		args = append(args, "-t")
	}
	args = append(args, "-w", o.Dest)
	if term := os.Getenv("TERM"); term != "" {
		args = append(args, "-e", "TERM="+term)
	}
	args = append(args, vmSecretEnvArgs(o)...)
	args = append(args, "--")
	return append(args, cmdArgs...)
}

// vmRunArgv assembles the smolvm argv for a one-shot ephemeral machine — the
// microVM counterpart of runArgs. The agent IS the machine's workload here (as
// in a container `run`), so its auth env is injected via --secret-env on the
// run itself, and -i/-t follow the same interactive+TTY rule.
func vmRunArgv(o RunOpts) []string {
	args := []string{"machine", "run", "-I", o.Image}
	if o.Interactive {
		args = append(args, "-i")
		if IsTerminal(o.Stdin) && IsTerminal(o.Stdout) {
			args = append(args, "-t")
		}
	}
	args = append(args, vmSecretEnvArgs(o)...)
	args = append(args, vmCommonArgs(o)...)
	args = append(args, "--")
	return append(args, o.Args...)
}

// vmCommonArgs assembles the machine flags shared by create and run: workdir,
// the identity-mapped source/home volumes (dirs only — smolvm shares
// directories, which is exactly sandbox.Mounts' model), the identity env (the
// same variables the container sets, SANDBOXER_IN_CONTAINER kept so the CLI's
// in-sandbox deny-all still fires), and the machine size. Credentials are NOT
// here — they travel per exec/run (vmSecretEnvArgs). Egress (--allow-host) is
// added by a later change; a machine with no network flag has no route at all,
// which is the fail-closed default.
func vmCommonArgs(o RunOpts) []string {
	args := []string{"-w", o.Dest}
	// The whole sandbox root as one share when nothing narrows it — a srcs edit
	// then shows up in a running machine live. A narrowed sandbox omits it, and
	// that absence is the containment boundary, exactly as with the container
	// --volume of Dest (see RunOpts.MountDest / sandbox.Mounts).
	if o.MountDest {
		args = append(args, "-v", o.Dest+":"+o.Dest)
	}
	args = append(args,
		"-e", "SANDBOXER_IN_CONTAINER=1",
		"-e", "SANDBOXER_SLUG="+o.Slug,
		"-e", "SANDBOXER_SANDBOX_DIR="+o.Dest,
		"-e", "LANG=C.UTF-8",
	)
	if o.DestGen != "" {
		args = append(args, "-e", "SANDBOXER_SANDBOX_GEN="+o.DestGen)
	}
	if o.MountGen != "" {
		args = append(args, "-e", "SANDBOXER_MOUNT_GEN="+o.MountGen)
	}
	if o.HomeDir != "" {
		args = append(args, "-e", "HOME="+o.HomeDir, "-v", o.HomeDir+":"+o.HomeDir)
	}
	for _, m := range o.SrcMounts {
		args = append(args, "-v", m+":"+m)
	}
	args = append(args, "--mem", vmMemMiB(o.Mem), "--cpus", vmCPUs(o.CPU))
	if csv := o.RT.DomainsCSV(); csv != "" {
		args = append(args, "-e", "SANDBOXER_ALLOW_DOMAINS="+csv)
	}
	args = append(args, vmExtraMountsAndEnv(o.Profile)...)
	return args
}

// vmSecretEnvArgs renders the agents' auth env (RunOpts.AuthEnv, "KEY=value")
// as smolvm --secret-env references, one per key: `--secret-env KEY=KEY` tells
// smolvm to read KEY from the host process env at launch. Only the key name
// enters argv; the value stays in the child process environment the caller
// sets, so it is never visible in ps nor persisted to the machine record.
func vmSecretEnvArgs(o RunOpts) []string {
	args := make([]string, 0, 2*len(o.AuthEnv))
	for _, kv := range o.AuthEnv {
		k, _, _ := strings.Cut(kv, "=")
		args = append(args, "--secret-env", k+"="+k)
	}
	return args
}

// vmExtraMountsAndEnv adds the profile's extraMounts and env injections in the
// smolvm dialect: -v src:target[:ro] (rw is the default, so it is left off) and
// -e k=v, keys sorted so map order never leaks into the argv.
func vmExtraMountsAndEnv(p *config.Profile) []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, m := range p.ExtraMounts {
		vol := m.Source + ":" + m.Target
		if m.Mode == "ro" {
			vol += ":ro"
		}
		out = append(out, "-v", vol)
	}
	for _, k := range slices.Sorted(maps.Keys(p.Env)) {
		out = append(out, "-e", k+"="+p.Env[k])
	}
	return out
}

// vmMemMiB converts a container-style memory cap ("2G", "512M", a raw byte
// count, "") into smolvm's integer MiB, applying the microvm default when
// unset. A value that does not parse falls back to the default rather than
// emitting a bad flag.
func vmMemMiB(s string) string {
	if s == "" {
		return vmDefaultMemMiB
	}
	mult := 1.0 / (1024 * 1024) // a bare number is bytes
	num := s
	switch s[len(s)-1] {
	case 'g', 'G':
		mult, num = 1024, s[:len(s)-1]
	case 'm', 'M':
		mult, num = 1, s[:len(s)-1]
	case 'k', 'K':
		mult, num = 1.0/1024, s[:len(s)-1]
	case 'b', 'B':
		mult, num = 1.0/(1024*1024), s[:len(s)-1]
	}
	n, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return vmDefaultMemMiB
	}
	mib := int(math.Ceil(n * mult))
	if mib < 1 {
		mib = 1
	}
	return strconv.Itoa(mib)
}

// vmCPUs converts a CPU quota (cpusFromQuota's "2"/"1.5"/"") into an integer
// vCPU count for smolvm's --cpus (which rejects a fraction), rounding up so a
// fractional quota never under-provisions, and applying the microvm default
// when unset.
func vmCPUs(quota string) string {
	f := cpusFromQuota(quota)
	if f == "" {
		return vmDefaultCPUs
	}
	n, err := strconv.ParseFloat(f, 64)
	if err != nil {
		return vmDefaultCPUs
	}
	c := int(math.Ceil(n))
	if c < 1 {
		c = 1
	}
	return strconv.Itoa(c)
}
