# Persistent sessions — design

Status: **implemented** (v0.21 line) — session naming/hash, stable-ID egress
sidecar, tmux in the toolbox image, the lifecycle state machine, persistent
`enter`/`exec`, `stop`, session-aware `rm`/`list`/`show`/`doctor`/`compose`.

This is a decisions-and-why record (an RFC), not a mirror of code
(`proc-doc-as-code`): it captures the mechanism choices and the alternatives
rejected, so the implementation PRs stay small and link back here.

## The problem

Every `enter`/`exec` used to be a one-shot `run --rm` container: leave the
shell (or lose your SSH connection) and everything in flight — a running agent,
a dev server, shell history, warm caches outside the sandbox dir — died with
the container. Long-running agent work needs a place to keep running while no
human is attached, and a way to come back to it.

## Chosen shape: one persistent container per sandbox + in-container tmux

A detached, named, labeled session container (`run -d --init … sleep
infinity`) is created on the first persistent `enter` and reused by every
later `enter`/`exec`; attaching means `exec`-ing a tmux client against a tmux
server that lives *inside* that container (`tmux -L sandboxer`). Detach
(Ctrl-Space d) only kills the client — the server, and whatever the agent is
doing, keeps running.

Rejected alternatives:

- **Host tmux** — multiplexes the *host* terminal, so the workload still dies
  with its one-shot container; it also drags host config into the loop, which
  the isolation model forbids.
- **`podman/docker attach` to the main process** — single shared stream, no
  multiple windows, detach keybinding differs per engine/config, and a TTY
  resize race on reattach; tmux gives sessions/windows/scrollback for free.
- **zellij** — nicer UX but a much bigger binary, not in the nixpkgs
  binary-cache-everywhere sense we rely on, and its config/keybinding surface
  is overkill for "detach and come back".
- **GNU screen** — solves the same problem with a smaller community, fewer
  scriptable probes (`list-clients`, `list-sessions` power the idle check and
  the tests); tmux is the conservative default.

## Decisions

### D1 — persistent is the default; ephemeral is the escape hatch

`enter`/`exec` use the session container unless told otherwise: `--ephemeral`
(flag), `session: ephemeral` (profile), or `SANDBOXER_SESSION=ephemeral`
(env). The env deliberately sits **above** the profile in the resolution chain
(unlike every other scalar) because it is an operator kill-switch: it must win
over a repo's committed `session:` choice.

### D2 — the engine's container store is the only session state

No metadata file. The session container carries a deterministic name
(`sandboxer-<slug>-<8-hex sha256 of the state base dir>` — same-named
sandboxes in different projects never collide) and discovery labels
(`sandboxer.managed/slug/base/hash/mounts`). Everything — list's STATE column,
doctor's orphan report, `clean`'s sweep — is an engine query. A state file
would have to be kept in sync with reality the engine already owns; after an
`rm -rf` of the project it would also be gone, while the labels still let
`doctor` find the orphaned container.

### D3 — config staleness via a create-argv hash; converge, never surprise

`ConfigHash` fingerprints the canonical create argv (image, mounts, env,
proxies, limits — excluding the name/labels), is stamped into
`sandboxer.hash` at create time and recomputed on every enter.

What is fingerprinted must be *configuration*, not ambient shell state. The
agents' auth env used to be in the create argv, so a rotated token — or simply
entering from a terminal that had not exported one — read as "profile
changed". That is not a config change, and paid for as one it made a session
permanently stale. Credentials are now scoped to the process that needs them
(`run` for a one-shot, each `exec` for a session shell), which keeps them out
of the hash *and* out of the long-lived container's environment, and lets a
rotation reach the next shell with no rebuild at all. Everything left in the
hash genuinely cannot change without a new container: image, mounts and their
generations, limits, proxy/egress wiring. The pure
decision table (`planSession`):

    not found               → create
    stopped + fresh         → start
    stopped + stale         → recreate
    running + fresh         → exec
    running + stale + idle  → recreate
    running + stale + busy  → attach as-is, announce the pending config

"Idle" means the in-container tmux server holds **no session** (`SessionIdle`
= `tmux -L sandboxer list-sessions`), and it is a *positive* finding: an
engine error, or no tmux in the image, reads as busy. Deliberately NOT "no
clients attached" — a detached session is precisely the case where an agent is
running unattended, and treating it as idle would destroy the thing sessions
exist to protect.

A stale **busy** session is attached, never sidestepped. Handing the user a
one-shot `run --rm` container instead (as enter did for a while) is the worst
of both: tmux is that container's main process, so Ctrl-Space d destroys
everything in it, the real session stays running and unreachable, and since
nothing converges it the same thing happens on every later enter. Attaching
keeps detach semantics uniform — enter always lands in the session container —
and the banner names the pending config plus the one command that applies it
(`stop` + `enter`); `--recreate` still forces the rebuild.

### D3a — announce WHAT went stale, and offer the rebuild when it is damage

