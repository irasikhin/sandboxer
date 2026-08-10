# microVM backend — Phase 0 spike report

> **Historical record — predates the msb-only migration** (the container backend and smolvm were since removed; microsandbox is the only backend).

**Verdict: GO on the Linux leg.** Every load-bearing assumption from
`plans/modular-questing-codd.md` held on a real KVM host. macOS and Windows legs
are deferred until hardware is available (their checks are duplicated into
`docs/e2e-checklist.md`); GO/NO-GO for the plan is taken on the Linux leg alone.

## Environment

- Host: NixOS, kernel 6.12.63, x86_64, btrfs, `/dev/kvm` = `crw-rw-rw-` (all users), 27 GiB RAM.
- `smolvm` **v1.6.13** — official `linux-x86_64` release tarball; sha256
  `9e6fec35254264fdd15c4b927c8ee97104cbee201956d3f6903a4b8c4c393956` (matches the
  release `checksums.sha256`). NOT in nixpkgs; its own flake's pinned hash was already
  stale at v1.6.13 (`hash mismatch`), so the release binary was used directly.
- **NixOS install note:** the tarball is a generic dynamically-linked ELF and will
  not run out of the box (`stub-ld`). It needs `patchelf --set-interpreter` + an rpath
  to a glibc/libgcc closure (bundled `lib/libkrun.so.2`, `lib/libkrunfw.so.5`), or
  `nix-ld`. This confirms plan risk #2's NixOS friction and belongs in the `doctor` hint.
- The distribution ships a `smolvm` wrapper (sets `LD_LIBRARY_PATH`/`SMOLVM_AGENT_ROOTFS`)
  around `smolvm-bin`; the wrapper is the supported entrypoint — sandboxer should shell
  out to `smolvm`, not `smolvm-bin`.

## Results against the checklist

### 1. virtio-fs escape — BLOCKER #1 → **wall holds**

One include dir mounted `-v H:H`. From the guest (running as VM-root, uid 0):

| Attack | Result |
|---|---|
| read mounted file | ✅ works (`SHARED-OK`) |
| sibling dir by absolute host path | ❌ `No such file or directory` |
| parent-secret by absolute host path | ❌ blocked |
| `share/../sibling/secret` dot-dot traverse | ❌ blocked |
| mounting a path with a parent component | ❌ smolvm refuses at launch: `mount destination: parent traversal is not allowed` |
| host-made symlink `share/link → sibling` | ❌ dangles (target resolved in guest ns) |
| host-made symlink `share/link → /etc/shadow` | reads the **guest's** `/etc/shadow` (`root:*::…`), not the host's |
| guest-made symlink → host sibling path | ❌ dangles |
| guest-made symlink → `/etc` | resolves to the **guest** `/etc` |

The virtiofs mount is rooted at the share; there is no path, dot-dot, or symlink route
to another host directory. `open_by_handle_at` is not a practical vector: the fh is
server-generated and opaque, so a sibling inode cannot be forged. **`include`-narrowing
is safe on this backend** — the mount set is the wall, exactly as with containers.

### 2. uid / ownership → **better than podman**

Files and dirs written from the guest land on the host owned by the **invoking host user**
(`ir:users`, mode 0644/0755) — not root. No `--userns=keep-id` analogue is needed, and the
macOS `SANDBOXER_CONTAINER_USER` escape hatch has no reason to exist here. (macOS/HVF
ownership still to be checked on hardware.)

### 3. exec semantics → **all pass**

- Exit codes propagate: `run … -- sh -c 'exit 7'` → host rc 7; `exit 0` → 0.
- `-i` stdin pipe: `echo X | exec -i -- cat` round-trips.
- `-i -t` TTY: guest sees `/dev/pts/0`, `test -t 0/1` both true — interactive tmux enter viable.
- `-w/--workdir` **exists** on both `run` and `exec` — the planned `cd DEST && exec …`
  wrapper is unnecessary; workdir is a native flag.
- `-e KEY=VALUE` works.
- `--secret-env GUEST=HOST` works; the value is **absent from host `ps` args, from every
  `/proc/*/cmdline`, and from the persisted machine record**. It is present only in the
  environ of the transient host-side `smolvm` process (that is how the value is passed over
  vsock; environ is readable only by the owner uid) — acceptable, and the AuthEnv channel.

