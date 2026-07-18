# View mounts — design

Status: **implemented** — `include` narrows the container's MOUNT SET instead of
the host worktree's contents; the worktree is always a complete checkout.

This is a decisions-and-why record (an RFC), not a mirror of code
(`proc-doc-as-code`): it captures the mechanism choices, the alternatives
rejected and the measurements that killed them.

## The problem

`include` used to narrow the source worktree itself, via non-cone
`git sparse-checkout`. The narrowed tree was the containment boundary: the
container got one bind mount of `<slug>/`, and the excluded files were simply
not on disk, so there was nothing to leak.

That conflated two things which turn out to be separate:

- what the **agent** may touch, and
- what **you** have on disk.

They are the same directory, and an IDE sits on the disk side. A narrowed
sandbox's worktree is not a workable project: the IDE cannot resolve imports,
index, or build, because most of the repo is missing. Nor could you open the
branch anywhere else — git allows one worktree per branch, and the sandbox holds
it, so the main checkout cannot check it out either. A frequent narrowing user
was left with no way to look at their own branch.

## Chosen shape: full worktree on the host, per-directory mounts in the container

The worktree is always checked out whole. `include` becomes a list of
directories, and narrowing is enforced by what gets bind-mounted:

| `include` | `<slug>/` root mount | source mounts |
|---|---|---|
| absent (or `["**"]`) | **mounted rw** — one stable live window | adopted worktrees only |
| present | **not mounted** | one rw mount per listed directory, at its own path |

`sandbox.Mounts` decides both together and is the only place that may. When the
sandbox is narrowed, the engine materializes the unmounted `<slug>/…` parents as
empty directories in the container's own layer, so the container's view is
structurally identical to the old sparse tree — at the same paths.

Not mounting the root IS the boundary. The excluded files are on the host, one
directory above the mounted ones; nothing else hides them. `RunOpts.MountDest`
carries the decision into the argv, and two tests pin it — one on the argv
(`TestRunArgvNarrowedNeverMountsDest`), one on a real engine
(`TestRun_RealEngine_SrcsWall`). Both were confirmed to fail when the invariant
is broken (a mutation forcing the root mount trips the argv test, and trips the
engine test with "WALL BREACHED: serviceB visible").

## Why `include` is directories only

A mount names a path, so a pattern must resolve to exactly one directory.
gitignore semantics cannot survive the move:

- a **glob** (`*.md`, `**/x`) or a **negation** (`!/vendor/`) selects a file
  *set* only a matcher can evaluate. Expanding it means one mount per matched
  file, and a file-granular bind mount **breaks atomic saves**: write-temp +
  rename over the mountpoint fails with `EBUSY`, which is how editors and agents
  write files.
- a **bare file** (`/go.mod`) hits the same problem — name its directory.

So `ValidateInclude` refuses those shapes with the directory form to use
instead, and refuses them at `config validate` time. `resolveSrcs` validates too,
because `create` materializes the sandbox *before* it resolves the runtime, so
the config-level check alone would fire too late.

An include naming a path that is not a directory on the branch is a hard error,
not a warning: an engine asked to bind-mount a missing source **creates it**,
root-owned, inside the user's worktree.

`checkViewDirs` also resolves each include's real path and refuses one that
escapes the worktree via a **symlink**. This is a genuine exposure the old
sparse model did not have: the engine resolves a bind-mount source on the host,
so `include = ["/services/"]` where a checked-in `services -> /etc` symlink
exists would mount the host's `/etc` past the wall (a lexical prefix check does
not catch it — the symlink's own path is inside the worktree). Symlinks *inside*
the mounted content are left alone; the container resolves those in its own
namespace.

## Alternatives rejected

**Symlinks** (full worktree, a tree of symlinks mounted into the container).
Impossible, not merely awkward: a symlink is resolved by the kernel in the
*container's* mount namespace. Either the target is unmounted (dangling — the
agent sees nothing) or it is mounted (the agent walks the absolute path and the
wall is gone). There is no third case. The codebase already depends on this
property in the other direction: a worktree's `.git` file points at an unmounted
host path and is dead inside the container **on purpose**.

