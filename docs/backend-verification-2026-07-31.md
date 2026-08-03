# Backend verification pass — 2026-07-31

A full pass over all four isolation backends on one host, at `088d93f` (v0.66.0
+ 3 dependency commits). Motivation: CI exercises the integration suite on
**docker only**, the microVM legs run nightly against `alpine`, and the
CLI-level checklist in [e2e-checklist.md](e2e-checklist.md) had never been
recorded as run for either microVM runner.

**Verdict: `docker`, `podman` and `microsandbox` pass. `microvm` (smolvm) does
not boot its default profile.** Six defects found. Four are fixed on this
branch, one (F2) turned out to be fixed concurrently on main and is covered
with tests here instead, and one is upstream and documented with workarounds.

## Environment

| | |
|---|---|
| Host | NixOS 25.05, kernel 6.12.63, x86_64, 16 CPU / 27 GiB, `/dev/kvm` mode 0666 |
| Toolchain | go 1.26.3, golangci-lint 2.12.2 (all via `nix develop`) |
| Engines | docker 27.5.1 (rootful), podman 5.8.2 (devShell only) |
| Runners | smolvm 1.6.13, microsandbox 0.6.7 (msb), libkrunfw 5.6.0 |
| Images | toolbox + proxy pulled from `git.rgband.ru/rgband/…:pins-64c08a7-9f92699` (matching the flake's nixpkgs/llm-agents pins), retagged to the default names; toolbox `docker save`d to `<state>/images/sandboxer-toolbox-latest.tar` (3.45 GB) for the microVM runners |
| Network | direct outbound available (no proxy needed), so every live-egress leg ran for real |

## Results

### Automated suites (`-tags integration`)

| Leg | Result |
|---|---|
| docker — backend + egress + cli + toolbox | **416 pass, 0 fail**, 2 skip (nested-multi-uid is podman-only; the image build needs `SANDBOXER_ITEST_BUILD_IMAGE=1`) |
| podman — backend + egress + cli | **360 pass, 2 fail**, 4 skip — both failures are host-environment, not sandboxer (see F6) |
| smolvm — `TestVM_*_RealEngine` | **4/4 pass** on `alpine`; the new 5th case fails on the toolbox tar (see F1) |
| microsandbox — `TestMSB_*_RealEngine` | **5/5 pass** |

Unit gate: gofmt clean, `go vet` clean on both build tags, golangci-lint 0
issues, `go test ./... -race` green, coverage **90.9%** (floor 90%).

The msb cases finish in ~0.4 s each, which looked vacuous; it is not. A direct
`msb run alpine -- uname -a` reports guest kernel **6.12.95** against the host's
6.12.63, hostname `msb-<id>`, uptime 0.08 s, 1 vCPU — real microVMs, booting in
about 2 s.

### CLI invariants (live, throwaway project)

Against a real project with `srcs`, an allowlist and the toolbox image:

| Invariant | docker | podman | smolvm | msb |
|---|---|---|---|---|
| boot + exec | ✅ | ✅ | ❌ **F1** | ✅ |
| exit-code propagation | ❌ **F2** → ✅ | ❌ **F2** → ✅ | ❌ **F2** → ✅ | ❌ **F2** → ✅ |
| narrowing wall (sibling unreachable) | ✅ | ✅ | ✅ | ✅ |
| host `/etc/shadow` not the host's | ✅ | ✅ | ✅ | ✅ |
| host project files outside the sandbox | ✅ | ✅ | ✅ | ✅ |
| git metadata absent in guest | ✅ | ✅ | ✅ | ✅ |
| guest-written file owned by the host user | ✅ | ✅ | ✅ | ✅ |
| egress: allowlisted domain reachable | ✅ | ✅ | n/a (F1) | ✅ (HTTP 200) |
| egress: off-list domain refused | ✅ | ✅ | n/a (F1) | ✅ (curl 000) |
| secret absent from host `ps` | ✅ | ✅ | ✅ | ✅ |
| `image build` / store seeding | ✅ | ✅ | ✅ | ✅ (auto-imports the tar) |

`microvm` was additionally confirmed to boot and run correctly with
`SANDBOXER_NO_EGRESS=1` — same image, same three shares — which is what isolates
F1 to the allowlist flag.

## Findings

### F1 — `backend = "microvm"` cannot boot its default profile *(upstream; documented)*

Every `enter`/`exec` on smolvm fails in ~3 s:

```
krun_start_enter returned: -22 (EINVAL — libkrun rejected the VM configuration …)
```

sandboxer always shares three directories (sandbox dir, private home, read-only
`/run/sandboxer`), and an allowlist adds `--allow-host`. **Three shares + a
network flag + a large image** is the combination libkrun refuses. Bisected to a
minimal reproducer that needs no sandboxer at all:

```console
$ smolvm machine run -I toolbox.tar -v A:A -v B:B -v C:C:ro --allow-host example.com -- true   # EINVAL
$ smolvm machine run -I toolbox.tar -v A:A -v B:B -v C:C:ro                          -- true   # boots
$ smolvm machine run -I toolbox.tar -v A:A -v B:B          --allow-host example.com  -- true   # boots
$ smolvm machine run -I alpine      -v A:A -v B:B -v C:C:ro --allow-host example.com -- true   # boots
```

`microsandbox` takes the byte-identical profile and boots in 27 s. This is
upstream smolvm/libkrun, so the fix here is disclosure: `docs/microvm.md` now
opens with the breakage and steers to `microsandbox`, and
`docs/troubleshooting.md` carries the reproducer and four workarounds.

**Why it shipped green:** the smolvm integration cases each boot with ONE share
and no network flags, so the argv shape the CLI always emits was never tested —
there was no smolvm twin of `TestMSB_EgressAllowlist_RealEngine`. And the suite
boots `alpine`, on which the bug does not reproduce. Both halves are now closed:
`TestVM_EgressAllowlist_RealEngine` carries the full mount set, and the
checklist requires one pass with the real toolbox tar.

### F2 — `exec`/`enter` never returned the child's exit code *(fixed on main in v0.67.0; tests added here)*

Every non-zero status came back as **1**, on all four backends. The code was
read correctly off `backend.Run`/`ExecSession` and then discarded into a
`silentErr`; `cli.Run` returned a flat 1 for any error. `sandboxer exec box --
make test` could not gate a script or a CI job. This is checklist invariant 8.
Verified live after the fix: 7 → 7, 130 → 130, 0 → 0.

Notably `raw docker run` returns 7 correctly and the integration suite proves
`backend.Run` reports 7 — the loss was in the CLI seam above both, which nothing
covered.

Two sessions found this independently: a concurrent branch landed the same
`exitErr` passthrough and shipped it as **v0.67.0** while this pass was running,
so the fix on this branch was dropped in favour of the released one. It went out
with no test, so what remains here is the regression coverage —
`internal/cli/exitcode_test.go`, exec and enter over six statuses at the `Run()`
seam. The measured four-backend result below stands: it was taken against the
broken build, and re-verified against the fix.

### F3 — an empty `allowedDomains` silently meant "the 40 defaults" *(fixed)*

`allowedDomains = [ ]` — the spelling that reads as "reach nothing" — resolved
to the full built-in allowlist. `ResolveRuntime` joined the list to a string and
fell through to the defaults when empty, folding "absent" and "explicitly empty"
together. Fail-open against intent, and deny-all was unexpressible: the flag and
`SANDBOXER_DOMAINS` collapsed the same way (all three measured: `egress=on (40
domains)`). It also made `errEmptyAllowlist` dead code from the config path —
exercised only by tests constructing `config.Runtime` directly.

Now `[ ]` stays empty and the container backend refuses with a message pointing
at `egress.enabled = false`; an absent attr still gets the defaults.

### F4 — the documented smolvm allowlist grammar was wrong *(fixed)*

Code and docs claimed `--allow-host` matches an exact host, so a leading dot was
"lost" and `example.com` stopped covering `api.example.com`. Measured:

| allow | github.com | api.github.com | codeload.github.com | example.com |
|---|---|---|---|---|
| `github.com` | ✅ | ✅ | ✅ | ❌ |
| `api.github.com` | ❌ | ✅ | ❌ | ❌ |

Name-bound suffix matching — subdomains on different IPs are admitted, the
parent of an allowed subdomain is not — i.e. exactly squid's leading-dot
`dstdomain` and msb's `*.domain`. All three backends agree. The old claim
understated the reach a listed domain grants and was the stated reason msb could
check something smolvm could not. Corrected in `vm.go`, `docs/microvm.md` (×2)
and `docs/e2e-checklist.md`.

(msb's `*.domain` was checked in the same way and does cover the apex: allow
`example.com` → apex 200, `www` 200, off-list refused.)

### F5 — `create` skipped backend validation *(fixed)*

`create` ran the `warn*` helpers but never `ValidateBackend`/`ValidateSession`,
so a microVM profile with `egress.routes` was announced as created — worktrees
and state dir on disk — while `enter`, `exec`, `stop` and `compose` all refused
it.

### F6 — the devShell ships podman with none of its configuration *(documented)*

`nix develop` puts the podman **binary** on `$PATH` and nothing else. On a host
without a system-wide podman install, podman cannot pull, load or run anything:

- `Error: no policy.json file found …`
- `Error: short-name "alpine:latest" did not resolve to an alias and no containers-registries.conf(5) was found`
- `newuidmap` is at `/run/wrappers/bin`, which `nix develop` drops from `$PATH`

Both podman integration failures trace here, not to sandboxer:
`TestRunArgv_RealEngineAccepts` sets `HOME` to a temp dir (to prove no host
credentials bind) which also hides podman's own user config, and
`TestRun_RealEngine_NestedMultiUID` needs `newuidmap`. After writing a minimal
`policy.json` + `registries.conf` by hand, the whole podman leg passes.
`containers-common` is not an attribute in the pinned nixpkgs, so this is
recorded in `docs/troubleshooting.md` and `scripts/itest.sh` rather than papered
over in the flake.

### Lower-severity observations

- **No supported way to seed the microVM tar store from an existing image.**
  `vmBuildImageToStore` only ever *builds*, `BuildVMImage` rebuilds
  unconditionally, and `vmEnsureImage` treats any non-default image name as "a
  public ref — let the runner pull it" (which would send smolvm at a private
  registry). Seeding this pass required a hand `docker save` into
  `<state>/images/<sanitized-name>.tar`. An `image import --from-engine` would
  close it.
