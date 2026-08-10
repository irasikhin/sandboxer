# microsandbox vs smolvm — spike report

> **Historical record — predates the msb-only migration** (the container backend and smolvm were since removed; microsandbox is the only backend).

**Verdict: microsandbox (`msb`) is a credible, arguably better-fitting alternative to smolvm.**
It passes every security invariant the smolvm spike checked, sits on the same libkrun VMM (so the
isolation primitive is identical and the cgo-libkrun escape hatch is shared), and adds four things
smolvm cannot do at all: a **name-aware network policy engine** (per-domain, per-proto, per-port,
with `*.suffix` wildcards — squid's grammar without squid), a **host-scoped secret guard**, **labels
+ JSON metadata** (which removes the `_meta/<slug>.vm.json` sidecar the smolvm design was forced
into), and **macOS + Windows builds** — the gate that currently blocks flipping the backend default.

This is NOT a reason to rip out the shipped smolvm backend. It is a strong case for a second
adapter behind the same boundary (`internal/backend/vm*.go` behind golden tests), exactly as that
boundary was designed for.

Run 2026-07-27 on the same NixOS + KVM host as `microvm-spike.md`, microsandbox **v0.6.7**, against
the smolvm v1.6.13 the microvm backend ships. Same measurement discipline as the smolvm leg: median
of 5, first run dropped. macOS/Windows legs remain hardware-blocked.

## Why this spike

Both smolvm and microsandbox are thin consumption layers over **libkrun** (Red Hat; KVM on Linux,
HVF on macOS) — so the question was never "which hypervisor" (same one) but "which CLI fits
sandboxer's shell-out model best". smolvm was spiked and shipped first because it surfaced as the
direct libkrun CLI; microsandbox was not in the comparison set at decision time. This closes that
gap. The network dimension was added after an egress question: if containers and the squid sidecar
ever go away, whatever the VM runner enforces *is* the entire egress story.

## Environment

- Host: NixOS, kernel 6.12.63, x86_64, 16 cores, 27 GiB RAM, `/dev/kvm` = `crw-rw-rw-`, CPU virt `svm`.
- **microsandbox v0.6.7** (`superradcompany/microsandbox`, Apache-2.0, Rust, ~7k stars, pushed the
  day of this spike). Tarball `microsandbox-linux-x86_64.tar.gz`, sha256
  `5cae2e1d67b976a659baea73f42bd853611daf92e4f8187574db0689ad07e828` — matches the release
  `checksums.sha256`. Contents: exactly two files, `msb` + `libkrunfw.so.5.6.0`. Not in nixpkgs.
- Per release it also publishes `msb-{linux,darwin,windows}-{x86_64,aarch64}`, matching `libkrunfw`
  for all three OSes, and `libmicrosandbox_go_ffi-*` — **a Go FFI library**, i.e. an in-process
  alternative to shelling out.

### NixOS install friction — comparable to smolvm, with two extra traps

- **smolvm**: bundles libkrun+libkrunfw+agent-rootfs + a wrapper; needs the loader/libgcc patched
  (already packaged in `nix/smolvm.nix`).
- **microsandbox**: generic dynamically-linked ELF → `stub-ld`. Needs `patchelf --set-interpreter`
  plus **`libcap-ng.so.0`** on the rpath (the only non-glibc `NEEDED` lib), and
  `libkrunfw.so.5.6.0` must sit **beside the resolved binary** (or under `$MSB_HOME/lib`) under its
  exact versioned name — not via `LD_LIBRARY_PATH`.
- **Trap 1: the `ld-linux --library-path … ./msb` shim does not work.** `msb` re-execs itself as a
  `sandbox` child, so under a loader shim `/proc/self/exe` is the loader and the child dies with
  `error while loading shared libraries: sandbox`. Only a real `patchelf` of the binary works — fold
  that into the nix derivation and the `doctor` hint.
- **Trap 2: `MSB_HOME` must yield a Unix-socket path < 108 bytes.** A deep state dir fails at the
  first `run`: `agent relay socket path is too long: shortest derived path is 142 bytes … set
  MSB_HOME or paths.sandboxes to a shorter directory`. sandboxer's state root is
  `$XDG_STATE_HOME/sandboxer/<project-id>` with slug-derived names — the adapter must point
  `MSB_HOME`/`paths.sandboxes` somewhere short.
- **Trap 3: `MSB_HOME` is not relocatable.** Moving it after a pull breaks the image cache (records
  store absolute paths: `Data storage file "…/cache/fsmeta/sha256_….erofs": No such file or directory`).

`msb doctor` is a genuinely good preflight and reports all of this:

```
info Platform: Linux x86_64        info Version: v0.6.7
   ✓ msb  …/msb-patched            ✓ libkrunfw  …/libkrunfw.so.5.6.0
   ✓ CPU virt     svm              ✓ KVM device   /dev/kvm      ✓ KVM access read/write
done Host setup is ready.
```

## The four invariants (same as microvm-spike.md)

All checks driven through the real `msb` CLI on a live microVM.

| # | Invariant | smolvm | microsandbox | Notes |
|---|---|---|---|---|
| 1 | **Narrowing wall** — a non-mounted sibling dir is unreachable in the guest | ✅ | ✅ | full escape matrix below |
| 2 | **uid** — a guest-written file is host-user-owned | ✅ (`ir:users`) | ✅ (`ir:users`) | guest runs as root, host sees the invoking user |
| 3 | **secrets** — token not exposed in host `ps` | ✅ `--secret-env` | ✅✅ `--secret ENV@HOST` | msb's is stronger — see below |
| 4 | **exit codes** propagate | ✅ | ✅ (`rc=7`) | stdin pipes and PTY also verified |

### 1. Host share — path-identical, live in both directions

`-v <path>:<path>` + `-w <path>` works; the sandboxer mount model (identical host/guest paths,
`sandbox.Mounts` deciding the set) transfers unchanged. The mount lands as `virtiofs (rw,relatime)`
— **not** `sync` like smolvm's, which is where the write speed below comes from.

| check | result |
|---|---|
| guest write → host read, same instant | ✅ `guest-live-500` |
| host write → guest read, same instant | ✅ `host-live-1968813` |
| **host creates a NEW dir under the share while the sandbox runs** | ✅ visible + readable immediately in the running sandbox |

That last row is the property `docs/live-view-design.md` was written about: under an unnarrowed
share, new host directories appear live with no recreate.

**Fixture gotcha (also seen in the parallel run):** the guest mounts a `tmpfs` on `/tmp` *after* the
shares, so a share whose destination is under `/tmp` is shadowed — `mount` shows the virtiofs,
`ls` says no such file. Any non-`/tmp` path works. sandboxer worktrees are not under `/tmp`, but a
msb adapter must confirm identity mapping on the real worktree roots (it holds off `/tmp`).

### 2. Containment wall — holds, identical to smolvm

Only the shared dir is mounted; ancestors materialize as empty skeleton dirs:

| attack | result |
|---|---|
| list the parent of the share | ✅ shows only `child/` — `secret.txt` invisible |
| absolute host path to the sibling secret | ❌ `No such file or directory` |
| `../secret.txt` dot-dot traverse | ❌ blocked |
| host-made symlink `child/link → parent/secret.txt` | ❌ dangles |
| host-made symlink `child/link → /etc/shadow` | reads the **guest's** `/etc/shadow` (`root:*::0:::::`) |
| guest-made symlink → host absolute path | ❌ dangles |
| guest-made symlink → `/etc` | resolves to the **guest** `/etc` |

`include`-narrowing is as safe here as on smolvm and on containers: the mount set is the wall.

### 3. uid / ownership / modes

- Guest default identity is `uid=0(root)`; host files land owned by the **invoking host user**
  (`1000 100`) — no `--userns=keep-id` analogue needed.
- `-u 1000:1000` works (`uid=1000 gid=1000`), host ownership unchanged.
- **Mode quirk (new vs smolvm):** files/dirs created in the guest appear on the host as `0600`/`0700`
  while the guest sees them `0644`/`0755`. smolvm produced `0644/0755` host-side. Harmless on a
  single-user box; worth a docs line.

### 4. Secrets — microsandbox is stronger

smolvm's `--secret-env GUEST=HOST` reads a host env var at launch and never persists it — the real
value does reach the guest environment. microsandbox's `--secret ENV@HOST[,HOST...]` reads from the
host env, stores only a reference (inline `ENV=VALUE@HOST` is *rejected*), gives the guest a
**stand-in**, and scopes where that handle may travel:

```
host token len=23        guest len=16        # different value, verified by length
grep -rc <value> $MSB_HOME → no match        # never persisted
```

Controlled experiment — one sandbox, `--secret SPIKE_TOKEN@httpbin.org`, `example.com` allowed by a
net rule but **not** on the secret's host list:

```
--- control: example.com WITHOUT token ---   CONTROL-OK
--- example.com WITH token ---               WITHTOKEN-BLOCKED
```

Same host, same rule, same run — the *presence of the secret* is what blocked the request
(`--on-secret-violation {block,block-and-log,block-and-terminate,passthrough}`). That is DLP on the
credential itself, directly relevant to running agent code with a live API token; sandboxer today
passes `authEnv` through and relies on the egress allowlist alone (see `SECURITY.md`'s
hostConfigs-exfiltration caveat).

Both backends leak the value if you use plain `-e/--env` instead — same footgun, same fix.

**Not verified:** the substitution direction (does the *real* value reach an allowed host?).
`httpbin.org` answered 503 during the run and a local echo server was unreachable from the guest
(host firewall, not inspectable without root). Re-test before designing on it.

### 5. Network — a policy engine, not an allowlist

Grammar: `--net-rule "<action>[:<direction>]@<target>[:<proto>[:<ports>]]"`, repeatable and
comma-separated; targets are IPs/CIDRs, domains, `*.suffix` domains, or groups (`public`, `private`,
`multicast`, …). Measured:

| config | probe | result |
|---|---|---|
| no flags (default) | DNS, HTTP, HTTPS, raw IP | all **OK** — open network |
| `--no-net` | same | all **FAIL**, DNS `NXDOMAIN` — fully offline |
| `--no-net --net-rule allow@example.com:tcp:80` | `http://example.com` | **OK** |
| ″ | `https://example.com` (same host, port 443) | **FAIL** — per-port |
| ″ | `https://api.github.com` | **FAIL** |
| ″ | `http://8.47.69.0/` (the allowed domain's own IP) | **FAIL** — rules are **name-bound**, not IP-bound |
| ″ | `http://1.1.1.1/`, `https://1.1.1.1/` | **FAIL** |
| ″ | DNS resolution | **OK** — gateway DNS auto-enabled by the domain rule |
| `--no-net --net-rule allow@*.github.com:tcp:443` | api / apex / codeload github.com | **all OK**; `example.com` FAIL |
| `--no-net --net-rule allow@github.com:tcp:443` | apex only OK; api + codeload **FAIL** | exact-host |

Two consequences for the config schema:

1. **`*.domain` covers apex + subdomains — exactly squid's leading-dot semantics.** The mapping
   `.example.com` → `*.example.com` is lossless, and plain `example.com` maps to exact-host. That
   erases the microvm allowlist regression recorded at `internal/backend/vm.go:187-193`
   (`vmAllowHosts` strips the dot, so `cloudfront.net` stops covering distributions and ECR/Hub blob
   pulls break).
2. Denial is by **name**, so a shared-CDN IP is not a hole — the raw-IP probe to the allowed
   domain's own address was refused.

Also present, unmeasured: `--net-default-egress/-ingress`, `--net public|private|host` profiles,
`--dns-nameserver`, `--dns-query-timeout-ms`, `--no-dns-rebind-protection`, `--net-ipv4/6-pool`,
`--max-connections`, `--tls-intercept` (+ custom CA, `--tls-bypass`, QUIC blocked by default when
on), `--trust-host-cas`, `-p/--port` forwarding.

**Gap vs the container backend:** no upstream-HTTP-proxy chaining for *guest* traffic — no
`cache_peer` analogue, so `egress.proxy` / `egress.routes` stay unsupported (as they already are
under microvm, `internal/config/runtime.go:192-205`). Partial compensation: **`msb pull` honors
`HTTP(S)_PROXY`** — proved by pointing it at a dead proxy and watching the pull fail — which is
exactly the RU-host image-pull path `internal/toolbox/build_vm.go` has no answer for today.

### 6. Lifecycle, metadata and CLI fit

microsandbox's surface is **richer and maps at least as well** as smolvm's:

| need | smolvm | microsandbox |
|---|---|---|
| one-shot run | `machine run -I img -- cmd` | `msb run img -- cmd` |
| persistent + exec | `machine create/start` + `machine exec` | `msb create` + `msb exec` |
| host dir share | `-v H:G[:ro]` (dirs) | `-v SOURCE:DEST[:OPTIONS]`, plus `--mount-dir/-file/-disk/-named`, `--tmpfs` |
| workdir / env | `-w` / `-e` | `-w` / `-e` |
| cpu / mem | `--cpus` / `--mem` | `-c/--cpus` / `-m/--memory` (+ `--max-*` hotplug ceilings) |
| **local image store** | ✗ (`-I <tar>` re-imports each run) | ✅ `msb load`/`save`/`pull`, cached by ref |
| **labels + machine-readable state** | ✗ (forced the `_meta/<slug>.vm.json` sidecar) | ✅ `--label`, `list --label` (AND-matched), `list/inspect --format json` |
| snapshots / fork | fork base flag | `snapshot` subcommand, `--from-snapshot` |
| extras | — | `copy`, `ssh`, `ps`, `metrics`, `logs`, named `volume`s, `--security restricted`, `--rlimit` |

Verified lifecycle: `create --name X` (boots in background, returns in ~0.2 s) → `exec` → `stop`
(2.0 s) → `start` → `exec`, with **both the guest rootfs upper layer and the shared files
surviving** (`persisted-in-guest`, `shared-write`); `rm -f` is non-interactive. `inspect` reports
image, status, cpus/memory, security profile, **workdir, labels and every mount with its host path**:

```
Labels    sbx.hash=abc123  sbx.slug=demo
Mounts    /var/tmp/…/child → /var/tmp/…/child (rw)     /tmp → tmpfs (128 MiB) (rw)
```

→ the docker-labels pattern `internal/backend/session.go` already uses for
`ConfigHash`/`sandboxer.mounts` works unchanged, and **the planned sidecar is unnecessary here.**

Both runners are **daemonless**: one `msb sandbox --name <X>` process per sandbox, reparented to
init (~114 MB RSS), nothing lingering after a `run`. Exec semantics: exit codes propagate
(`rc=7`), stdin pipes (`PIPED-IN`), `-t` allocates a real PTY (`/dev/pts/0`, `test -t 0/1` both
true) → interactive `enter` + tmux viable.

### 7. Image model — OCI native, and `docker save` tars load

`msb pull alpine` = 7.9 s from Docker Hub; images cached as erofs under `$MSB_HOME/cache`.
`msb load -i <tar> -t <ref>` consumed a `docker save` archive in 65 ms and booted it with
`--pull never`:

```
✓ Loaded  alpine:latest      ✓ Loaded  spike-loaded:v1
BOOTED-FROM-TAR    3.24.1
```

Same tar shape the toolbox `dockerTools.buildLayeredImage` emits, so the existing image pipeline
feeds this runner directly — and unlike smolvm there is no `--max-image-size` ceiling to raise.
`--pull always|if-missing|never`, `--insecure`, `--ca-certs` present; registry credentials via
`msb registry`.

The load-bearing difference is the **image store**: sandboxer builds the toolbox image once and
reuses it; microsandbox caches it by reference so every cold boot is boot-only, while the smolvm
backend stores a tar and passes `-I <tar>`, which smolvm **re-imports on every cold create**. That
shows up in the numbers below.

## Performance

Median of 5, first dropped, same host, alpine:

| op | microsandbox | smolvm | docker |
|---|---|---|---|
| **warm exec** (persistent machine) | **16–17 ms** | 26 ms¹ | 56–65 ms |
| **create → first exec ready** | **250 ms** | — | — |
| cold ephemeral run (`run … true`) | 2.2 s | 0.73 s¹ / ~5.1 s² | 0.77 s |

Raw samples this run: msb exec `16 17 17 16 15`; docker exec `61 56 56 53 55`; msb run
`2219 2223 2238 2232 2231`; docker run `822 877 767 607 573`.

¹ from `microvm-spike.md` (raw tarball, clean run).
² a parallel measurement of the **nix-packaged** smolvm via `machine run -I <tar>` came out ~5.1 s,
confounded by (a) the per-run tar re-import and (b) a possible cold-boot regression in
`nix/smolvm.nix` — likely read-only nix-store templates/rootfs forcing a copy per boot. **Worth its
own look**; do not treat the 5 s as microsandbox's win.

microsandbox's ephemeral `run` is the one clear regression (root-disk provisioning + agent-relay
setup + teardown per run). It is also the path sandboxer does not use: the model is persistent
sessions, where **250 ms to a live machine and 16 ms per exec** is the best of all three backends.

Share I/O (host-timed, each figure includes ~16 ms exec overhead):

| workload | on the share | guest-local | smolvm share (prev spike) |
|---|---|---|---|
| 500 small files | 214 ms (~0.4 ms/file) | 46 ms | ~7.5 ms/file (≈3.75 s) |
| 128 MB bulk write | 273 ms | 212 ms | ≈2 s (scaled from 256 MB / ~4 s) |

`rw,relatime` instead of smolvm's `rw,sync` buys ~18× on small-file churn while liveness still held
in both directions in every probe above. The smolvm-era "keep build caches off the share" guidance
is much less pressing here.

## Adapter delta (estimate)

Engine dispatch is already a string switch, so this is additive, not a rewrite:

- `internal/backend/detect.go` — a second engine identity beside `smolvmEngine`, its own
  `SANDBOXER_MSB` binary hook and `doctor` status (KVM + libkrunfw resolution + an `MSB_HOME`
  length check).
- `internal/backend/vm*.go` — a sibling argv builder: `-v host:host` and `-w` map 1:1; `--label`
  replaces the sidecar-metadata plan; `--secret ENV@HOST` replaces `--secret-env`;
  `create/exec/stop/start/rm -f` map onto the existing session lifecycle; `list --format json
  --label` replaces the machine-registry scan.
- `internal/config/runtime.go` — `validateMicrovm` can *relax* for this engine: `.domain` becomes
  `*.domain` (allowlist parity with squid restored), per-port/proto rules become expressible;
  `egress.proxy`/`routes` stay rejected (no guest-side proxy chaining).
- `internal/toolbox/build_vm.go` — `msb load` instead of the bespoke rootfs path, and it can finally
  honor `config.HostProxyEnv` for pulls.
- `nix/` — a `microsandbox` derivation: patchelf (interpreter + libcap-ng rpath), keep
  `libkrunfw.so.5.6.0` beside the binary, no autoPatchelf of guest assets; darwin/aarch64 variants
  available from the same release.

## Risks

1. **Substitution direction unverified** (§4) — re-test before relying on `--secret` for agent auth.
2. **Pre-1.0** (v0.6.7, daily development) → flag/CLI churn; pin the version in nix, gate upgrades on
   the itest.
3. **`MSB_HOME` path-length + non-relocatability** must be designed in, not patched later.
4. `0600` host modes for guest-created files (§3).
5. macOS/Windows binaries exist but were **not** run — the e2e checklist still needs real hardware;
   this spike only moves those legs from "impossible" to "untested".
6. **Bus factor:** both are libkrun consumption layers, so the ultimate dependency (and the
   cgo-libkrun fallback) is identical. microsandbox is more feature-complete and active, company-
   backed (Super Rad Company) with a closed-beta cloud — the usual "OSS deprioritized for the paid
   product" risk. smolvm is thinner and simpler. Neither is clearly safer.

## Recommendation

Keep **smolvm** for v1 — it is shipped, verified end-to-end (`microvm-spike.md` +
`internal/backend/vm_real_integration_test.go`) and released as v0.62.x. Add **microsandbox as a
second engine** behind the same adapter boundary and run the soak on it, because it is the only
candidate that can plausibly close the mac/win gate, and because its network + secret model is the
first thing in this project that is strictly *stronger* than the squid sidecar it would replace.
Concretely, four properties would be real upgrades:

1. the **network policy engine** — squid's domain grammar plus per-port/proto/CIDR/ingress, enforced
   in the runner, with no sidecar and no proxy env;
2. the **host-scoped `--secret`** — the value never enters the guest, and cannot leave to a
   non-listed host;
3. the **image store** (`msb load`) — removes smolvm's per-cold-create tar re-import;
4. **labels + JSON state** — deletes the sidecar-metadata design and reuses the container-shaped code.

If the soak confirms, the default flip (and the podman/docker removal the plan wants) is argued from
microsandbox, not from smolvm. Follow-up either way: investigate the nix-packaged smolvm cold-boot
regression (~5 s vs the raw tarball's ~0.7 s).

Reproduction transcript: session scratchpad `msb/` (patched binary, `msbw` shim, `perf.sh`), state
dir `/tmp/msb-spike`, fixtures `/var/tmp/msb-spike/wall`.
