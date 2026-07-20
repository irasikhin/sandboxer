# A live view for narrowed sandboxes — design sketch

Status: **not implemented, not decided.** This is an RFC for the one thing the
mount-drift work (`docs/sessions-design.md` D3a) does not deliver: a narrowed
sandbox where a new or recreated directory shows up in an **already running**
container, with no enter and no rebuild. It exists so D3a does not read as the
final word on the question.

## What is settled, and what is left

`include` narrows by mounting one directory per match and leaving `<slug>/`
unmounted; not mounting the root IS the boundary (`docs/view-mounts-design.md`).
That makes the mount set a snapshot taken at container-create time, and the
host keeps moving underneath it:

- a directory that starts matching a pattern is not mounted, and cannot be;
- a mounted directory that a build or a checkout removes and recreates leaves
  the container's bind mount pinned to an inode nobody writes to any more.

Both are *detected* on every enter (patterns re-expand against the worktree,
`MountFingerprint` restats each mount) and, since D3a, *named*: an idle session
converges, a busy one is told what moved and offered the rebuild. The gap is
that applying it always costs a new container, and a container is where the
user's agent lives.

An **unnarrowed** sandbox has none of this. Its single rw mount of `<slug>/` is
a live window: anything created under it appears instantly, and the root is
inode-stable in practice (a git operation inside it recreates children, never
the mounted directory). Everything below is about buying that property for the
narrowed case without giving up the wall.

## Why it cannot be done with bind mounts

Neither podman nor docker can add a bind mount to a running container: mounts
are established at create time from the caller's mount namespace, and there is
no API to attach one later. Three ways around that were considered and none
survives:

- **Mount propagation** (`:rslave` on a shared parent). Propagates *mounts*,
  not directory creation — so the host side would have to `mount --bind` each
  new view dir, which needs privileges sandboxer does not have and will not
  ask for. Rootless is the target, not an afterthought.
- **An in-container reconciler** re-binding the view from a hidden full mount.
  Needs `CAP_SYS_ADMIN`, and worse, needs the *complete* tree present inside
  the container for the reconciler to bind from — which is the wall, gone.
- **Masking excluded paths over a full mount** (tmpfs over what must be
  hidden). Already rejected in `view-mounts-design.md` and rejected harder
  here: the mask list would itself have to track the host, and anything new
  that nobody masked yet is visible. Fail-open.

## The one shape that works: a host-side FUSE view

A per-sandbox daemon on the host serves `<stateDir>/_view/<slug>/`, presenting
exactly the include-matched tree, recomputed per lookup. The container gets
**one static bind mount** of that directory.

What it buys, all from the same property — the view is resolved by *path*, per
operation, rather than captured once as a set of inodes:

- a new matching directory is visible immediately, in the running container;
- a recreated directory is followed, because nothing is pinned to an inode;
- the mount set stops moving, so `MountGen` stops flipping and a narrowed
  session stops going stale from ordinary host activity at all. D3a's diff
  would have almost nothing left to report.

## What it costs — the reason this is not obviously right

- **The wall changes kind.** Today it is "the kernel was never asked to mount
  that path" — fail-closed, and nothing sandboxer runs can weaken it. With
  FUSE it becomes "a daemon answered this lookup correctly", every time, for
  every path an agent can construct. A pattern-matching bug is then a leak,
  not a missing mount. This is the objection to beat, and it is not small:
  `proc-security-posture` asks for fail-closed, and an unmounted path is the
  strongest form of that available.
- **A daemon per sandbox**, outliving the CLI process that started it, needing
  start/stop wired into create/enter/stop/clean and needing to survive a
  crash — with `ENOTCONN` inside the container as a brand-new failure mode
  that looks like nothing users see today.
- **A hard host dependency on `/dev/fuse`**, plus rootless-podman specifics
  around bind-mounting a FUSE mountpoint into a container (the mount must
  pre-exist and be visible to the engine's namespace).
- **Latency on every path operation** in the agent's hot loop — builds and
  language servers walk large trees, and a userspace round trip per lookup is
  not free even with attribute caching.

## Recommendation

Not now. D3a makes the current model honest and gives the user a way to apply
drift on the spot, which covers the reported pain at a fraction of the risk.
Revisit if the drift diagnosis turns out to fire often enough in real use that
"rebuild to see your directory" becomes the routine answer rather than the
exception — that, and not elegance, is the signal that the snapshot model has
stopped paying for itself.

Before any implementation: a spike that measures the lookup overhead on a
realistic tree and demonstrates the wall holding under an agent actively trying
to walk out of it, on **both** engines. Same bar `view-mounts-design.md` set
for the current design, which was measured before it was written.
