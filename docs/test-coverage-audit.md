# Test coverage audit

This maps the `internal/*` packages to the kind of test that exercises them,
distinguishing the **fake path** (in-process unit tests with stub engines /
`httptest` — these run in CI and feed the 90% coverage gate) from the **real
path** (the `-tags integration` end-to-end suite that drives a real container
engine, real networks and the real squid egress proxy). The real path is
`//go:build integration`, so it is excluded from the CI coverage gate; it runs
via `scripts/itest.sh` locally and on the Jenkins e2e job (see
[CONTRIBUTING](../CONTRIBUTING.md#integration-tests)).

Legend: ✓ covered · — not applicable (pure logic / no external process) · ⤴
exercised transitively.

## backend

| Function(s) | Fake-path test | Real-path test |
|---|---|---|
| `Run` | `TestContainerRun*` (stub engine) | `TestRun_RealEngine_NoEgress_ExitAndMount`, `…_ExitCodePropagation`, `…_WallTimeoutKills` |
| `RunArgv` | `TestRunArgv` (string only) | `TestRunArgv_RealEngineAccepts` (engine parses the argv) |
| `EnsureSession` / `ExecSession` / `StopSession` / `RemoveSession` / `InspectSession` / `SessionWantHash` | `session_test.go` (`planSession` oracle, stub engine) | `TestSession_RealEngine_Lifecycle` (create → persist → stale-recreate → rm), `TestSession_RealEngine_StopStart` (stop keeps the container, start resumes it) |
| `ConfigHash` / `CreateArgv` / `ExecArgv` | `session_test.go` | ⤴ via the session lifecycle tests |
| `ResolveEngine` / `DetectEngine` | `TestResolveEngine` / `TestDetectEngine` (stub PATH) | ⤴ via real `Run` |
| `EngineLabel` | `TestEngineLabel` (direct, untagged) | — |
| `ImageExists` | `TestImageExists` (real `image inspect`, untagged) | ⤴ |

## egress

The production egress proxy is a stock **squid** sidecar (`config.ProxyImage()`),
not a sandboxer binary — there is no `_proxy` command in the network path. Unit
tests assert the generated `squid.conf` and the create argv; the integration
tests assert the sidecar's actual enforcement behaviour.

| Function(s) | Fake-path test | Real-path test |
|---|---|---|
| `Up` / `UpNamed` / `Down` | `TestUpDownSuccess`, `TestUp*Failure`, `TestUpNamed*`, `TestUpNoDomains` (fail-closed) | `TestUpDown_RealNetworks`, `TestUp_RealEngine_TearsDownOnSidecarFailure` |
| `squidConf` (allowlist, subdomains, CONNECT/443, upstream) | `TestSquidConf`, `TestUpWithUpstream` (conf/argv strings) | see the allowlist row |
| allowlist enforcement (end to end) | — | `TestEgressAllowlist_AllowVsBlock_RealSidecar` (allow vs deny), `TestProxy_SubdomainMatch_RealSidecar` (leading-dot subdomain rule), `TestProxy_NetworkIsolation_RealSidecar` (no direct outbound without the proxy) |
| `Lookup` / `Stop` / `Start` / `ProxyRunning` | `TestLookupDown`, `TestStopStart*`, `TestProxyRunning` | ⤴ via the session lifecycle test |

HTTPS/CONNECT-to-443 is proven by the CLI egress test (below), which probes with
`curl` from the toolbox image — busybox `wget` cannot tunnel HTTPS through a
proxy.

## cli

Command *logic* is exercised extensively through `cli.Run` by the engine-free
unit tests (`cli_*_test.go`, stub engine on `PATH`), which cover `create`,
`enter`, `exec`, `stop`, `rm`, `recreate`, `list`, `use`, `pull`, `push`,
`diff`, `show`, `clean`, `compose`, `doctor`, `hook`, `agents`, `image *` and
`profile *`. The integration tests add the real-engine behaviour those cannot:

| Flow | Real-path test |
|---|---|
| `create → diff → push` with real file vendoring + real `diff(1)` | `TestLifecycle_CreateDiffPush` (engine-free) |
| `create → exec` container round-trip, in-container edit pushed back via the rw mount | `TestLifecycle_Container_ExecPush` |
| persistent session `enter → exec → stop → re-enter → rm` WITH egress on | `TestSessionLifecycle_Container_EnterStopRm` |
| one-shot `exec --ephemeral` WITH egress on: `HTTP_PROXY` injected, allow/deny over HTTP + HTTPS | `TestExec_Container_EgressOn_OneShot` |
| agent env passthrough + host-home isolation | `TestExec_Container_AgentEnvAndHomeIsolation` |
| `recreate` keeps the private home, `recreate --full` wipes it | `TestRecreate_Container_KeepsHome_RealEngine` |

## srcs, sandbox, config, registry, toolbox

| Package | Status |
|---|---|
| `srcs` (`CopyIn`/`CopyOut`) | Real-fs in `srcs_test.go`; also exercised end-to-end by the lifecycle tests. |
| `sandbox` (`ResolveBase`/`MakeSandbox`/…) | Real-fs; exercised by the lifecycle + recreate tests. |
| `config` (`ResolveRuntime`/`ValidateDomains`/`Load*`/`Sanitize`/…) | Pure logic, fully unit-tested; no external-process path. |
| `registry` (`Get`/`Names`/`HeadlessCmd`/…) | Pure command-template rendering; fully unit-tested. |
| `toolbox` (`BuildImage`) | Fake-engine unit test; the real multi-minute nix build (both the toolbox and the squid proxy image) runs only under `SANDBOXER_ITEST_BUILD_IMAGE=1` — `TestBuildImageUserNixContract`. |

## Helper package

`internal/itest` (all files `//go:build integration`) holds the shared real-path
helpers — engine detection/skip (`Engine`), unique naming (`Slug`), leak-safe
teardown (`CleanupContainer`/`CleanupNetwork`), smoke-image gating (`SmokeImage`),
and toolbox/proxy image gating (`EnsureToolboxImage`/`EnsureProxyImage`, which
build on demand only under `SANDBOXER_ITEST_BUILD_IMAGE=1`). It compiles to
nothing without the tag, so it never enters the production binary or the coverage
build.
