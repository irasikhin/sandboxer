package backend

import (
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
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
	args = append(args, vmNetworkArgs(o)...)
	if csv := o.RT.DomainsCSV(); csv != "" {
		args = append(args, "-e", "SANDBOXER_ALLOW_DOMAINS="+csv)
	}
	// The profile.json, shared read-only at /run/sandboxer — the same path the
	// container mounts the single file at (smolvm shares DIRECTORIES only, so it
	// is staged into a per-sandbox run dir first; see stageProfileJSON). Mounted
	// whenever a profile.json is configured; stageProfileJSON guarantees the dir
	// exists before boot.
	if d := vmRunDir(o); d != "" {
		args = append(args, "-v", d+":/run/sandboxer:ro")
	}
	args = append(args, vmExtraMountsAndEnv(o.Profile)...)
	return args
}

// vmRunDir is the per-sandbox directory shared read-only at /run/sandboxer, or
// "" when no profile.json is configured. smolvm cannot share the single
// profile.json file (it shares directories), and _meta holds every sandbox's
// metadata, so the file is staged into its own <slug>.run dir beside it.
func vmRunDir(o RunOpts) string {
	if o.ProfileJSONPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(o.ProfileJSONPath), o.Slug+".run")
}

// stageProfileJSON populates the per-sandbox run dir (vmRunDir) with the
// profile.json so it can be shared at /run/sandboxer. The dir is always created
// when a profile.json path is configured (so the -v source exists even if the
// file itself is absent); the file is copied when present. No-op otherwise.
func stageProfileJSON(o RunOpts) error {
	dir := vmRunDir(o)
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if !pathExists(o.ProfileJSONPath) {
		return nil
	}
	data, err := os.ReadFile(o.ProfileJSONPath)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "profile.json"), data, 0o600)
}

// vmNetworkArgs renders the machine's outbound policy. A smolvm machine has NO
// route by default, so the states are:
//
//   - egress.proxy set → proxy-delegated egress (the container's direct mode):
//     open the network and point the guest's HTTP(S) clients at the proxy via
//     HTTP_PROXY/HTTPS_PROXY/NO_PROXY env. There is no squid, so the proxy is the
//     egress control point; an active allowlist alongside a proxy is refused by
//     ValidateBackend, never silently dropped. localhost is NOT rewritten (unlike
//     the container host-gateway) — smolvm's TSI reaches a host-local proxy as-is.
//   - egress disabled (egress.enabled=false or SANDBOXER_NO_EGRESS), no proxy →
//     --net, an open network;
//   - egress on with an allowlist → --allow-host per domain (each implies --net),
//     the fail-closed default: only the listed hosts resolve;
//   - egress on with an EMPTY allowlist → no flag at all, a fully offline machine
//     (a VALID state here, unlike the container path's errEmptyAllowlist — with no
//     route by default, "allow nothing" is simply "reach nothing").
//
// The flags live in the create argv, so they fold into vmSessionWantHash: adding
// a domain, toggling egress, or changing the proxy recreates the machine (the
// analogue of the container ConfFingerprint).
func vmNetworkArgs(o RunOpts) []string {
	if p := o.RT.Proxy; p != "" {
		args := []string{"--net",
			"-e", "HTTP_PROXY=" + p, "-e", "http_proxy=" + p,
			"-e", "HTTPS_PROXY=" + p, "-e", "https_proxy=" + p}
		if o.RT.NoProxy != "" {
			args = append(args, "-e", "NO_PROXY="+o.RT.NoProxy, "-e", "no_proxy="+o.RT.NoProxy)
		}
		return args
	}
	if !egressRequired(o) {
		return []string{"--net"}
	}
	var args []string
	for _, d := range vmAllowHosts(o.RT.Domains) {
		args = append(args, "--allow-host", d)
	}
	return args
}

