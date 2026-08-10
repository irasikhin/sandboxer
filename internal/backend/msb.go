package backend

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/execx"
)

// This file is the microVM dialect: microsandbox (`msb`) on libkrun (KVM on
// Linux, HVF on macOS). sandboxer shells out to msb, so the whole backend
// stays a set of pure argv builders a golden test pins without a hypervisor;
// the lifecycle (vm_session.go), the host-side record (vm_state.go) and the
// image store (vm_image.go) sit on top. See docs/microsandbox-spike.md for the
// measured comparison that selected msb over its smolvm predecessor.
//
// Three msb capabilities this adapter leans on:
//
//   - a NAME-BOUND network policy engine (--net-rule) whose `*.suffix` targets
//     are exactly squid's leading-dot grammar, so the allowlist survives the
//     translation intact instead of narrowing to exact hosts;
//   - an image STORE (msb load) — the toolbox tar is imported once and every
//     later create is boot-only, instead of re-importing a multi-GB tar;
//   - labels, so a sandboxer machine is identifiable through the engine alone.
//
// The labels are stamped for exactly that reason, but the SOURCE OF TRUTH for
// a session's identity stays the host-side record (vm_state.go): one
// mechanism, one set of sweeps, no drift between two stores.

// msbEngine is the engine identity the `microsandbox` backend resolves to (the
// msb CLI). SANDBOXER_MSB overrides the looked-up path.
const msbEngine = "microsandbox"

// msbSecretsEnv opts into microsandbox's host-scoped --secret for the agents'
// auth env instead of a plain --env. See msbSecretArgs for the trade.
const msbSecretsEnv = "SANDBOXER_MSB_SECRETS"

// msbCreateArgv assembles the msb argv that creates the named sandbox for o:
// `msb create --name N <labels> <common> IMAGE`. `msb create` also BOOTS the
// machine — the lifecycle never follows it with a start — and the image is a
// trailing positional.
//
// The labels carry the same identity a container session stamps, so a machine is
// discoverable through `msb list --label` alone; they sit HERE and not in
// msbCommonArgs so they stay out of the session hash (msbHashArgv) — exactly the
// container path's split, where relabeling a session never flips its hash.
func msbCreateArgv(o RunOpts, name, hash string) []string {
	args := []string{"create", "--name", name}
	args = append(args, msbLabelArgs(o, hash)...)
	args = append(args, msbCommonArgs(o)...)
	return append(args, o.Image)
}

// msbHashArgv is the canonical config argv a microsandbox session's hash is
// taken over: the create argv minus the name AND the labels, so a rename or a
// relabel never flips the hash while any change to the image, mounts, env, size
// or network policy does.
func msbHashArgv(o RunOpts) []string {
	return append(append([]string{"create"}, msbCommonArgs(o)...), o.Image)
}

// msbLabelArgs renders the session identity labels. An empty value is skipped
// rather than emitted as a bare marker: `--label k=` and `--label k` mean
// different things to msb, and a one-shot run has no base dir to record.
func msbLabelArgs(o RunOpts, hash string) []string {
	var args []string
	add := func(k, v string) {
		if v != "" {
			args = append(args, "--label", k+"="+v)
		}
	}
	add(LabelManaged, "true")
	add(LabelSlug, o.Slug)
	add(LabelBase, o.BaseDir)
	add(LabelHash, hash)
	add(LabelMounts, o.MountIDs)
	return args
}

// msbStartArgv starts a stopped sandbox.
func msbStartArgv(name string) []string { return []string{"start", name} }

// msbStopArgv stops a running sandbox, keeping its disk so a later start
// resumes it.
func msbStopArgv(name string) []string { return []string{"stop", name} }

// msbRemoveArgv deletes a sandbox non-interactively (-f stops it first, which a
// programmatic teardown cannot do in a separate step without racing).
func msbRemoveArgv(name string) []string { return []string{"remove", "-f", name} }

// msbListArgv lists sandboxes as JSON (name + status + image).
func msbListArgv() []string { return []string{"list", "--format", "json"} }

