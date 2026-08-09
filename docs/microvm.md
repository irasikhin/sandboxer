# The microVM backends (smolvm · microsandbox)

sandboxer can isolate an agent in a real **microVM** instead of a container.
Where a container shares the host kernel and leans on `--cap-drop`, user
namespaces and the mount set for its boundary, a microVM gives each sandbox its
**own kernel** behind a hardware virtualization boundary — the isolation an LLM
agent running untrusted code deserves.

Two runners are supported, both thin CLIs over **libkrun** (KVM on Linux,
Hypervisor.framework on macOS/Apple Silicon), shelled out to exactly as the
container backend shells out to docker/podman:

| backend value | runner | binary |
|---|---|---|
| `microvm` | [smolvm](https://smolmachines.com) | `smolvm` (`SANDBOXER_SMOLVM`) |
| `microsandbox` | [microsandbox](https://microsandbox.dev) | `msb` (`SANDBOXER_MSB`) |

The isolation primitive is identical, so the containment wall, the uid mapping
and the session lifecycle behave the same on both. They differ in what the
runner's CLI can express — see [Choosing a runner](#choosing-a-runner).

## Status

Experimental, opt-in. The container backend remains the default; select a
microVM backend per profile or per run:

```nix
# sandboxer.nix
{ backend = "microvm"; srcs = [ { src = "."; branch = "feat/x"; } ]; }
# or
{ backend = "microsandbox"; srcs = [ { src = "."; branch = "feat/x"; } ]; }
```

```console
$ sandboxer enter mybox --backend microsandbox
$ SANDBOXER_BACKEND=microsandbox sandboxer enter mybox
```

> **Known breakage — prefer `microsandbox` today.** On smolvm 1.6.13 the
> `microvm` runner cannot boot the stock profile: three virtio-fs shares (the
> ones sandboxer always creates) plus `--allow-host` plus a large image is a
> configuration libkrun rejects with `krun_start_enter returned: -22 (EINVAL)`,
> so every `enter`/`exec` fails while the allowlist is on. `microsandbox` takes
> the identical profile and boots. Workarounds, minimal reproducer and the exact
> versions are in
> [troubleshooting.md](troubleshooting.md#microvm-smolvm-krun_start_enter-returned--22-einval-on-every-enter).

## Choosing a runner

| | `microvm` (smolvm) | `microsandbox` (msb) |
|---|---|---|
| allowlist grammar | name-bound suffix (`--allow-host`) — a leading dot is stripped, but the rule still covers the domain *and* its subdomains | **name-bound suffix rules** — same coverage, and additionally per protocol and port, like the squid sidecar |
| unresolvable allowlist entry | hard-fails the machine at boot (sandboxer drops such entries with a warning) | fine — rules match at connect time |
| raw-IP bypass of an allowed domain | possible | refused (rules are name-bound) |
| image handling | a docker-save tar, re-imported on every cold create | imported once into msb's image store, then boot-only |
| credentials | `--secret-env` reference (value never in argv) | `--env` per exec by default; opt-in host-scoped `--secret` |
| macOS / Windows builds | Linux + macOS | Linux, macOS **and** Windows |
| maturity here | shipped and soaked first; the default microVM runner | newer adapter, same lifecycle code |

Measured comparison (boot/exec latency, share I/O, the escape matrix):
[microsandbox-spike.md](microsandbox-spike.md) and [microvm-spike.md](microvm-spike.md).

## Migrating to `microsandbox` (decommissioning docker/podman)

`microsandbox` is the runner that can fully replace the container backend: it
has the lossless name-bound egress allowlist (no unresolvable-domain drop, raw
IPs refused), a real image store, labels, and it boots the stock profile that
smolvm 1.6.13 rejects with EINVAL. The container backend stays supported, but
the switch is a per-profile config line:

```nix
{ backend = "microsandbox"; srcs = [ { src = "."; branch = "feat/x"; } ]; }
```

What carries over **unchanged** (backend-agnostic, no action needed): srcs
worktrees and `include` narrowing, remote srcs, the per-sandbox home and
`hostConfigs` seeding, profile.json, session hashing/staleness, tmux
capture/restore and agent auto-resume, resource limits (`memory`/`cpus`), and
the auth-env channel. A session created under a container engine keeps its
deterministic name (`SessionName` is engine-independent), so switching the
profile recreates the machine with that same name while `clean`/`rm` sweep the
old container on every engine — nothing strands.

What **drops**, and how it surfaces:

| feature | on `microsandbox` |
|---|---|
| `egress.routes` | hard config error (no squid `cache_peer` analogue) |
| `limits.pids` | ignored with a warning |
| `nestedContainers` | ignored — a real VM runs container engines natively (row 13 partially passing on Linux/KVM) |
| `extraMounts` pointing at a regular **file** | hard error: virtio-fs shares directories only |
| `sandboxer compose` | hard error (container-only) |

**Image build without docker/podman.** The toolbox image is built with **host
nix** — nix is already a hard requirement of the CLI (it evaluates
`sandboxer.nix` on every invocation), so there is no builder container and no
builder guest:

```console
$ sandboxer image build --backend microsandbox          # or: microsandbox <profile>
```

The build context is the same embedded flake the container builder used
(`path:<ctx>#image`); the realized tar goes straight into the microVM store both
runners boot from. The "latest" input-rev pins resolve on the **host via `git
ls-remote`** (git is also a hard requirement), so a **cold pins cache on a
docker-less host resolves exactly like a warm one** — the first-ever build on a
machine with no docker/podman just works. `sandboxer image build --backend
microvm` behaves identically (host nix is runner-agnostic), and `nix run
.#build-image` also places the tar into the microVM store.

**Verifying the move:** `sandboxer doctor` reports the container engine row as
*info* on a VM-only host (and checks the image in the microVM store there),
warns per profile when its backend cannot run here — the signal that a profile
still names docker/podman after removal — and `doctor --strict` can be green on
an msb-only host.

## Requirements

The runner is a **preinstalled host requirement** (like `nix-instantiate`,
docker or podman) — sandboxer never downloads or vendors it, and never silently
falls back to a container engine, which would quietly weaken isolation. An
absent runner is a clear error with an install hint.

- **Linux:** `/dev/kvm` must exist and be accessible (typically the `kvm`
  group). `sandboxer doctor` reports both runners.
- **macOS:** macOS 11+ on **Apple Silicon** (Intel Macs are not a target).
- Install smolvm from <https://smolmachines.com> and microsandbox from
  <https://microsandbox.dev>; `SANDBOXER_SMOLVM` / `SANDBOXER_MSB` override the
  looked-up binary.
- **NixOS:** both upstream releases are generic dynamically-linked ELFs and will
  not run out of the box (stub-ld), so this repo packages both:

  ```console
  $ nix run github:irasikhin/sandboxer#smolvm -- --version
  $ nix run github:irasikhin/sandboxer#microsandbox -- doctor
  # or drop into the devShell (both are on PATH there): nix develop
  ```

  To install system-wide, add the flake's `smolvm` / `microsandbox` package (via
  `overlays.default`, which exposes `pkgs.smolvm` and `pkgs.microsandbox`) to
  `environment.systemPackages`. Neither is in nixpkgs yet.

Two microsandbox-specific host constraints, both reported by `doctor` or
rejected up front rather than surfacing as a mysterious failure:

- **`MSB_HOME` must be short.** Every sandbox's agent-relay UNIX socket path
  derives from it and must fit in the kernel's 108-byte `sun_path`. The default
  (`~/.msb`) is fine; a deep override makes every `create` fail. sandboxer never
  points `MSB_HOME` at its own (long) per-project state dir.
- **Nothing under `/tmp` can be shared.** The guest mounts a tmpfs over `/tmp`
  *after* the host shares, so a share below it is invisible inside. sandboxer's
  own paths are the project's `./sandboxes` and the XDG state dir; a profile that
  deliberately points `worktreesDir` (or an `extraMounts` target) under `/tmp` is
  refused with that explanation.

Check everything at once:

```console
$ sandboxer doctor
microvm (smolvm)     ✓  smolvm 1.6.13 available
microsandbox (msb)   ✓  msb 0.6.7 available
```

## How it maps to the container backend

| container | smolvm | microsandbox |
|---|---|---|
| `podman/docker run … IMAGE` | `smolvm machine run/create/exec …` | `msb run/create/exec …` |
| bind mount `-v H:H:rw` | virtio-fs share `-v H:H` (directories only) | virtio-fs share `-v H:H` |
| `--cap-drop=ALL`, `--userns=keep-id`, seccomp | subsumed by the VM boundary (absent) | ″ |
| squid sidecar + allowlist | `--allow-host` (no sidecar) | `--net-rule` policy engine (no sidecar) |
| toolbox image in the engine store | a docker-save tar in `<state>/images/` | that same tar, imported into msb's image store |
| image built in a `nixos/nix` **container** | image built in a `nixos/nix` **microVM** | built with a container engine when present, else in a microVM |

Session identity lives in a host-side record at `<state>/machines/<name>.json`
(smolvm) or `<state>/machines/microsandbox/<name>.json` — one mechanism for both
runners, so a project that switches runners keeps two independent records and
never strands the other's machine. microsandbox additionally stamps the same
identity as engine labels (`msb list --label sandboxer.managed=true`), which the
container backend keeps in engine labels too. tmux capture/restore, agent
auto-resume, the mount-drift rebuild and the whole enter/exec orchestration are
shared with the container path verbatim.

Guest writes land on the host owned by **you** (no `--userns=keep-id` needed),
and a narrowed sandbox (`include`) shares only its listed directories — a
sibling worktree on the same host filesystem is unreachable inside the guest,
the same wall the container backend enforces.

## Building the image

The first `enter` builds the toolbox image automatically. To build it ahead of
time (or rebuild after a config change), the backend is taken from the profile,
or an explicit flag:

```console
$ sandboxer image build --backend microvm              # stock image → the tar store
$ sandboxer image build --backend microsandbox         # …and imported into msb's store
$ sandboxer image build --backend microsandbox <prof>  # a profile's variant
$ sandboxer image rm --backend microsandbox            # drop the stored image
```

Both runners share ONE build artifact: `<state>/images/<tag>.tar`. The image is
built with **host nix** — the same `path:<ctx>#image` derivation the container
backend realizes in an ephemeral `nixos/nix` builder — so no builder container,
no builder guest, and no container engine anywhere in the path (nix and git are
both already hard requirements of the CLI). The tar boots under either runner;
for `microsandbox` it is then imported (`msb load`) into its image store, which
is what its `create` reads — so `image rm --backend microsandbox` drops both the
cached image and the tar. A container-built *image in a container engine's own
store* is never reused here. `--engine` is container-only and ignored.

## Egress

Egress is a create-time policy folded into the session hash (changing it
recreates the machine). There is no squid sidecar on either runner.

**smolvm** has no network route by default. The modes, in precedence order:

1. **`egress.proxy` set → proxy-delegated egress.** The network is opened and the
   guest's HTTP(S) clients are pointed at the proxy
   (`HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` env, `egress.noProxy` → `NO_PROXY`).
   The proxy IS the egress control point — the microVM analogue of the
   container's direct mode. Unlike containers, **`localhost` is not rewritten**:
   smolvm's TSI reaches a host-local proxy transparently (verified — a guest
   with `--net` reaches the host's `127.0.0.1`), so `http://127.0.0.1:3128`
   works. When `allowedDomains` is ALSO set the two **coexist** (the common
   homelab case): the proxy forwards, and the allowlist is enforced by the proxy
   rather than at the VM network layer — smolvm cannot both filter at the network
   layer and reach a host-local proxy (that needs the open TSI network). This is
   surfaced as a warning at enter/exec, not an error; ensure your proxy restricts
   egress to the intended allowlist.
2. `egress.enabled = false` (or `SANDBOXER_NO_EGRESS=1`), no proxy → open network.
3. `egress.allowedDomains` non-empty → `--allow-host` per domain, fail-closed
   (only those hosts resolve). A leading dot (`.example.com`, the container's
   subdomain grammar) is stripped so the name resolves, but this does **not**
   narrow the rule: measured on smolvm 1.6.13, `--allow-host` matches the host
   and its subdomains, the same coverage squid's leading-dot `dstdomain` and
   microsandbox's `*.domain` give. An entry that does not resolve on the host
   would hard-fail the machine at boot, so such entries are dropped with a
   warning.
4. egress on with an **empty** allowlist → a fully offline machine (valid here —
   with no default route, "allow nothing" simply reaches nothing).

**microsandbox** defaults to an open network, and the same four states map onto
its policy engine:

1. `egress.proxy` set → the same proxy-delegated mode (open network + proxy env).
2. egress off → open network, no rules.
3. an allowlist → `--no-net` (default deny) plus, per domain, an allow rule for
   HTTP and HTTPS: `allow@*.domain:tcp:80,allow@*.domain:tcp:443`. This is the
   **lossless** translation of the squid sidecar's leading-dot `dstdomain`: the
   domain and its subdomains, those two ports, DNS via the gateway, and nothing
   else. Rules are matched by NAME, so a raw IP — even the allowed domain's own —
   is refused.
4. an empty allowlist → `--no-net` alone: fully offline, DNS included.

`egress.routes` (per-domain upstream proxies) is unsupported on both.

## Credentials

Auth env (`hostConfigs` + the agents' `authEnv`) never enters the long-lived
machine's configuration on any backend, so it is not part of the session hash and
a rotated token is picked up by the next shell with no rebuild.

- **smolvm**: `--secret-env KEY=KEY` — a reference resolved from sandboxer's own
  process environment, so the value is never in argv (`ps`) nor in the machine
  record.
- **microsandbox**: `--env KEY=value` per exec, the same channel (and the same
  exposure) as the container backend.
- **microsandbox, opt-in**: `SANDBOXER_MSB_SECRETS=1` switches to msb's
  host-scoped `--secret KEY@host,host…` (with `--on-secret-violation
  block-and-log`), scoped to `egress.allowedDomains`. The value then never enters
  the guest at all — it gets a stand-in — and cannot be sent to a host outside the
  list. Two caveats keep it opt-in: the substitution direction (does the *real*
  value reach an allowed host?) is unverified upstream, so a wrong assumption
  silently breaks agent authentication; and the value is bound at boot, so a
  rotation needs a machine restart. Without an allowlist to scope to, the mode
  degrades to the default.

## What the microVM backends do not support

Rejected or ignored under `backend = microvm` / `microsandbox`:

- `sandboxer compose` — errors (it emits a docker-compose file and a
  podman/docker run argv with no VM equivalent).
- `egress.routes` — config error (per-domain upstream proxies are a squid
  cache_peer feature with no analogue in either runner).
- `limits.pids` — ignored with a warning.
- a **fractional** `limits.cpus`, or an **unparseable** `limits.memory` — error:
  the runners take a whole number of vCPUs and a parseable size, and a silent
  rounding / 4 GiB fallback is worse than a clear message.
- `nestedContainers` — ignored (a real VM runs container engines natively; the
  toolbox image's docker/podman/compose run inside the guest without the outer
  seccomp/userns dance). **Measured, not asserted — scoped to what was run:** on
  Linux/KVM an msb guest maps the whole uid range natively, and podman inside it
  runs plain images and a user-switching postgres SERVICE (entrypoint drops to
  uid 70, postgres answers `select 42`) with no `/etc/subuid` grant, no
  capability grant and no seccomp widening — measured with
  `quay.io/podman/stable` as the guest image (the toolbox build OOMs on the test
  host). The toolbox image's OWN engine stack (storage.conf, policy.json,
  registries.conf, the pre-created `/var/tmp`) still needs one run, and macOS /
  Windows/WSL2 still need their hardware runs (phase C). See
  [e2e-checklist.md](e2e-checklist.md) and the gated
  `TestMSB_NestedContainer_RealEngine` integration test.
- an `extraMounts` whose `source` is a regular **file** — error: virtio-fs
  shares directory trees only, so a docker-style single-file bind mount has no
  share analogue; mount a directory that holds the file instead.

Default machine size is **2 vCPU / 4 GiB** (smaller than either runner's own
default, for the parallel-agent workload); raise it with `limits.memory` /
`limits.cpus`.

## Windows

There is no native Windows build of sandboxer. Run it **inside WSL2**, where the
Linux binary and a Linux runner use nested KVM (`.wslconfig` with
`nestedVirtualization=true`). See [windows.md](windows.md).

## Performance

Warm `exec` into a persistent session is ~26 ms on smolvm and ~16 ms on
microsandbox (both faster than `docker exec`); cold boot is sub-second. smolvm
mounts its virtio-fs share synchronously, so heavy in-tree build churn
(`go build`, `npm install` writing thousands of files) is much slower than a
container bind mount — keep build caches (`GOCACHE`, `node_modules`) on
guest-local storage. microsandbox mounts it `relatime`, roughly 18× faster on
small-file churn while staying live in both directions. Measured numbers:
[microvm-spike.md](microvm-spike.md), [microsandbox-spike.md](microsandbox-spike.md).