### 4. persistence & lifecycle → **as needed**

- Named machine `create`→`start`→`exec`→`stop`→`start`→`exec`: the `-v` mount and
  guest-written files survive stop/start.
- A machine boots to a bare agent and stays alive with an **empty workload cmd** — no
  `sleep infinity` keepalive needed (contrast the container `run -d … sleep infinity`).
- `machine ls --json` is scriptable: emits `name, state, cpus, memory_mib, mounts (count),
  ports, pid, image, network, restart_*, created_at`. **No labels and no mount paths** →
  confirms the plan's `_meta/<slug>.vm.json` sidecar + global machine registry for
  hash/mountIDs/baseDir. `machine delete` prompts unless `-f/--force` (needed for
  non-interactive `RemoveSession`).
- Machine name alphabet: `spike` accepted; matches `SessionName`'s charset.

### 5. networking → **fail-closed allowlist confirmed**

- **Net off by default:** with no `--net`/`--allow-host`, DNS fails (`wget: bad address`) —
  no route at all. Empty allowlist = fully offline machine (valid state for microvm;
  contrast container `errEmptyAllowlist`).
- `--allow-host example.com`: `example.com` fetches (`<title>Example Domain`), while a
  non-listed domain (`cloudflare.com`) fails at DNS (`bad address`). Allowlist is by
  hostname, resolved at VM start — maps onto `egress.allowedDomains`.
- Backends: `tsi` (libkrun TSI) and `virtio-net`; also `--allow-cidr`, `--dns`,
  `--outbound-localhost-only`, and image-pull `--proxy`/`--no-proxy`.

### 6. image → docker-save tar consumed directly

`docker save alpine -o alpine.tar` boots via `-I alpine.tar` with no engine in the path.
Same format as the toolbox `dockerTools.buildLayeredImage` output. Note `--max-image-size`
default is **8 GiB** — the toolbox image is larger than alpine, so the builder/consumer must
raise it (`--max-image-size` / `SMOLVM_MAX_IMAGE_BYTES`). Full `nixos/nix` build-in-VM is
deferred to the itest (needs network + a long nix build).

### 7. speed → **warm exec beats docker; boot is sub-second**

Median of 5 (first run dropped), same host:

| op | smolvm | docker |
|---|---|---|
| **warm exec** (`exec … true`) | **26 ms** | 65 ms |
| cold ephemeral (`run … true`) | 732 ms | 528 ms |

Warm `exec` — the sandboxer hot path (enter/exec into a persistent session) — is
**2.5× faster than `docker exec`**. Cold VM boot is ~1.4× a docker cold run but still
< 0.8 s. With persistent sessions as the default, the warm number dominates. "Максимально
быстрые" satisfied for the interactive path.

**virtio-fs write caveat (real, not a blocker):** the share mounts `rw,sync,relatime` — every
write is synchronous so the host sees edits immediately (the liveness property sandboxer wants).
Cost: creating 2000 small files through virtiofs took **~15 s (~7.5 ms/file)** vs instant on the
guest-local overlay; a 256 MB bulk write took ~4 s. **Implication for the plan:** source edits
(a few files) are fine, but heavy in-tree build churn (`go build`, `npm install`) is slow on the
share. Mitigation to bake into the toolbox/profile: keep build caches (`GOCACHE`, `node_modules`,
etc.) on guest-local storage, not the mounted worktree.

## Deltas to fold into the plan

1. **`-w/--workdir` is native** → drop the `cd DEST && exec` wrapper from PR 2; pass `-w`.
2. **`--max-image-size`** must be raised for the toolbox tar (PR 2 consume + PR 5 build).
3. **`machine delete -f`** is required for non-interactive teardown (PR 3).
4. **No `sleep infinity`** keepalive — empty workload cmd suffices (PR 2/3).
5. **Build-cache-off-the-share** guidance → toolbox/profile note (PR 5 / docs).
6. **NixOS patchelf/nix-ld** friction is real → `doctor` install hint (PR 6).
7. `--secret-env` is the AuthEnv channel and is clean — matches PR 2's design.

None of these change the architecture; they simplify PR 2 (native workdir, no keepalive) and
add three flags. **Proceeding to Phase 1.**