// msbExecArgv runs cmdArgs inside the running sandbox name. msb has NO -i flag
// (docker's grammar, not msb's — exec pipes stdin by default, and --stream
// exists for byte-faithful piping), so interactivity is -t alone, only with a
// real TTY; -w pins the workdir; TERM rides along so full-screen TUIs render;
// and the agents' auth env travels per exec so a rotated token reaches the
// sandbox with no rebuild (see msbAuthEnvArgs).
func msbExecArgv(o RunOpts, name string, cmdArgs []string) []string {
	args := []string{"exec"}
	if IsTerminal(o.Stdin) && IsTerminal(o.Stdout) {
		args = append(args, "-t")
	}
	args = append(args, "-w", o.Dest)
	if term := os.Getenv("TERM"); term != "" {
		args = append(args, "-e", "TERM="+term)
	}
	args = append(args, msbAuthEnvArgs(o)...)
	args = append(args, name, "--")
	return append(args, cmdArgs...)
}

// msbGuestExecArgv is the non-interactive "read something out of the guest"
// primitive (tmux layout capture, agent detection, the idleness probe).
func msbGuestExecArgv(name string, argv []string) []string {
	return append([]string{"exec", name, "--"}, argv...)
}

// msbRunArgv assembles the argv for a one-shot ephemeral sandbox — msb removes
// it when the command exits, so there is no --rm analogue to pass (msb's own
// --rm hides a path from the guest rootfs, an unrelated flag). Like exec, msb
// run has NO -i flag — stdin is piped by default — so an interactive run adds
// only -t, and only on a real TTY.
func msbRunArgv(o RunOpts) []string {
	args := []string{"run"}
	if o.Interactive && IsTerminal(o.Stdin) && IsTerminal(o.Stdout) {
		args = append(args, "-t")
	}
	args = append(args, msbAuthEnvArgs(o)...)
	args = append(args, msbCommonArgs(o)...)
	args = append(args, o.Image, "--")
	return append(args, o.Args...)
}

// msbCommonArgs assembles the machine flags shared by create and run: workdir,
// the identity-mapped host shares, the identity env, the machine size and the
// network policy. Credentials are NOT here — they travel per exec/run, and in
// --secret mode only as a reference (msbSecretArgs).
func msbCommonArgs(o RunOpts) []string {
	args := []string{"-w", o.Dest}
	// The whole sandbox root as one share when nothing narrows it — a srcs edit
	// then shows up in a running machine live. A narrowed sandbox omits it, and
	// that absence is the containment boundary (see RunOpts.MountDest).
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
	args = append(args, "-m", vmMemMiB(o.Mem)+"M", "-c", vmCPUs(o.CPU))
	args = append(args, msbNetworkArgs(o)...)
	args = append(args, msbSecretArgs(o)...)
	if csv := o.RT.DomainsCSV(); csv != "" {
		args = append(args, "-e", "SANDBOXER_ALLOW_DOMAINS="+csv)
	}
	// The profile.json, shared read-only at /run/sandboxer — msb shares
	// DIRECTORIES only, so it is staged into a per-sandbox dir first
	// (vmRunDir/stageProfileJSON).
	if d := vmRunDir(o); d != "" {
		args = append(args, "-v", d+":/run/sandboxer:ro")
	}
	args = append(args, msbExtraMountsAndEnv(o.Profile)...)
	return args
}

