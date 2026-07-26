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

- smolvm installed (`smolvm --version`); `SANDBOXER_SMOLVM` set if off PATH.
- macOS: macOS 11+ Apple Silicon. Windows: WSL2 with
  `nestedVirtualization=true` in `.wslconfig` and `wsl --shutdown` applied, then
  run everything **inside** the WSL2 Linux distro.
- A built sandboxer binary and a bootable image. The tests default to the public
  `alpine` ref; set `SANDBOXER_ITEST_VM_IMAGE=/path/to/image.tar` for offline.

## Fast path — the Go suite

The same real-engine tests the Linux CI runs are portable; on a Mac they use
Hypervisor.framework automatically:

```console
$ SANDBOXER_SMOLVM=/path/to/smolvm \
  go test -tags integration -run 'TestVM_.*_RealEngine' ./internal/backend/...
```

Expect `TestVM_Lifecycle`, `TestVM_NarrowingWall`, `TestVM_GuestWriteUID`, and
(Linux/WSL2 only) `TestVM_SecretNotInPS` to pass. If they pass, the invariants
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
| 11 | Image build in VM | `sandboxer image build --backend microvm` (needs network) | builds via a nixos/nix microVM, no docker/podman |
| 12 | Clean teardown | `sandboxer clean` then `smolvm machine ls` | no leftover machines |

## Windows / WSL2 specific

- W1: confirm nested KVM — inside WSL2, `ls -l /dev/kvm` exists after the
  `.wslconfig` change. If absent, the whole Windows story is blocked (this is
  the plan's biggest unverified Windows assumption).
- W2: run invariants 1–10 inside the WSL2 distro exactly as on Linux.
- Native `smolvm.exe` (WHP) is **not** the supported v1 path (fork-maintained
  libkrun, single-vCPU, virtio-fs symlinks need Developer Mode); it is out of
  scope until separately verified.
