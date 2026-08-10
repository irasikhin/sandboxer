# The microVM backend (microsandbox)

Every sandbox is a real **microVM**. Where a container shares the host kernel
and leans on `--cap-drop`, user namespaces and seccomp for its boundary, a
microVM gives each sandbox its **own kernel** behind a hardware virtualization
boundary — the isolation an LLM agent running untrusted code deserves.

The runner is [microsandbox](https://microsandbox.dev) (`msb`), a thin CLI over
**libkrun** (KVM on Linux, Hypervisor.framework on macOS/Apple Silicon),
shelled out to exactly as the retired container backend shelled out to
docker/podman. `backend = "microsandbox"` is the default and the only value;
`SANDBOXER_MSB` overrides the looked-up binary.

> **History.** The microVM backend shipped first as an opt-in with two runners:
> smolvm (`backend = "microvm"`, the v1 runner) and microsandbox. The
> docker/podman container backend and the smolvm runner were both removed once
> microsandbox proved the better adapter — a lossless name-bound allowlist, a
> real image store, and it boots the stock profile smolvm 1.6.13 rejected with
> EINVAL. The retired `backend:` values (`docker`, `podman`, `microvm`,
> `native`) now fail with a migration error. The measured comparison that made
> the call: [microsandbox-spike.md](microsandbox-spike.md) and
> [microvm-spike.md](microvm-spike.md).

Three msb capabilities the backend leans on:

- a **name-bound network policy engine** (`--net-rule`) whose `*.suffix`
  targets are exactly squid's leading-dot grammar, so the allowlist keeps its
  subdomain coverage — and raw-IP dials are refused (rules match names, not
  addresses);
- an **image store** (`msb load`) — the toolbox tar is imported once and every
  later create is boot-only, never a multi-GB re-import;
- **labels**, so a sandboxer machine is identifiable through `msb list` alone
  (the source of truth stays the host-side record — see below).

## What carried over from the container era

Backend-agnostic, unchanged: srcs worktrees and `include` narrowing, remote
srcs, the per-sandbox home and `hostConfigs` seeding, profile.json, session
hashing/staleness, tmux capture/restore and agent auto-resume, resource limits
(`memory`/`cpus`), and the auth-env channel. Session identity lives in a
host-side record at `<state>/machines/microsandbox/<name>.json` — the slug,
base dir, config hash, image id and mount fingerprint; msb's labels duplicate
it for discoverability only.

Guest writes land on the host owned by **you** (virtio-fs maps the identity —
the guest's uid 0 never mints host-root files), and a narrowed sandbox
(`include`) shares only its listed directories — a sibling worktree on the same
host filesystem is unreachable inside the guest, the same wall the container
backend enforced.

**Nested containers** now run natively: the toolbox image's docker/podman/
compose work against the guest's own kernel with a full uid range — no opt-in
(`nestedContainers` is a retired key), no seccomp widening, no subuid grants.

## Requirements

The runner is a **preinstalled host requirement** (like `nix-instantiate` and
`git`) — sandboxer never downloads or vendors it. An absent runner is a clear
error with an install hint; `sandboxer doctor` checks everything below.

- **Linux:** `/dev/kvm` must exist and be accessible (typically the `kvm`
  group).
- **macOS:** macOS 11+ on **Apple Silicon** (Hypervisor.framework; Intel Macs
  are not a target). **Not live-verified** — see [macos.md](macos.md).
- **Windows:** inside WSL2 with nested KVM. **Not live-verified** — see
  [windows.md](windows.md).
- Install microsandbox from <https://microsandbox.dev>; `SANDBOXER_MSB`
  overrides the looked-up binary.
- **NixOS:** the upstream release is a generic dynamically-linked ELF and will
  not run out of the box (stub-ld), so this repo packages it:

  ```console
  $ nix run github:irasikhin/sandboxer#microsandbox -- doctor
  # or drop into the devShell (msb is on PATH there): nix develop
  ```

  To install system-wide, add the flake's `microsandbox` package (via
  `overlays.default`, which exposes `pkgs.microsandbox`) to
  `environment.systemPackages`. It is not in nixpkgs yet.

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
microsandbox (msb)   ✓  msb 0.6.7 available
```

## How it maps to the container backend it replaced

| container era | microsandbox |
|---|---|
| `podman/docker run … IMAGE` | `msb run/create/exec …` |
| bind mount `-v H:H:rw` | virtio-fs share `-v H:H` (directories only) |
| `--cap-drop=ALL`, `--userns=keep-id`, seccomp | subsumed by the VM boundary (absent) |
| squid sidecar + allowlist | `--net-rule` policy engine (no sidecar) |
| toolbox image in the engine store | prebuilt GHCR ref pulled into msb's image store (variants: a docker-save tar in `<state>/images/`, imported) |
| image built in a `nixos/nix` container | prebuilt image pulled; local builds with **host nix** |

tmux capture/restore, agent auto-resume, the mount-drift rebuild and the whole
enter/exec orchestration carried over verbatim.

## Getting the image

The stock toolbox image comes **prebuilt**:
`ghcr.io/irasikhin/sandboxer-toolbox:latest`, republished nightly and tagged
per release (`.github/workflows/image.yml`). The first `enter` lets msb
**pull and cache it host-side** (the pull honors the shell's `HTTP(S)_PROXY`);
a create only pulls a ref *missing* from the store, so a moved `latest` is
refreshed explicitly:

```console
$ sandboxer image pull             # pull, or refresh a moved `latest`
$ sandboxer image build            # LOCAL stock build (offline hosts)
$ sandboxer image build <prof>     # a profile's content-addressed variant
$ sandboxer image rm               # drop the cached image AND any stored tar
```

The **local build** covers what the registry cannot: a profile's `var-`
variant (never published — the first `enter` still auto-builds a missing one)
and offline/air-gapped hosts. It runs with **host nix** — the same
`path:<ctx>#image` derivation the CI workflow realizes, built directly (nix
and git are both already hard requirements of the CLI). The "latest"
input-rev pins resolve on the **host via `git ls-remote`**, so a cold pins
cache resolves the same way everywhere — the first-ever build on a fresh
machine just works. The realized tar is stored at `<state>/images/<tag>.tar`
and imported (`msb load`) into msb's own image store, which is what its
`create` boots — a locally built stock image sits there under the prebuilt
ref, so a create boots it without pulling. A rebuilt tar (or a re-pulled ref)
makes the cached copy read as stale, so a rebuild/pull + `recreate` is how a
new image reaches a sandbox. `nix run .#build-image` also places the tar into
the store.