- **smolvm re-ingests the image on every ephemeral run** (~2 min for the 3.45 GB
  tar, every `machine run`), while msb imports once into its own store and then
  boots in seconds. A large practical difference for `exec`, which uses the
  one-shot path whenever no session is live.
- **`doctor` flags `sandboxer.nix` as gitignored** and advises dropping the
  rule — but this repo deliberately ignores it (`.gitignore:27`, "the
  per-developer config, which names host paths"). The tool's advice contradicts
  its own repo's convention.
- **The guest runs as uid 0** on both microVM runners (`whoami` fails, no passwd
  entry). By design — the VM boundary replaces the uid boundary, and
  guest-written files still land host-user-owned — but it differs visibly from
  the container backends' `--user 1000:100`.

## Parity divergences reviewed

All nine backend-conditional behaviours were checked against the code and, where
observable, against a live run. Seven are intended and correctly documented:
empty-allowlist semantics (container errors, VM boots offline — now reachable,
see F3), `localhost` proxy rewrite on containers only, `limits.pids` and
`nestedContainers` warned-and-ignored on microVM (verified: both warnings fire),
`egress.routes` and `compose` hard-refused on microVM (verified), smolvm
dropping unresolvable domains (verified — `cloudfront.net` dropped with an
explanatory warning), `msb exec` carrying no `-i`, and the VM-only `"gone"`
state. Two produced findings: F4 above, and —

- **VM sweeps iterate host-side records, never the live inventory**
  (`vmRemoveAllSessions`, `vmAllSessionStates`, `vmOrphanSessions`). A machine
  whose record is lost — a wiped state dir, a changed `SANDBOXER_STATE` — is
  invisible to `clean`, `list` and `doctor` while still existing in
  smolvm/msb. The container path cannot have this: it queries the engine by
  label. msb already stamps labels and smolvm machine names carry a
  `sandboxer-` prefix, so a live-inventory sweep is available to both. Left
  unfixed here (it needs a design call on which store is authoritative) and
  recorded as the main open item.

## Not covered

macOS and Windows/WSL2 — no hardware. `docs/e2e-checklist.md` keeps them manual,
and W1 (nested KVM under WSL2) remains the biggest unverified assumption.
