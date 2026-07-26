# The microVM backend (smolvm)

sandboxer can isolate an agent in a real **microVM** instead of a container.
Where a container shares the host kernel and leans on `--cap-drop`, user
namespaces and the mount set for its boundary, a microVM gives each sandbox its
**own kernel** behind a hardware virtualization boundary — the isolation an LLM
agent running untrusted code deserves.

The backend shells out to [`smolvm`](https://smolmachines.com) (which wraps
libkrun: KVM on Linux, Hypervisor.framework on macOS/Apple Silicon), exactly as
the container backend shells out to docker/podman.

## Status

Experimental, opt-in. The container backend remains the default; select the
microVM backend per profile or per run:

```nix
# sandboxer.nix
{ backend = "microvm"; srcs = [ { src = "."; branch = "feat/x"; } ]; }
```

```console
$ sandboxer enter mybox --backend microvm
$ SANDBOXER_BACKEND=microvm sandboxer enter mybox
```

## Requirements

smolvm is a **preinstalled host requirement** (like `nix-instantiate`, docker or
podman) — sandboxer never downloads or vendors it, and never silently falls back
to a container engine, which would quietly weaken isolation. An absent smolvm is
a clear error with an install hint.

- **Linux:** `/dev/kvm` must exist and be accessible (typically the `kvm`
  group). `sandboxer doctor` reports both.
- **macOS:** macOS 11+ on **Apple Silicon** (Intel Macs are not a target).
- Install from <https://smolmachines.com>; `SANDBOXER_SMOLVM=/path/to/smolvm`
  overrides the looked-up binary.
- **NixOS:** the upstream release binary is a generic dynamically-linked ELF and
  will not run out of the box (stub-ld), so this repo ships a packaged smolvm
  (autoPatchelf'd, sparse disk templates preserved). Use it directly:

  ```console
  $ nix run github:irasikhin/sandboxer#smolvm -- --version
  # or in a checkout: nix run .#smolvm -- machine ls
  # or drop into the devShell (smolvm is on PATH there): nix develop
  ```

  To install it system-wide, add the flake's `smolvm` package (via
  `overlays.default`, which exposes `pkgs.smolvm`) to
  `environment.systemPackages`. Alternatively, run the official install and rely
  on `programs.nix-ld.enable = true` (with `programs.nix-ld.libraries = [
  pkgs.stdenv.cc.cc.lib ]`). smolvm is not yet in nixpkgs.

Check everything at once:

```console
$ sandboxer doctor
microvm (smolvm)   ✓  smolvm 1.6.13 available
```

## How it maps to the container backend

| container | microVM |
|---|---|
| `podman/docker run … IMAGE` | `smolvm machine run/create/exec …` |
| bind mount `-v H:H:rw` | virtio-fs share `-v H:H` (directories only) |
| `--cap-drop=ALL`, `--userns=keep-id`, seccomp | subsumed by the VM boundary (absent) |
| squid sidecar + allowlist | smolvm's built-in `--allow-host` (no sidecar) |
| toolbox image in the engine store | a docker-save tar in `<state>/images/` |
| image built in a `nixos/nix` **container** | image built in a `nixos/nix` **microVM** |

Session identity: smolvm machines carry no labels, so each session's config
hash, image id and mount fingerprint live in a host-side record at
`<state>/machines/<name>.json` (the container backend keeps these in engine
labels). tmux capture/restore, agent auto-resume, the mount-drift rebuild and
the whole enter/exec orchestration are shared with the container path verbatim.

Guest writes land on the host owned by **you** (no `--userns=keep-id` needed),
and a narrowed sandbox (`include`) shares only its listed directories — a
sibling worktree on the same host filesystem is unreachable inside the guest,
the same wall the container backend enforces.

## Building the image

The first `enter` builds the toolbox image in a microVM automatically. To build
it ahead of time (or rebuild after a config change), the backend is taken from
the profile, or an explicit flag:

```console
$ sandboxer image build --backend microvm            # stock image → the tar store
$ sandboxer image build --backend microvm <profile>  # a profile's variant
$ sandboxer image rm --backend microvm               # drop the stored image
```

The build runs a `nixos/nix` microVM (no docker/podman anywhere) and stores the
result at `<state>/images/<tag>.tar`. A container-built image is **not** reused
by the microVM backend — the two use separate stores. `--engine` is
container-only and ignored here.

## Egress

A smolvm machine has **no network route by default**, so egress is a
create-time flag rather than a proxy sidecar:

- `egress.enabled = false` (or `SANDBOXER_NO_EGRESS=1`) → open network;
- `egress.allowedDomains` non-empty → `--allow-host` per domain, fail-closed
  (only those hosts resolve);
- egress on with an **empty** allowlist → a fully offline machine (a valid
  state here — with no default route, "allow nothing" simply reaches nothing).

`--allow-host` matches an **exact hostname**; a leading dot (`.example.com`, the
container allowlist's subdomain grammar) is stripped, so it no longer covers
`api.example.com`. Domain edits recreate the machine (the allowlist is part of
its config hash).

## What the microVM backend does not support

These are container-only and are rejected or ignored under `backend = microvm`:

- `sandboxer compose` — errors (it emits a docker-compose file and a
  podman/docker run argv with no VM equivalent).
- `egress.proxy`, `egress.noProxy`, `egress.routes` — config errors (smolvm has
  no upstream-proxy chaining).
- `limits.pids` — ignored with a warning (no smolvm equivalent).
- `nestedContainers` — ignored (a real VM runs container engines natively).

Default machine size is **2 vCPU / 4 GiB** (smaller than smolvm's 4/8, for the
parallel-agent workload); raise it with `limits.memory` / `limits.cpus`.

## Windows

There is no native Windows build. Run sandboxer **inside WSL2**, where the Linux
binary and the Linux smolvm use nested KVM (`.wslconfig` with
`nestedVirtualization=true`). See [windows.md](windows.md).

## Performance

Warm `exec` into a persistent session is ~26 ms (faster than `docker exec`);
cold machine boot is sub-second. The virtio-fs share is mounted synchronously so
the host sees edits immediately — great for source edits, but heavy in-tree
build churn (`go build`, `npm install` writing thousands of files) is slower
than a container bind mount. Keep build caches (`GOCACHE`, `node_modules`) on
guest-local storage. See [microvm-spike.md](microvm-spike.md) for the measured
numbers.