**File permissions / ACLs** (deny-all, grant the exposed subset). Dead on
arrival: `containerUserArgs` runs the container as the invoking host uid, which
*owns* the worktree files, and DAC is discretionary — an owner can always undo a
mode. `--cap-drop=ALL` does not help, since `CAP_FOWNER` is only needed to chmod
files you do not own. Making permissions real would mean running the agent as a
different uid, which is exactly what the host-uid mapping exists to avoid (the
mounts must stay writable), and would then lock the IDE out of the exposed
subset — trading the original problem for itself. Permissions also leak
structure (a `000` file still has a name in a listing, where an unmounted path
has no existence) and drift open with every host-side git operation. SELinux /
AppArmor would be genuinely mandatory rather than discretionary, but that is a
hard host dependency sandboxer does not have.

**Masking excluded paths over a full mount** (tmpfs over what must be hidden).
Fail-open: anything new on the host that is not in the mask list is visible.
`proc-security-posture` is fail-closed; an unmounted path is.

**A read-only root to make out-of-view writes fail loudly.** Designed, then
found unnecessary — see below. It would also have needed a host-side skeleton of
pre-created mountpoints, since mountpoints cannot be created inside a read-only
tmpfs.

## What was measured, not assumed

Verified against a real engine before the implementation was written:

- Mounting only `<wt>/src/proto` with `<wt>` unmounted gives exactly the narrow
  view; the excluded paths do not exist; the engine creates the intermediate
  directories itself.
- A write **outside** the view fails with `Permission denied` — no ro-root
  machinery required. The engine creates the unmounted parents root-owned, and
  the container runs as the host uid. Silent loss into the ephemeral layer,
  which the design had treated as its main cost, does not happen.
- With `SANDBOXER_CONTAINER_USER=""` (the macOS escape hatch: no `--user`, so
  the container is root) that write **succeeds** and is silently lost — but the
  wall itself still holds, the excluded paths remain invisible. Documented in
  SECURITY.md rather than guarded in code.

The whole integration suite was run on a real docker engine, including the three
wall tests. Note that CI does **not** run it (`ci.yml` has no `-tags
integration`; only the Jenkins job does), so the engine-level guarantee is not
gated by the GitHub build.

## Costs accepted

- **`include` loses gitignore expressiveness.** Directories only. Every
  documented example was already directory-shaped.
- **The mount list is in the argv**, so it is part of the session `ConfigHash`:
  changing `include` rebuilds the session. Correct, and cheap.
- **A bind mount is pinned to an inode.** If a host-side `git checkout` or build
  removes and recreates a mounted directory, a live session keeps the old inode
  and silently diverges. `DestGen` solves this for the `<slug>/` root (which is
  inode-stable anyway — a git op inside it recreates children, never the mounted
  dir). The individual mounts one level in — a narrowed sandbox's view dirs and
  any adopted worktree — are exactly what a checkout can recreate, so they get
  the same treatment: `MountFingerprint` folds their device+inode into
  `RunOpts.MountGen`, a `SANDBOXER_MOUNT_GEN` env var that enters the session
  `ConfigHash`. A recreate flips the fingerprint, flips the hash, and the next
  enter rebuilds against the fresh directory. Empty for a sandbox with no
  individual mounts (the common one-managed-source case), so that argv — and its
  session hash — is unchanged and nothing rebuilds on upgrade. The orphaning
  hazard itself is demonstrated on a real engine
  (`TestRun_RealEngine_OrphanedMountIsCaught`): a detached container keeps
  reading the orphaned inode while the host has moved on, and the fingerprint
  changes across the recreate.

## Verified on both engines

The wall, the out-of-view write refusal, the unnarrowed whole-repo mount and the
orphaned-mount hazard all pass on **docker** and on **rootless podman** (the
engine sandboxer prefers). Notably the "a write outside the view fails" property
holds on rootless podman too, under its `--userns=keep-id` mapping — not only on
docker. The one integration test that fails locally on podman,
`TestRunArgv_RealEngineAccepts`, does so only because it runs podman under an
empty `$HOME` with no `policy.json`/`registries.conf`; that is a podman host-config
prerequisite (present at `/etc/containers` on a real CI host), not a property of
this change — the argv it generates is byte-identical to before.

## Migration

Automatic and in place. A worktree narrowed by an older sandboxer is widened on
the next sync (`worktree.Unsparse` disables sparse-checkout), keeping the branch
and any uncommitted work — no recreate. A config carrying a glob or negation
starts failing `config validate` with the directory form to use.