One hash cannot say what moved, and "profile changed" was wrong most of the
time it mattered. A narrowed sandbox re-expands its `include` patterns against
the live worktree on every enter, so its mount set tracks the **host**: a
directory that now matches a pattern, or one a checkout or a build removed and
recreated. Told their profile changed, a user who never touched it has no
reason to act.

So the mount identities themselves are recorded, in `sandboxer.mounts`
(`sandbox.EncodeMountIDs` — base64url, so it can never contain a space and
shift `InspectSession`'s field split). A fresh resolve diffs against them and
the banner says `mounts moved: ~ /…/services/api (recreated on the host)`.
Deliberately label-only: `MountGen` already carries the same identity into the
hash, and putting it in the argv too would flip the hash of every session that
exists the moment the field shipped. Two things stay honest — an absent label
(a session predating it) degrades to the old "profile changed" rather than
reporting every mount as new, and because both can move at once, the reason is
re-derived with the *recorded* identities substituted back in, appending "the
profile also changed" only when that still does not reproduce the stored hash.

Mount drift is also the one stale shape where attaching as-is is not a
postponement but a defect: a bind mount is pinned to an inode, so a session
whose mounted directory was recreated is *already* reading a tree the host
threw away. Rebuilding fixes it and destroys the running agent; neither is safe
to choose for the user, so on a terminal enter asks, once, and treats anything
but yes as "attach, as before". Never asked without a terminal — a scripted
enter must not block on a question nobody will read, and `--recreate` is that
answer. This is the CLI's only interactive prompt, and the asymmetry of the two
losses is the whole justification.

`exec` rides a running fresh session but **never** creates or replaces the
daemon container (that is enter's job); anything else falls back to a one-shot
run (with a notice when a running session is stale; a missing or stopped
session falls back silently — that is exec's normal pre-session behavior,
nothing surprising to flag), so scripts keep working. A one-shot for a single
command is safe — it costs no session, and it runs with the configuration the
user just asked for.

Addendum (image customization work): freshness now also compares the
container's **image ID** against the engine's current one for the same tag, so
an image rebuilt under an unchanged tag (e.g. `image build` re-run on the
default image) recreates the session through the same table; an image the
engine doesn't have yet reads as unknown and skips the check.

### D4 — tmux from the toolbox image, on its own socket, with a shipped config

tmux (+ ncurses terminfo) is baked into the image; the server runs as `tmux -L
sandboxer` under `/etc/sandboxer/tmux.conf` (every window goes through the
rc.sh launcher; the prefix is Ctrl-Space, so Ctrl-Space d detaches; mouse +
deep scrollback). `--session <name>`
attaches/creates a named tmux session inside the same container, so several
terminals can share one sandbox. Same stale-image convention as the rc itself:
an image built before tmux degrades to the plain rc shell with a rebuild hint.

### D5 — stably-named egress sidecar, with `stop` between attach and remove

A one-shot run's egress proxy uses PID-suffixed names and dies with the run; a
session's egress must be *rediscoverable* by a later CLI invocation, so it
uses the session name as the ID (`<name>-int/-ext/-proxy`, `egress.UpNamed` /
`Lookup`). Fail-closed is unchanged — no proxy, no session. `stop` parks the
container **and** the proxy but keeps the networks and the sandbox files
(resume = plain start); `rm`/`clean` sweep container, proxy and networks. A
fresh-looking session whose proxy died is treated as stale and rebuilt rather
than left without an outbound path — under D3's busy guard: a session holding a
tmux session is attached as-is instead of being torn down under it.

## The honest limitation

A persistent session survives **client disconnect** — closing the terminal,
dropping SSH, Ctrl-Space d. It does **not** survive a container stop, a host reboot,
or an engine restart: `sleep infinity` and the tmux server are processes, and
the processes inside the container die with it (the container itself and its
filesystem are kept and restarted by the next enter). Resuming the *agent's
conversation* after that is the agent's own job — e.g. `claude --continue` —
sandboxer only guarantees the place it runs in (container fs, sandbox dir,
per-sandbox `$HOME`) is still there.

> **Historical note:** the detach-time deps push described below was removed in
> v0.27.0 when sandboxes became git worktrees — work lands on each source's
> configured branch; nothing is pushed back on detach.

The detach-time deps push is likewise best-effort **by design**: detaching
pushes rw deps back to their origins while the session — and anything running
in it — keeps going, so a writer caught mid-write can land a torn copy at the
origin. Accepted rather than skipped: the push is the same copy `sandboxer
push` does, so re-running it once the sandbox is quiet restores a consistent
copy, while skipping it would silently break enter's "leaving returns your
work" contract that every other exit path keeps.

## Tests

Unit (gate, engine-free): `planSession`'s full decision table, naming/hash
purity, argv builders, the CLI routing seams (`backendEnsureSession`/
`backendExecSession`/… stubs), compose's create+exec pair. Integration
(`-tags integration`, skip-clean): the real lifecycle against podman/docker —
labels, exec persistence, stale-recreate, stop/resume, rm sweep, egress
network cleanup — see `internal/backend/session_integration_test.go` and
`internal/cli/session_integration_test.go`.