// vmAllowHosts normalizes the allowlist for smolvm's --allow-host, which resolves
// each entry as a hostname at VM start: a leading dot (the squid subdomain-
// wildcard grammar) is stripped so the name resolves, blanks and duplicates are
// dropped, and the result is sorted for a stable machine hash.
//
// Stripping the dot does NOT narrow the rule. Measured on smolvm 1.6.13,
// --allow-host matches the host AND its subdomains, the same suffix semantics
// as squid's leading-dot dstdomain and microsandbox's `*.domain`:
// `--allow-host github.com` admits api.github.com and codeload.github.com
// (different IPs, so this is name-bound, not an address or subnet match) and
// refuses example.com, while `--allow-host api.github.com` refuses the parent
// github.com. All three backends therefore agree on what a listed domain
// covers — worth re-checking on a smolvm upgrade, since only the runner
// enforces it.
func vmAllowHosts(domains []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range domains {
		d = strings.TrimPrefix(strings.TrimSpace(d), ".")
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// vmSharePreflight rejects a profile extraMount the microVM runners cannot
// expose. virtio-fs shares DIRECTORY trees only — a docker-style file bind
// mount (source is a regular file, e.g. a single dotfile or config) has no
// microVM share analogue, and silently sharing nothing would be worse than an
// upfront error. Only an EXISTING regular-file source is rejected; a missing
// source is left for the runner to answer (it materializes the path as an
// empty dir, like the container engines do).
func vmSharePreflight(o RunOpts) error {
	if o.Profile == nil {
		return nil
	}
	var bad []string
	for _, m := range o.Profile.ExtraMounts {
		if fi, err := os.Stat(m.Source); err == nil && !fi.IsDir() {
			bad = append(bad, m.Source)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("the microVM backends cannot expose %s: virtio-fs shares directories only, "+
		"and a regular file has no share analogue — mount a directory that holds it",
		strings.Join(bad, ", "))
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

// parseMemMiB is vmMemMiB's core: the MiB value of a container-style memory cap
// ("2G", "512M", a raw byte count), with ok=false for anything unparseable.
// vmMemMiB falls back to the default on !ok; vmLimitsPreflight REJECTS instead,
// so a bad value is a clear error on the microVM backends, never a silent 4 GiB.
func parseMemMiB(s string) (int, bool) {
	if s == "" {
		return 0, false
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
		return 0, false
	}
	mib := int(math.Ceil(n * mult))
	if mib < 1 {
		mib = 1
	}
	return mib, true
}

// vmMemMiB converts a container-style memory cap ("2G", "512M", a raw byte
// count, "") into smolvm's integer MiB, applying the microvm default when
// unset. A value that does not parse falls back to the default rather than
// emitting a bad flag (vmLimitsPreflight rejects such a value up front).
func vmMemMiB(s string) string {
	if s == "" {
		return vmDefaultMemMiB
	}
	if mib, ok := parseMemMiB(s); ok {
		return strconv.Itoa(mib)
	}
	return vmDefaultMemMiB
}

// vmLimitsPreflight rejects a resource limit the microVM runners cannot honor,
// before the silent conversions in vmMemMiB/vmCPUs paper over it: a fractional
// CPU count (the runners accept only a whole number of vCPUs, and rounding up
// changes what the user asked for) and an unparseable memory cap (silently
// becoming 4 GiB is worse than an error).
func vmLimitsPreflight(o RunOpts) error {
	if cpus := cpusFromQuota(o.CPU); cpus != "" {
		n, err := strconv.ParseFloat(cpus, 64)
		if err != nil {
			return fmt.Errorf("limits.cpus %q is not a value the microVM backends understand "+
				"(a whole number of vCPUs like 2, or a systemd quota like 400%%)", o.CPU)
		}
		if math.Trunc(n) != n {
			return fmt.Errorf("limits.cpus %q is a fraction — the microVM backends take a whole "+
				"number of vCPUs (e.g. 2)", o.CPU)
		}
	}
	if o.Mem != "" {
		if _, ok := parseMemMiB(o.Mem); !ok {
			return fmt.Errorf("limits.memory %q is not a valid memory size (e.g. 2G, 512M)", o.Mem)
		}
	}
	return nil
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
