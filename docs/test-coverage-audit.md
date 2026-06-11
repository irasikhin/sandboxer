# Test coverage audit

This maps every exported function of the `internal/*` packages to the kind of
test that exercises it, distinguishing the **fake path** (in-process unit tests
with stub engines / `httptest` — these run in CI and feed the 90% coverage gate)
from the **real path** (the `-tags integration` end-to-end suite that drives a
real container engine, real networks and the real egress proxy — opt-in, not in
CI; see [CONTRIBUTING](../CONTRIBUTING.md#integration-tests-opt-in-not-run-in-ci)).

Legend: ✓ covered · ➕ added by this work · — not applicable (pure logic / no
external process) · ⤴ exercised transitively.

## backend

| Function | Fake-path test | Real-path test |
|---|---|---|
| `Run` | `TestContainerRun*` (stub engine) | ➕ `TestRun_RealEngine_NoEgress_ExitAndMount`, `…_ExitCodePropagation`, `…_WallTimeoutKills` |
| `RunArgv` | `TestRunArgv` (string only) | ➕ `TestRunArgv_RealEngineAccepts` (engine parses the argv) |
| `NativeExec` | `TestNativeExecGeneric/ClaudeWrapping` | ➕ `TestNativeExec_RealClaudeWrapping_StubBinary` (real argv/cwd/env) |
| `NativeEnter` | `TestNativeEnter` | ➕ `TestNativeEnter_RealShell_ProxyEnv` |
| `ProxyEnv` | `TestProxyEnv` | — (pure) |
| `ResolveEngine` | `TestResolveEngine` (stub PATH) | ⤴ via real `Run` |
| `DetectEngine` | `TestDetectEngine` | ⤴ |
| `EngineLabel` | **was indirect only** → ➕ `TestEngineLabel` (direct, untagged) | — |
| `ImageExists` | **had no direct test** → ➕ `TestImageExists` (real `image inspect`, untagged) | ➕ |

`EngineLabel` and `ImageExists` were the only exported functions with no direct
test; both gaps are now closed with untagged unit tests (`detect_more_test.go`)
that run in CI and lift `backend`'s coverage.

## egress

| Function | Fake-path test | Real-path test |
|---|---|---|
| `Up` | `TestUpDownSuccess`, `TestUp*Failure` (stub engine) | ➕ `TestUpDown_RealNetworks`, `TestUp_RealEngine_TearsDownOnSidecarFailure` |
| `Down` | `TestUpDownSuccess`, `TestNilSafety` | ➕ `TestUpDown_RealNetworks` |
| `Net` / `ProxyURL` / `Active` | `TestGetters`, `TestNilSafety` | ⤴ |

The allowlist itself is verified end-to-end in
`TestProxyInContainer_RealProxyBinary` (and `…_RealSidecar`): a client on a real
`--internal` network reaches an allowed host through the proxy and is refused a
blocked one.

## proxy

| Function | Fake-path test | Real-path test |
|---|---|---|
| `Allowed` | `TestAllowed*` (pure) | — |
| `New` / `ServeHTTP` / `ListenAndServe` | `proxy_test.go` (`httptest`) | ➕ `TestProxyInContainer_RealProxyBinary` runs the real `_proxy` binary inside a container |

## cli

| Function | Fake-path test | Real-path test |
|---|---|---|
| `Run` | `cli_*_test.go` (stub engine, extensive) | ➕ `TestLifecycle_Native_CreateDiffPushExec`, `TestLifecycle_Container_ExecPush` |

The lifecycle tests drive `create → diff → push → exec` through `cli.Run` with
real file vendoring (`srcs.CopyIn`/`CopyOut`), a real `diff(1)`, and — for the
container variant — a real engine writing through the rw bind mount.

## srcs, sandbox, config, registry, runner, toolbox

| Package | Status |
|---|---|
| `srcs` (`CopyIn`/`CopyOut`) | Already real-fs in `srcs_test.go`; also exercised end-to-end by the lifecycle tests. |
| `sandbox` (`ResolveBase`/`MakeSandbox`/…) | Already real-fs; exercised by the lifecycle tests. |
| `config` (`ResolveRuntime`/`ValidateDomains`/`Load*`/`Sanitize`/…) | Pure logic, fully unit-tested; no external-process path. |
| `registry` (`Get`/`Names`/`HeadlessCmd`/…) | Pure command-template rendering; fully unit-tested. |
| `runner` (`Run`) | Extensively tested with a stub engine and dry-run. A real-container variant is **not** added: `runner.Run`'s container branch is thin orchestration over `backend.Run`, which now has direct real-path coverage. |
| `toolbox` (`BuildImage`) | Fake-engine unit test; the real multi-minute nix build runs only under `SANDBOXER_ITEST_BUILD_IMAGE=1` (documented, not run by default). |

## Helper package

`internal/itest` (all files `//go:build integration`) holds the shared real-path
helpers — engine detection/skip, unique naming, leak-safe teardown, smoke/toolbox
image gating, and a static-binary builder. It compiles to nothing without the
tag, so it never enters the production binary or the coverage build.