// msbNetworkArgs renders the machine's outbound policy. microsandbox defaults to
// an OPEN network (an implicit allow@public when no rule is given), so the
// states are:
//
//   - egress.proxy set → proxy-delegated egress: leave the network open and
//     point the guest's HTTP(S) clients at the proxy. There is no squid in the
//     path, so the proxy IS the control point. A loopback proxy is REWRITTEN
//     to msbHostAlias and the host group opened on its port (msbGuestProxyURL)
//     — msb's guest has a real network stack where 127.0.0.1 is guest-local.
//   - egress disabled (egress.enabled = false / SANDBOXER_NO_EGRESS) → open, no
//     flags at all.
//   - egress on with an allowlist → --no-net (default deny) plus one allow rule
//     per domain. The rules are NAME-bound and per-port, so they mirror the
//     squid sidecar they replace: the domain AND its subdomains over HTTP and
//     HTTPS, and a raw IP — even the allowed domain's own — is refused.
//   - egress on with an EMPTY allowlist → --no-net alone: a fully offline
//     machine, a valid state (a default-deny VM with no rules simply reaches
//     nothing).
//
// These flags live in the create argv, so they fold into the session hash: a
// domain added or egress toggled recreates the machine.
func msbNetworkArgs(o RunOpts) []string {
	if p := o.RT.Proxy; p != "" {
		p, hostRule := msbGuestProxyURL(p)
		args := []string{
			"-e", "HTTP_PROXY=" + p, "-e", "http_proxy=" + p,
			"-e", "HTTPS_PROXY=" + p, "-e", "https_proxy=" + p,
		}
		if o.RT.NoProxy != "" {
			args = append(args, "-e", "NO_PROXY="+o.RT.NoProxy, "-e", "no_proxy="+o.RT.NoProxy)
		}
		if hostRule != "" {
			// Rule-only invocation: any explicit rule replaces the implicit
			// open default with deny-by-default + exactly these rules, so
			// public is restated beside the host rule. Spelled as --net-rule
			// tokens, NOT `--net public` profiles: the --net flag only exists
			// since msb 0.6.7, while this vocabulary parses on every 0.6.x.
			args = append(args, "--net-rule", "allow@public,"+hostRule)
		}
		return args
	}
	if !egressRequired(o) {
		return nil
	}
	args := []string{"--no-net"}
	for _, t := range msbNetTargets(o.RT.Domains) {
		// One flag, two comma-separated rule tokens: HTTP and HTTPS, the same
		// two the squid sidecar allows (it denies CONNECT to anything but 443).
		args = append(args, "--net-rule",
			"allow@"+t+":tcp:80,allow@"+t+":tcp:443")
	}
	return args
}

// msbHostAlias is microsandbox's magic hostname: its DNS forwarder synthesizes
// the sandbox's gateway IP for it, and a gateway-bound dial is rewritten to the
// HOST loopback at connect time (msb crates/network: HOST_ALIAS +
// resolve_host_dst) — msb's host.docker.internal. It is the only way a guest
// reaches a proxy bound to the host's 127.0.0.1: the guest's loopback is its
// own smoltcp stack, so dialing 127.0.0.1 in-guest is refused immediately.
const msbHostAlias = "host.microsandbox.internal"

// msbGuestProxyURL adapts egress.proxy for the msb guest: a loopback proxy
// host (the common tunnel-client case) becomes msbHostAlias, everything else
// passes through untouched — a guest cannot dial the HOST's loopback. The
// second return is the --net-rule that dial needs — gateway IPs classify as
// the `host` destination group, which the default allow@public policy DENIES —
// scoped to the proxy's port ("" when nothing was rewritten: a public proxy is
// already covered by allow@public).
func msbGuestProxyURL(raw string) (guestURL, hostRule string) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw, ""
	}
	h := u.Hostname()
	if h != "localhost" && h != "::1" && !strings.HasPrefix(h, "127.") {
		return raw, ""
	}
	newHost := msbHostAlias
	rule := "allow@host:tcp"
	if port := u.Port(); port != "" {
		newHost += ":" + port
		rule += ":" + port
	}
	u.Host = newHost
	return u.String(), rule
}

// msbNetTargets maps the allowlist onto microsandbox rule targets. Every entry
// becomes a `*.domain` SUFFIX target, which matches the domain and its
// subdomains — the lossless translation of squid's leading-dot dstdomain, and of
// the leading dot the config itself accepts. Blanks and duplicates are dropped
// and the result sorted, so the machine hash is stable.
func msbNetTargets(domains []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range domains {
		d = strings.TrimPrefix(strings.TrimSpace(d), ".")
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, "*."+d)
	}
	sort.Strings(out)
	return out
}

// msbSecretArgs renders the agents' auth env as microsandbox HOST-SCOPED secret
// references — `--secret KEY@host,host…`, whose value msb reads from its own
// process environment at boot, stores only as a reference, and refuses to send
// anywhere but the listed hosts.
//
// It is OPT-IN (SANDBOXER_MSB_SECRETS=1) for two reasons the spike recorded:
// the guest sees a STAND-IN value, and whether the real one is substituted for
// an allowed host was never verified — a wrong guess there silently breaks every
// agent's authentication; and the value is bound at boot, so a rotation needs a
// machine restart, while the default per-exec --env picks the current token up
// on the next shell. Without an allowlist there is nothing to scope the secret
// to, so the mode degrades to the default rather than inventing a host list.
func msbSecretArgs(o RunOpts) []string {
	if !msbSecretsMode(o) {
		return nil
	}
	hosts := strings.Join(msbSecretHosts(o.RT.Domains), ",")
	args := make([]string, 0, 2*len(o.AuthEnv)+2)
	for _, kv := range o.AuthEnv {
		k, _, _ := strings.Cut(kv, "=")
		args = append(args, "--secret", k+"@"+hosts)
	}
	return append(args, "--on-secret-violation", "block-and-log")
}

