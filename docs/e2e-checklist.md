# microVM e2e checklist (macOS & Windows)

The Linux e2e runs automatically: `.github/workflows/e2e.yml` on GitHub's
KVM-capable runners, plus `scripts/itest.sh -run TestMSB_` locally. macOS and
Windows have **no hosted-CI path** for hardware virtualization:

- **macOS / Apple Silicon:** Apple forbids nested virtualization inside a guest
  VM, and every hosted macOS runner is a guest VM — so Hypervisor.framework is
  unreachable in CI. Real Apple hardware (a dev Mac, or a bare-metal Mac
  runner) is the only option.
- **Windows:** WSL2's nested KVM needs bare metal; hosted Windows runners are
  Azure nested VMs where it does not initialize.

So these platforms are verified by **running this checklist by hand on real
hardware** before a release that touches the microVM backend. Record the
result in the PR: `verified microvm e2e on <os/arch/hw> <date> — N/N pass`.

## Prerequisites

- microsandbox installed (`msb --version`; `SANDBOXER_MSB` if off `PATH`).
- macOS: macOS 11+ on Apple Silicon. Windows: WSL2 with
  `nestedVirtualization=true` in `.wslconfig` and `wsl --shutdown` applied,
  then run everything **inside** the WSL2 distro.
- A built sandboxer binary and a bootable image. The tests default to the
  public `alpine` ref; set `SANDBOXER_ITEST_MSB_IMAGE=/path/to/image.tar` for
  offline — **and run at least one pass with the real toolbox tar**, not
  `alpine`. Size and layer count are load-bearing here: the EINVAL that made
  the old smolvm runner unbootable with egress on reproduced only on the
  large image, so an alpine-only run of the same tests stays green through
  it.

## Fast path — the Go suite

The same real-engine tests the Linux CI runs are portable; on a Mac they use
Hypervisor.framework automatically:

```console
$ SANDBOXER_MSB=/path/to/msb \
  go test -tags integration -run 'TestMSB_.*_RealEngine' ./internal/backend/...
```

Expect `TestMSB_Lifecycle`, `TestMSB_NarrowingWall`, `TestMSB_GuestWriteUID`,
`TestMSB_EgressAllowlist`, `TestMSB_SecretsMode` and the git-share tests to
pass. If they do, the invariants below are covered — the manual steps are the
fallback when the Go suite cannot run.

## Manual invariants

Run each and confirm the expected result. Substitute a real profile/slug.

| # | Invariant | Command | Expected |
|---|---|---|---|
| 1 | Boots & execs | `sandboxer enter box` then `echo hi` | a shell in the sandbox; `hi` |
| 2 | **Narrowing wall** | in a sandbox narrowed with `include`, `cat ../<sibling-worktree>/somefile` | fails — the sibling is not mounted |
| 3 | Host `/etc/shadow` | inside: `head -1 /etc/shadow` | the GUEST's file (or denied), never the host's |
| 4 | Guest-write uid | inside: `touch $PWD/from-guest`; on host `ls -l` it | owned by **you**, not root |
| 5 | Egress fail-closed | egress on, empty allowlist: `curl -m5 https://example.com` | fails (no route) |
| 6 | Egress allowlist | `allowedDomains = ["example.com"]`: `curl example.com` vs `curl cloudflare.com` | allowed resolves; the other fails DNS |
| 7 | Secret not in ps | with a hostConfigs token, from the host run `ps -axo command \| grep <token>` while an exec runs | the token does NOT appear |
| 8 | Exit codes | `sandboxer exec box -- sh -c 'exit 7'; echo $?` | `7` |
| 9 | Session persist | enter, start a tmux window, detach; `sandboxer enter box` again | the window is restored |
| 10 | Recreate on change | edit `limits`/`allowedDomains`, re-enter | "recreating session" notice, new machine |
| 11 | Image build (host nix) | `sandboxer image build` (needs network) | builds with host nix into the microVM store, no docker/podman and no builder guest |
| 12 | Clean teardown | `sandboxer clean` then `msb list` | no leftover machines |
| 13 | **Nested containers in the guest** | in a sandbox on the toolbox image: `docker run --rm alpine id`; `docker run --rm --user 999:999 alpine id -u`; then postgres as a SERVICE — `docker run -d --name pg -e POSTGRES_PASSWORD=x postgres:16-alpine`, poll `docker exec pg pg_isready`, then `docker exec pg psql -U postgres -tAc 'select 42'` and `docker exec pg id -u postgres`. (Do NOT use `docker run --rm postgres id` — `id` replaces the image's CMD, so the entrypoint skips the data-dir chown and the gosu step-down and the command passes even where the uid machinery is broken.) | all pass — the engine runs, a non-root uid maps, postgres serves a query as its own user. **Partially passing on Linux/KVM:** the guest kernel + msb verified with `quay.io/podman/stable` as the guest (alpine, `--user 999:999`, postgres serving `select 42` as uid 70); the toolbox image's OWN engine stack (storage.conf, policy.json, registries.conf, pre-created `/var/tmp`) still needs one run. macOS / Windows/WSL2 pending |

One msb-specific extra: a sandbox root under `/tmp` must be REFUSED with the
tmpfs-shadowing explanation.

Invariant 6 checks the name-bound rule grammar: `allowedDomains =
["example.com"]` must cover `www.example.com` (msb's `*.suffix` target). A
divergence here means an upstream runner changed its grammar, which is exactly
what the check is for.

## Windows / WSL2 specific

- W1: confirm nested KVM — inside WSL2, `ls -l /dev/kvm` exists after the
  `.wslconfig` change. If absent, the whole Windows story is blocked (this is
  the plan's biggest unverified Windows assumption).
- W2: run invariants 1–13 inside the WSL2 distro exactly as on Linux.
