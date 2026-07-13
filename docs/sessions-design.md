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
(Ctrl-q) only kills the client — the server, and whatever the agent is doing,
keeps running.

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
(`sandboxer-<slug>-<8-hex sha256 of the .sandboxer base dir>` — same-named
sandboxes in different projects never collide) and discovery labels
(`sandboxer.managed/slug/base/hash`). Everything — list's STATE column,
doctor's orphan report, `rm-all`'s sweep — is an engine query. A state file
would have to be kept in sync with reality the engine already owns; after an
`rm -rf` of the project it would also be gone, while the labels still let
`doctor` find the orphaned container.

### D3 — config staleness via a create-argv hash; converge, never surprise

`ConfigHash` fingerprints the canonical create argv (image, mounts, env,
proxies, limits — excluding the name/labels), is stamped into
`sandboxer.hash` at create time and recomputed on every enter. The pure
decision table (`planSession`):

    not found               → create
    stopped + fresh         → start
    stopped + stale         → recreate
    running + fresh         → exec
    running + stale + idle  → recreate
    running + stale + busy  → refuse (detach others, or --ephemeral)

"Idle" = no clients on the in-container tmux server — an informed guess, not a
lock. `exec` rides a running fresh session but **never** creates or replaces
the daemon container (that is enter's job); anything else falls back to a
one-shot run (with a notice when a running session is stale; a missing or
stopped session falls back silently — that is exec's normal pre-session
behavior, nothing surprising to flag), so scripts keep working.

Addendum (image customization work): freshness now also compares the
container's **image ID** against the engine's current one for the same tag, so
an image rebuilt under an unchanged tag (e.g. `build-image` re-run on the
default image) recreates the session through the same table; an image the
engine doesn't have yet reads as unknown and skips the check.

### D4 — tmux from the toolbox image, on its own socket, with a shipped config

tmux (+ ncurses terminfo) is baked into the image; the server runs as `tmux -L
sandboxer` under `/etc/sandboxer/tmux.conf` (every window goes through the
rc.sh launcher; Ctrl-q detaches; mouse + deep scrollback). `--session <name>`
attaches/creates a named tmux session inside the same container, so several
terminals can share one sandbox. Same stale-image convention as the rc itself:
an image built before tmux degrades to the plain rc shell with a rebuild hint.

### D5 — stably-named egress sidecar, with `stop` between attach and remove

A one-shot run's egress proxy uses PID-suffixed names and dies with the run; a
session's egress must be *rediscoverable* by a later CLI invocation, so it
uses the session name as the ID (`<name>-int/-ext/-proxy`, `egress.UpNamed` /
`Lookup`). Fail-closed is unchanged — no proxy, no session. `stop` parks the
container **and** the proxy but keeps the networks and the sandbox files
(resume = plain start); `rm`/`rm-all` sweep container, proxy and networks. A
fresh-looking session whose proxy died is treated as stale and rebuilt rather
than left without an outbound path — under D3's busy guard: a running session
with clients attached refuses instead of being torn down under them.

## The honest limitation

A persistent session survives **client disconnect** — closing the terminal,
dropping SSH, Ctrl-q. It does **not** survive a container stop, a host reboot,
or an engine restart: `sleep infinity` and the tmux server are processes, and
the processes inside the container die with it (the container itself and its
filesystem are kept and restarted by the next enter). Resuming the *agent's
conversation* after that is the agent's own job — e.g. `claude --continue` —
sandboxer only guarantees the place it runs in (container fs, sandbox dir,
per-sandbox `$HOME`) is still there.

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