## Egress

Egress is a create-time policy folded into the session hash (changing it
recreates the machine). There is no squid sidecar — enforcement is the
runner's. microsandbox defaults to an open network, and the four config states
map onto its policy engine (`backend.msbNetworkArgs`):

1. **`egress.proxy` set → proxy-delegated egress** (open network + proxy env:
   `HTTP_PROXY`/`HTTPS_PROXY`, `egress.noProxy` → `NO_PROXY`), with one
   translation: microsandbox's guest has a **real network stack**, so
   `127.0.0.1` in the guest is the guest itself. A loopback proxy URL is
   therefore rewritten to `host.microsandbox.internal` (msb's DNS resolves it
   to the gateway, and a gateway dial lands on the host's loopback) and the
   policy gets `--net-rule allow@public,allow@host:tcp:<port>`, because
   gateway dials classify as the `host` group, which the default
   `allow@public` policy denies — and any explicit rule replaces that implicit
   default, so public is restated in the same token (the `--net` profile flag
   would say the same but only exists since msb 0.6.7). A non-loopback proxy
   passes through untouched (a LAN-private proxy address is not reachable
   under the default public-only policy today). An `allowedDomains` set
   alongside a proxy is enforced by the proxy, not the VM — sandboxer warns.
2. **egress off** (`egress.enabled = false` / `SANDBOXER_NO_EGRESS=1`) → open
   network, no rules (labeled OPEN on every run when no proxy polices it).
3. **an allowlist** → `--no-net` (default deny) plus, per domain, an allow rule
   for HTTP and HTTPS: `allow@*.domain:tcp:80,allow@*.domain:tcp:443`. This is
   the **lossless** translation of the squid sidecar's leading-dot
   `dstdomain`: the domain and its subdomains, those two ports, DNS via the
   gateway, and nothing else. Rules are matched by NAME, so a raw IP — even
   the allowed domain's own — is refused.
4. **an empty allowlist** → `--no-net` alone: fully offline, DNS included.

`egress.routes` (per-domain upstream proxies) was a squid `cache_peer` feature;
the key is retired and errors with a migration hint.

## Credentials

Auth env (`hostConfigs` + the agents' `authEnv`) never enters the long-lived
machine's configuration, so it is not part of the session hash and a rotated
token is picked up by the next shell with no rebuild.

- **Default:** `--env KEY=value` per exec (and per one-shot run) — the same
  scoping the container backend used.
- **Opt-in:** `SANDBOXER_MSB_SECRETS=1` switches to msb's host-scoped
  `--secret KEY@host,host…` (with `--on-secret-violation block-and-log`),
  scoped to `egress.allowedDomains`. The value then never enters the guest at
  all — it gets a stand-in — and cannot be sent to a host outside the list.
  Two caveats keep it opt-in: the substitution direction (does the *real*
  value reach an allowed host?) is unverified upstream, so a wrong assumption
  silently breaks agent authentication; and the value is bound at boot, so a
  rotation needs a machine restart. Without an allowlist to scope to, the mode
  degrades to the default.

## What the microVM backend does not support

Rejected — with the reason — under `backend = "microsandbox"`:

- `egress.routes`, `limits.pids`, `nestedContainers` — retired config keys
  from the container era; each fails the strict decode with a migration hint
  saying what replaced it.
- a **fractional** `limits.cpus`, or an **unparseable** `limits.memory` —
  error: the runner takes a whole number of vCPUs and a parseable size, and a
  silent rounding / 4 GiB fallback is worse than a clear message.
- an `extraMounts` whose `source` is a regular **file** — error: virtio-fs
  shares directory trees only, so a docker-style single-file bind mount has no
  share analogue; mount a directory that holds the file instead.
- shares under `/tmp`, and a too-deep `MSB_HOME` — see Requirements.

Default machine size is **2 vCPU / 4 GiB** (deliberately modest — the workload
is several agents in parallel); raise it with `limits.memory` / `limits.cpus`.

## Migration status

The decommission is **done**: the docker/podman container backend and the
smolvm runner are removed, `microsandbox` is the default and only backend, and
the squid `sandboxer-proxy` sidecar image no longer exists. A config still
naming a retired backend or a retired key (`egress.routes`, `limits.pids`,
`nestedContainers`) fails with a targeted migration hint; `sandboxer doctor`
warns per profile whose backend cannot run here. Old smolvm machine records
(the flat `<state>/machines/` root) are inert leftovers — remove any stray
smolvm machines with smolvm's own CLI if you ran the experimental runner.

## Windows

There is no native Windows build of sandboxer. Run it **inside WSL2**, where
the Linux binary and the Linux msb use nested KVM (`.wslconfig` with
`nestedVirtualization=true`). Unverified on real hardware — see
[windows.md](windows.md).

## Performance

Warm `exec` into a persistent session is ~16 ms (faster than `docker exec` was);
cold boot is sub-second. virtio-fs shares are mounted `relatime` and stay live
in both directions; heavy in-tree build churn is slower than a native
filesystem, so keep build caches (`GOCACHE`, `node_modules`) on guest-local
storage when it bites. Measured numbers:
[microsandbox-spike.md](microsandbox-spike.md).
