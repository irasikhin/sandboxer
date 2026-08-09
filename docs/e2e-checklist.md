# microVM e2e checklist (macOS & Windows)

The Linux microVM e2e runs automatically (`.github/workflows/e2e.yml`, on
GitHub's KVM-capable runners, plus `scripts/itest.sh -run TestVM_`). macOS and
Windows have **no hosted-CI path** for hardware virtualization:

- **macOS/Apple Silicon:** Apple forbids nested virtualization inside a guest
  VM, and every hosted macOS runner *is* a guest VM — so Hypervisor.framework is
  unreachable in CI. Real Apple hardware (a dev Mac, or a bare-metal Mac runner)
  is the only option.
- **Windows:** the smolvm WHP path and WSL2's nested KVM both need bare metal;
  hosted Windows runners are Azure nested VMs where WHP does not initialize.

So these platforms are verified by **running this checklist by hand on real
hardware** before a release that touches the microVM backend. Record the result
in the PR: `verified microvm e2e on <os/arch/hw> <date> — N/N pass`.

## Prerequisites

- The runner under test installed: smolvm (`smolvm --version`, `SANDBOXER_SMOLVM`
  if off PATH) and/or microsandbox (`msb --version`, `SANDBOXER_MSB`). Both are
  worth a pass on a Mac — microsandbox is the only runner with macOS AND Windows
  builds, so it is the candidate for a default flip.
- macOS: macOS 11+ Apple Silicon. Windows: WSL2 with
  `nestedVirtualization=true` in `.wslconfig` and `wsl --shutdown` applied, then
  run everything **inside** the WSL2 Linux distro.
- A built sandboxer binary and a bootable image. The tests default to the public
  `alpine` ref; set `SANDBOXER_ITEST_VM_IMAGE=/path/to/image.tar` for offline —
  **and run at least one pass with the real toolbox tar**, not `alpine`. Size and
  layer count are load-bearing here: the EINVAL that made `backend = "microvm"`
  unbootable with egress on reproduces only on the large image, so an
  alpine-only run of these same tests stays green through it.

## Fast path — the Go suite

The same real-engine tests the Linux CI runs are portable; on a Mac they use
Hypervisor.framework automatically:

```console
$ SANDBOXER_SMOLVM=/path/to/smolvm \
  go test -tags integration -run 'TestVM_.*_RealEngine' ./internal/backend/...
$ SANDBOXER_MSB=/path/to/msb \
  go test -tags integration -run 'TestMSB_.*_RealEngine' ./internal/backend/...
```

Expect `TestVM_Lifecycle`, `TestVM_NarrowingWall`, `TestVM_GuestWriteUID`, and
(Linux/WSL2 only) `TestVM_SecretNotInPS` to pass, and the microsandbox twins
(`TestMSB_Lifecycle`, `TestMSB_NarrowingWall`, `TestMSB_GuestWriteUID`,
`TestMSB_EgressAllowlist`, `TestMSB_SecretsMode`). If they pass, the invariants
below are covered — the manual steps are the fallback when the Go suite cannot
run.

## Manual invariants

Run each and confirm the expected result. Substitute a real profile/slug.

| # | Invariant | Command | Expected |
|---|---|---|---|
| 1 | Boots & execs | `sandboxer enter box --backend microvm` then `echo hi` | a shell in the sandbox; `hi` |
| 2 | **Narrowing wall** | in a sandbox narrowed with `include`, `cat ../<sibling-worktree>/somefile` | fails — the sibling is not mounted |
| 3 | Host `/etc/shadow` | inside: `head -1 /etc/shadow` | the GUEST's file (or denied), never the host's |
| 4 | Guest-write uid | inside: `touch $PWD/from-guest`; on host `ls -l` it | owned by **you**, not root |
| 5 | Egress fail-closed | egress on, empty allowlist: `curl -m5 https://example.com` | fails (no route) |
| 6 | Egress allowlist | `allowedDomains = ["example.com"]`: `curl example.com` vs `curl cloudflare.com` | allowed resolves; the other fails DNS |
| 7 | Secret not in ps | with a hostConfigs token, from the host run `ps -axo command \| grep <token>` while an exec runs | the token does NOT appear |
| 8 | Exit codes | `sandboxer exec box -- sh -c 'exit 7'; echo $?` | `7` |
| 9 | Session persist | enter, start a tmux window, detach; `sandboxer enter box` again | the window is restored |
| 10 | Recreate on change | edit `limits`/`allowedDomains`, re-enter | "recreating session" notice, new machine |
| 11 | Image build (host nix) | `sandboxer image build --backend microvm` (needs network) | builds with host nix into the microVM store, no docker/podman and no builder guest |
| 12 | Clean teardown | `sandboxer clean` then `smolvm machine ls` | no leftover machines |
| 13 | **Nested containers in the guest** | in a microsandbox sandbox (toolbox image), `docker run --rm alpine id` then a user-switching image, e.g. `docker run --rm postgres:16-alpine id` | both exit 0 — container engines run natively inside the VM (uid 0, own kernel). **Passing on Linux/KVM** (msb 0.6.7: whole-range uid map, podman runs alpine + postgres with its uid-70 user-switch); macOS / Windows/WSL2 pending |

Run 1–12 again with `--backend microsandbox` (teardown check: `msb list`). One
runner-specific extra: a sandbox root under `/tmp` must be REFUSED with the
tmpfs-shadowing explanation.

Invariant 6 is worth running on **both** runners: `allowedDomains =
["example.com"]` must cover `www.example.com` on each. Both do — smolvm's
`--allow-host` is name-bound suffix matching, not the exact-host match this
checklist once claimed — so a divergence here means an upstream runner changed
its grammar, which is exactly what the check is for.

## Windows / WSL2 specific

- W1: confirm nested KVM — inside WSL2, `ls -l /dev/kvm` exists after the
  `.wslconfig` change. If absent, the whole Windows story is blocked (this is
  the plan's biggest unverified Windows assumption).
- W2: run invariants 1–10 inside the WSL2 distro exactly as on Linux.
- Native `smolvm.exe` (WHP) is **not** the supported v1 path (fork-maintained
  libkrun, single-vCPU, virtio-fs symlinks need Developer Mode); it is out of
  scope until separately verified.