// msbSecretsMode reports whether this run uses --secret for the auth env: opted
// in, with credentials to scope and an allowlist to scope them to.
func msbSecretsMode(o RunOpts) bool {
	return os.Getenv(msbSecretsEnv) == "1" && len(o.AuthEnv) > 0 && len(msbSecretHosts(o.RT.Domains)) > 0
}

// msbSecretHosts normalizes the allowlist for a --secret host list: plain
// hostnames (no `*.` prefix — the secret guard matches hosts, not rule targets),
// deduped and sorted.
func msbSecretHosts(domains []string) []string {
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

// msbAuthEnvArgs renders the agents' auth env as plain --env flags for the
// process that needs it (an exec, or a one-shot run) — the same scoping the
// container backend uses, and for the same reason: the credential never enters
// the long-lived machine's configuration, so it is neither inspectable there nor
// part of the session hash. Empty in --secret mode, where the reference on the
// create argv already carries it (and a value here would override the stand-in).
func msbAuthEnvArgs(o RunOpts) []string {
	if msbSecretsMode(o) {
		return nil
	}
	args := make([]string, 0, 2*len(o.AuthEnv))
	for _, kv := range o.AuthEnv {
		args = append(args, "-e", kv)
	}
	return args
}

// msbExtraMountsAndEnv adds the profile's extraMounts and env injections in the
// msb dialect: -v src:target[:ro] and -e k=v, keys sorted so map order never
// leaks into the argv.
func msbExtraMountsAndEnv(p *config.Profile) []string {
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

// msbPreflight rejects — with the reason — a configuration the guest cannot
// honor, before msb fails on it with a symptom. There is exactly one
// msb-specific trap: microsandbox mounts a tmpfs over /tmp AFTER the host
// shares, so any share whose GUEST path is under /tmp is shadowed and simply
// is not there. The sandbox root then "does not exist in guest" and every
// create fails; a source mount would silently be empty, which is worse.
// sandboxer's own paths are the project's ./sandboxes and the XDG state dir,
// so this only bites a profile that deliberately points somewhere under /tmp.
func msbPreflight(o RunOpts) error {
	// A regular-file extraMount is unshareable on any virtio-fs runner (it
	// shares directories only) — check it before the msb-specific trap.
	if err := vmSharePreflight(o); err != nil {
		return err
	}
	if err := vmLimitsPreflight(o); err != nil {
		return err
	}
	paths := append([]string{o.Dest, o.HomeDir}, o.SrcMounts...)
	if o.Profile != nil {
		for _, m := range o.Profile.ExtraMounts {
			paths = append(paths, m.Target)
		}
	}
	var bad []string
	for _, p := range paths {
		if p == "/tmp" || strings.HasPrefix(p, "/tmp/") {
			bad = append(bad, p)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("the microsandbox backend cannot expose %s: the guest mounts a tmpfs over /tmp "+
		"after the host shares, so anything under /tmp is invisible inside the sandbox — "+
		"move it off /tmp (e.g. the profile's worktreesDir)",
		strings.Join(bad, ", "))
}

// vmSharePreflight rejects a profile extraMount the microVM cannot expose.
// virtio-fs shares DIRECTORY trees only — a docker-style file bind mount
// (source is a regular file, e.g. a single dotfile or config) has no microVM
// share analogue, and silently sharing nothing would be worse than an upfront
// error. Only an EXISTING regular-file source is rejected; a missing source is
// left for the runner to answer (it materializes the path as an empty dir).
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
	return fmt.Errorf("the microsandbox backend cannot expose %s: virtio-fs shares directories only, "+
		"and a regular file has no share analogue — mount a directory that holds it",
		strings.Join(bad, ", "))
}

// vmLimitsPreflight rejects a resource limit the microVM cannot honor, before
// the silent conversions in vmMemMiB/vmCPUs paper over it: a fractional CPU
// count (the runner accepts only a whole number of vCPUs, and rounding up
// changes what the user asked for) and an unparseable memory cap (silently
// becoming 4 GiB is worse than an error).
func vmLimitsPreflight(o RunOpts) error {
	if cpus := cpusFromQuota(o.CPU); cpus != "" {
		n, err := strconv.ParseFloat(cpus, 64)
		if err != nil {
			return fmt.Errorf("limits.cpus %q is not a value the microsandbox backend understands "+
				"(a whole number of vCPUs like 2, or a systemd quota like 400%%)", o.CPU)
		}
		if math.Trunc(n) != n {
			return fmt.Errorf("limits.cpus %q is a fraction — the microsandbox backend takes a whole "+
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

// vmDefaultMemMiB / vmDefaultCPUs size a machine when the profile sets no
// limit. A microVM must be given a size (unlike a container's unbounded
// default), and sandboxer's workload is SEVERAL parallel agents, so the
// defaults are deliberately modest — a profile raises them through the
// ordinary `limits` (Mem/CPU) knobs.
const (
	vmDefaultMemMiB = "4096"
	vmDefaultCPUs   = "2"
)

// parseMemMiB is vmMemMiB's core: the MiB value of a container-style memory cap
// ("2G", "512M", a raw byte count), with ok=false for anything unparseable.
// vmMemMiB falls back to the default on !ok; vmLimitsPreflight REJECTS instead,
// so a bad value is a clear error, never a silent 4 GiB.
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
// count, "") into the integer MiB msb's -m takes, applying the microVM default
// when unset. A value that does not parse falls back to the default rather
// than emitting a bad flag (vmLimitsPreflight rejects such a value up front).
func vmMemMiB(s string) string {
	if s == "" {
		return vmDefaultMemMiB
	}
	if mib, ok := parseMemMiB(s); ok {
		return strconv.Itoa(mib)
	}
	return vmDefaultMemMiB
}

// vmCPUs converts a CPU quota (cpusFromQuota's "2"/"1.5"/"") into an integer
// vCPU count for msb's -c (which rejects a fraction), rounding up so a
// fractional quota never under-provisions, and applying the microVM default
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

// --- inventory ---------------------------------------------------------------

// msbSandbox is the subset of `msb list --format json` a session cares about.
// msb reports a capitalized status ("Running"/"Stopped"); parseMSBSandboxes
// lowercases it so the shared session code compares one vocabulary.
type msbSandbox struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// parseMSBSandboxes decodes `msb list --format json` into the shared vmMachine
// shape (pure, so it is unit-tested without a hypervisor); malformed output
// yields no machines rather than an error, mirroring the engine-query stance
// elsewhere.
func parseMSBSandboxes(out []byte) []vmMachine {
	var boxes []msbSandbox
	if json.Unmarshal(out, &boxes) != nil {
		return nil
	}
	machines := make([]vmMachine, 0, len(boxes))
	for _, b := range boxes {
		machines = append(machines, vmMachine{Name: b.Name, State: strings.ToLower(b.Status)})
	}
	return machines
}

// msbListMachines runs `msb list --format json` and returns the live sandboxes.
// A package var so a test can inject a fake inventory; a nil/error result is
// "no machines", never a crash.
var msbListMachines = func() []vmMachine {
	out, err := exec.Command(msbBin(), msbListArgv()...).Output()
	if err != nil {
		return nil
	}
	return parseMSBSandboxes(out)
}

// --- images ------------------------------------------------------------------

// msbImageInspect reports the image's manifest digest from msb's local store, or
// "" when the image is not cached (a non-zero exit) or the output is unreadable.
// The digest doubles as the freshness id: a rebuilt-and-reloaded toolbox image
// gets a new one, which reads as a stale session and recreates the machine.
var msbImageInspect = func(ref string) string {
	out, err := exec.Command(msbBin(), "image", "inspect", ref, "--format", "json").Output()
	if err != nil {
		return ""
	}
	var img struct {
		Digest string `json:"digest"`
	}
	if json.Unmarshal(out, &img) != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(img.Digest), "sha256:")
}

// msbImageExists reports whether msb has the image in its local store.
func msbImageExists(ref string) bool { return msbImageInspect(ref) != "" }

// msbImageID answers "what would a create boot NOW", which for a store-built
// toolbox image is the build tar's content id — NOT msb's own cached digest.
// The cached digest only changes after msbEnsureImage re-imports the tar, and
// that runs inside create/recreate — after planSession already ruled. Reading
// it here compared the old copy against itself, so a rebuilt image never went
// stale and a live machine kept its old rootfs forever (`image build` looked
// like a no-op). A public ref has no store tar (vmImageID "") — msb's cached
// copy is all there is, and "" skips the freshness check as usual.
func msbImageID(image string) string {
	if id := vmImageID(image); id != "" {
		return id
	}
	return msbImageInspect(image)
}

// msbLoadImage imports a docker-save tar into msb's image store under ref — the
// step that turns sandboxer's locally-built toolbox tar into a first-class
// cached image, so every later create is boot-only instead of re-importing it.
var msbLoadImage = func(ref, tar string, stderr io.Writer) error {
	cmd := exec.Command(msbBin(), "load", "-i", tar, "-t", ref, "-q")
	cmd.Stderr = stderr
	return cmd.Run()
}

// msbRemoveImage drops the image from msb's store AND deletes the build tar it
// was loaded from: `image rm` is an explicit user action, and leaving a
// multi-GB tar behind after it would be a surprise. Idempotent — an image
// absent from either store is success.
func msbRemoveImage(ref string) error {
	if msbImageExists(ref) {
		if err := execx.Run(msbBin(), "image", "remove", "-f", ref); err != nil {
			return err
		}
	}
	return vmRemoveImage(ref)
}

// msbEnsureImage resolves o.Image to the reference `msb create` is handed. An
// uncustomized image — the stock GHCR-published default and a user-set ref
// alike — passes straight through: msb pulls public refs host-side on first
// create and caches them in its store (and a copy `sandboxer image build`
// loaded under the same ref IS that cache, so an explicit local build still
// wins on an offline host). Only a variant with a non-empty spec is built
// here — it exists nowhere public — ensured in msb's store by building the
// tar (once, with host nix) and loading it on first use unless
// SANDBOXER_NO_AUTOBUILD is set.
func msbEnsureImage(o RunOpts) (string, error) {
	if o.Spec.Empty() {
		return o.Image, nil // prebuilt or user-chosen — msb pulls (or has) it
	}
	if msbImageExists(o.Image) && msbImageCurrent(o.Image) {
		return o.Image, nil
	}
	hint := "sandboxer image build --backend microsandbox <profile> (this variant image needs its profile)"
	if !vmImageExists(o.Image) {
		if os.Getenv("SANDBOXER_NO_AUTOBUILD") != "" {
			return "", fmt.Errorf("toolbox image %q is not in the microsandbox image store "+
				"and is built locally (never published) — build it with:\n  %s", o.Image, hint)
		}
		if o.Stderr != nil {
			fmt.Fprintf(o.Stderr, "sandboxer: toolbox image %q not found — building it now "+
				"(one-time, several minutes; disable with SANDBOXER_NO_AUTOBUILD=1)…\n", o.Image)
		}
		if err := vmBuildImageToStore(o); err != nil {
			return "", fmt.Errorf("%w — build manually with: %s", err, hint)
		}
	}
	if err := msbLoadStoredImage(o.Image, o.Stderr); err != nil {
		return "", fmt.Errorf("%w — build manually with: %s", err, hint)
	}
	return o.Image, nil
}

// msbCreateFailHint names the likeliest cause when a create died with the
// STOCK image absent from msb's store: the default ref is prebuilt and pulled
// from GHCR at create time, so on an offline (or blocked) host the pull is
// what failed — an actionable situation the raw msb error does not name. Only
// the default triggers it: a user-set ref or a loaded variant fails for its
// own reasons.
func msbCreateFailHint(image string) string {
	if image != config.DefaultImage || msbImageExists(image) {
		return ""
	}
	return "\n  (network needed to pull the prebuilt image — or build locally: sandboxer image build)"
}

// msbLoadStoredImage imports the tar the build stored (vmStoreImage) into
// msb's image store under the image's own name — the build artifact and the
// bootable image live in separate stores, and this is the handoff between
// them.
func msbLoadStoredImage(image string, stderr io.Writer) error {
	tar := vmImagePath(image)
	if tar == "" || !pathExists(tar) {
		return fmt.Errorf("toolbox image %q is missing from the image store after the build", image)
	}
	if stderr != nil {
		fmt.Fprintf(stderr, "sandboxer: importing %s into the microsandbox image store…\n", image)
	}
	src, cleanup, err := msbLoadableTar(tar)
	if err != nil {
		return fmt.Errorf("load image %q into microsandbox: %w", image, err)
	}
	defer cleanup()
	if err := msbLoadImage(image, src, stderr); err != nil {
		return fmt.Errorf("load image %q into microsandbox: %w", image, err)
	}
	if p := msbLoadMarkerPath(image); p != "" {
		_ = os.WriteFile(p, []byte(vmImageID(image)), 0o600)
	}
	return nil
}

// msbLoadableTar returns a path holding the store tar in the form `msb load`
// accepts, with a cleanup for any temp file it made. The store artifact is the
// nix buildLayeredImage output — a GZIPPED docker tarball, which docker load
// decompresses transparently — but msb opens the outer archive
// raw and dies parsing gzip bytes as a tar header ("numeric field did not have
// utf-8 text… when getting cksum"). A gzipped tar is therefore streamed out
// BESIDE the store tar (never /tmp — often a tmpfs too small for a multi-GB
// image; the sibling temp also inherits the store's filesystem, the EXDEV
// lesson) for the one-time import; a plain tar passes through as-is.
func msbLoadableTar(tarPath string) (string, func(), error) {
	f, err := os.Open(tarPath)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = f.Close() }()
	magic := make([]byte, 2)
	if n, _ := io.ReadFull(f, magic); n < 2 || magic[0] != 0x1f || magic[1] != 0x8b {
		return tarPath, func() {}, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", nil, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", nil, fmt.Errorf("decompress %s: %w", tarPath, err)
	}
	defer func() { _ = gz.Close() }()
	tmp, err := os.CreateTemp(filepath.Dir(tarPath), ".msb-load-*.tar")
	if err != nil {
		return "", nil, err
	}
	if _, err := io.Copy(tmp, gz); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", nil, fmt.Errorf("decompress %s: %w", tarPath, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", nil, err
	}
	return tmp.Name(), func() { _ = os.Remove(tmp.Name()) }, nil
}

// msbLoadMarkerPath is the sidecar recording WHICH build tar was imported under
// an image's reference.
func msbLoadMarkerPath(image string) string {
	p := vmImagePath(image)
	if p == "" {
		return ""
	}
	return p + ".msb"
}

// msbImageCurrent reports whether msb's cached copy of image was imported from
// the tar that is in the store NOW. Without it a rebuilt tar would leave
// microsandbox booting its old cached copy forever — the ref is present, so
// nothing would ever re-import it. A missing tar answers "current": there is
// nothing to compare against, and evicting a working image because its build
// artifact was reclaimed would be strictly worse.
func msbImageCurrent(image string) bool {
	id := vmImageID(image)
	if id == "" {
		return true
	}
	data, err := os.ReadFile(msbLoadMarkerPath(image))
	return err == nil && string(data) == id
}

// msbHome is the effective MSB_HOME: microsandbox's state root, which every
// sandbox's agent-relay UNIX SOCKET path derives from. It matters because that
// socket must stay under the kernel's 108-byte sun_path limit — a deep home
// makes every `create` fail — so sandboxer never points msb at its own (long)
// per-project state dir and doctor checks the length instead.
func msbHome() string {
	if h := os.Getenv("MSB_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".msb")
}

// msbHomeRoomy reports whether MSB_HOME leaves room for a sandbox's relay
// socket. The derived path is MSB_HOME plus a per-sandbox suffix; msbHomeBudget
// is the slack that suffix needs, measured against the observed failure ("the
// shortest derived path is 142 bytes" for a 70-byte home).
func msbHomeRoomy(home string) bool { return home != "" && len(home) <= msbHomeBudget }

// msbHomeBudget is the longest MSB_HOME that still fits a relay socket path in
// sun_path (108 bytes), leaving room for the per-sandbox suffix.
const msbHomeBudget = 40
