package backend

// Two microVM runners ship behind ONE backend implementation. smolvm and
// microsandbox are both thin consumption layers over libkrun (KVM on Linux, HVF
// on macOS), so the isolation primitive, the mount model, the session policy
// (planSession), the host-side record and the tmux capture/restore are shared
// verbatim; only the CLI vocabulary differs. vmRunner is that vocabulary — the
// one seam between the two — so a third runner is a file, not a fork.
//
// Everything a runner is asked for is either an ARGV (pure, golden-tested
// without a hypervisor) or a narrow engine query. The lifecycle itself lives in
// vm_session.go and never branches on the engine. The toolbox image build is NOT
// here: it is runner-agnostic host nix (toolbox.BuildImageHostNix), so no runner
// has a build dialect of its own.
type vmRunner interface {
	// bin is the actual binary to exec (an override path or the default name).
	bin() string
	// createArgv creates the named machine for o, stamped with hash where the
	// runner supports labels.
	createArgv(o RunOpts, name, hash string) []string
	// hashArgv is the canonical configuration argv the session hash is taken
	// over: the create argv minus the name and any labels.
	hashArgv(o RunOpts) []string
	startArgv(name string) []string
	stopArgv(name string) []string
	removeArgv(name string) []string
	// execArgv runs cmdArgs inside the running machine, with the caller's stdio.
	execArgv(o RunOpts, name string, cmdArgs []string) []string
	// guestExecArgv is the non-interactive "read state out of the guest" form
	// (tmux capture, agent detection, the idleness probe).
	guestExecArgv(name string, argv []string) []string
	// runArgv is the one-shot ephemeral machine.
	runArgv(o RunOpts) []string
	// listMachines is the live inventory (name + lowercase state).
	listMachines() []vmMachine
	// startsOnCreate reports whether create already boots the machine, so the
	// lifecycle knows whether a separate start is needed.
	startsOnCreate() bool
	// ensureImage resolves o.Image to the reference the runner is handed,
	// building/importing the locally-built toolbox image on first use.
	ensureImage(o RunOpts) (string, error)
	// imageID is the runner's content id for a stored image, or "" when absent
	// (which skips the freshness check — never a false "stale").
	imageID(image string) string
	// recordDir is the per-engine subdirectory of the machine-record store, ""
	// for the flat legacy layout smolvm has always used.
	recordDir() string
	// preflight rejects a configuration this runner cannot honor, before it is
	// launched — so the user gets the reason instead of the runner's symptom.
	preflight(o RunOpts) error
}

// isVMEngine reports whether engine is one of the microVM runners, i.e. whether
// the vm* path — not the container path — owns this session.
func isVMEngine(engine string) bool {
	return engine == smolvmEngine || engine == msbEngine
}

// IsVMEngine is the exported form of isVMEngine: whether an engine identity
// names one of the microVM runners. Callers outside the package use it to treat
// the runner identities ("smolvm"/"microsandbox") distinctly from the container
// engines — e.g. the CLI must not hand a runner a container-dialect argv.
func IsVMEngine(engine string) bool { return isVMEngine(engine) }

// vmRunnerFor returns the dialect for a microVM engine identity. smolvm is the
// default so a zero/unknown engine keeps the original behaviour: only an
// explicit microsandbox identity selects msb.
func vmRunnerFor(engine string) vmRunner {
	if engine == msbEngine {
		return msbRunner{}
	}
	return smolvmRunner{}
}

// smolvmRunner speaks smolvm's `machine <verb>` dialect. Every method delegates
// to the free functions in vm.go, which stay the golden-tested surface.
type smolvmRunner struct{}

func (smolvmRunner) bin() string { return smolvmBin() }

// createArgv ignores hash: smolvm has no labels, so a machine's identity lives
// entirely in the host-side record (vm_state.go).
func (smolvmRunner) createArgv(o RunOpts, name, _ string) []string { return vmCreateArgv(o, name) }

func (smolvmRunner) hashArgv(o RunOpts) []string     { return vmSessionHashArgv(o) }
func (smolvmRunner) startArgv(name string) []string  { return vmStartArgv(name) }
func (smolvmRunner) stopArgv(name string) []string   { return vmStopArgv(name) }
func (smolvmRunner) removeArgv(name string) []string { return vmRemoveArgv(name) }
func (smolvmRunner) runArgv(o RunOpts) []string      { return vmRunArgv(o) }
func (smolvmRunner) listMachines() []vmMachine       { return vmListMachines() }
func (smolvmRunner) startsOnCreate() bool            { return false }
func (smolvmRunner) imageID(image string) string     { return vmImageID(image) }
func (smolvmRunner) recordDir() string               { return "" }
func (smolvmRunner) preflight(o RunOpts) error {
	if err := vmSharePreflight(o); err != nil {
		return err
	}
	return vmLimitsPreflight(o)
}
func (smolvmRunner) ensureImage(o RunOpts) (string, error) { return vmEnsureImage(o) }

func (smolvmRunner) execArgv(o RunOpts, name string, cmdArgs []string) []string {
	return vmExecArgv(o, name, cmdArgs)
}

func (smolvmRunner) guestExecArgv(name string, argv []string) []string {
	return append([]string{"machine", "exec", "--name", name, "--"}, argv...)
}

// msbRunner speaks microsandbox's `msb <verb>` dialect (see msb.go).
type msbRunner struct{}

func (msbRunner) bin() string { return msbBin() }

func (msbRunner) createArgv(o RunOpts, name, hash string) []string {
	return msbCreateArgv(o, name, hash)
}

func (msbRunner) hashArgv(o RunOpts) []string     { return msbHashArgv(o) }
func (msbRunner) startArgv(name string) []string  { return msbStartArgv(name) }
func (msbRunner) stopArgv(name string) []string   { return msbStopArgv(name) }
func (msbRunner) removeArgv(name string) []string { return msbRemoveArgv(name) }
func (msbRunner) runArgv(o RunOpts) []string      { return msbRunArgv(o) }
func (msbRunner) listMachines() []vmMachine       { return msbListMachines() }

// startsOnCreate: `msb create` boots the sandbox in the background, so the
// lifecycle must NOT follow it with a start (which would fail on a running one).
func (msbRunner) startsOnCreate() bool                  { return true }
func (msbRunner) imageID(image string) string           { return msbImageInspect(image) }
func (msbRunner) recordDir() string                     { return msbEngine }
func (msbRunner) preflight(o RunOpts) error             { return msbPreflight(o) }
func (msbRunner) ensureImage(o RunOpts) (string, error) { return msbEnsureImage(o) }

func (msbRunner) execArgv(o RunOpts, name string, cmdArgs []string) []string {
	return msbExecArgv(o, name, cmdArgs)
}

func (msbRunner) guestExecArgv(name string, argv []string) []string {
	return msbGuestExecArgv(name, argv)
}
