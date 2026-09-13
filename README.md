# sandboxer

[![CI](https://github.com/irasikhin/sandboxer/actions/workflows/ci.yml/badge.svg)](https://github.com/irasikhin/sandboxer/actions/workflows/ci.yml)
[![Coverage](https://img.shields.io/badge/coverage-%E2%89%A590%25-brightgreen.svg)](#testing)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

sandboxer runs disposable, isolated dev sandboxes on your machine. Each
sandbox is a real microVM with your repository mounted, a full toolchain
inside, and its own network policy. Use it as a shell yourself, or let an
agent (claude, dsh, pi, and others) work in it. The sandbox edits files in
host-side git worktrees on branches you named; you review and commit the
result with ordinary git.

> Experimental personal project, not a product. Pre-1.0: CLI flags, the
> `sandboxer.nix` schema and the on-disk layout may change between minor
> versions. Breaking changes are called out in the changelog. Use at your own
> risk.

## What it gives you

- **Isolation.** Every sandbox is its own microVM (libkrun: KVM on Linux,
  Hypervisor.framework on macOS), not a container. See
  [docs/microvm.md](./docs/microvm.md).
- **Your repo inside.** Each source becomes a git worktree under
  `./sandboxes/` in your project. The sandbox sees only the directories you
  select; git itself stays out unless a source opts in.
- **A ready environment.** The prebuilt toolbox image carries the agents,
  python/node/jdk, podman with a `docker` shim and compose, tmux, and the
  everyday CLI tools. No per-sandbox provisioning.
- **A fence around the network.** Outbound traffic is default-deny behind a
  name-bound domain allowlist; inbound only through ports you publish.
- **Ordinary git review.** Sandbox edits land live in the host worktrees.
  Nothing is copied in or out, and your own checkout is never touched.

## Requirements and install

Three host requirements, none bundled:

- **nix** evaluates `sandboxer.nix` and builds customized toolbox images.
  A hard requirement of the CLI.
- **microsandbox** (`msb`), the microVM runner, from
  <https://microsandbox.dev>. `SANDBOXER_MSB` overrides the looked-up path.
- **`/dev/kvm`** on Linux (usually the `kvm` group). macOS uses
  Hypervisor.framework; Windows runs inside WSL2 with nested KVM. Both
  compile but are not yet live-verified: [docs/macos.md](./docs/macos.md),
  [docs/windows.md](./docs/windows.md).

`sandboxer doctor` checks all three.

```bash
nix run    github:irasikhin/sandboxer -- help                  # try without installing
nix profile install github:irasikhin/sandboxer                 # Nix; adds an `sb` alias + completions
go install github.com/irasikhin/sandboxer/cmd/sandboxer@latest # Go
```

Prebuilt binaries (linux amd64/arm64):
<https://github.com/irasikhin/sandboxer/releases>.

## Quick start

```bash
sandboxer create feat               # worktree on branch feat/feat, boots the machine
sandboxer enter  feat               # interactive shell (tmux); Ctrl-Space d detaches
sandboxer exec   feat -- claude     # run a command or an agent inside
git log feat/feat                   # review the work on the host, commit with plain git
sandboxer list                      # every sandbox on the host (alias: status)
sandboxer stop   feat               # park the machine; enter resumes it
sandboxer rm     feat               # delete sandbox + machine; the branch stays
```

The first run scaffolds a commented `sandboxer.nix` with the whole repo as
one source. `srcs` is always explicit: an empty list is an error.

## The model

- **Sandbox:** a set of sources, materialized as git worktrees under
  `./sandboxes/<slug>/<branch>/<repo>/` (auto-added to the project's
  `.gitignore`; relocatable via the profile's `worktreesDir`).
- **Sources (`srcs`):** always explicit. Each entry has:
  - `src`: a repo path relative to the project root, or a git URL
    (`https://`, `ssh://`, `git@host:path`, …), cloned once into a host-side
    cache and re-fetched by `recreate`.
  - `branch`: required. Names the worktree's on-disk directory. A branch
    you already have checked out in a worktree is adopted (shared at its
    host path); the repository's own checkout or another sandbox's tree is
    refused.
  - `include`: the directories the sandbox sees. Literal anchored paths or
    ant-style directory patterns (`/services/*/`, `**/proto/`). Patterns
    match directories only; zero matches is an error. See
    [docs/view-mounts-design.md](./docs/view-mounts-design.md).
  - `git`: `"ro"` or `"rw"`, per source: share that repo's git dir so git
    works inside the sandbox. Not combinable with `include`. `rw` also hands
    over `.git/hooks` and `.git/config`; see [SECURITY.md](SECURITY.md).
- **What `include` narrows.** `include` narrows the guest's mount set, never
  the worktree: the host tree is always a complete checkout, so an IDE can
  open it. A directory that is not listed is not mounted, and does not exist
  inside the sandbox.
- **Review.** On the host, per source: `git -C <repo> log <branch>`, commit
  in the worktree, merge. The sandbox has no git access unless a source opts
  in with `git:`.
- **State split.** The config is one committed file, `sandboxer.nix`.
  Worktrees live in the project (`./sandboxes/`). Everything else (private
  agent homes, logs, machine records) lives outside the repo under
  `$XDG_STATE_HOME/sandboxer/<project-id>`, so credentials can never be
  committed. `sandboxer clean` wipes the data, never the config. Teardown
  keeps branches; a dropped source with uncommitted work is set aside under
  `_detached/` instead of deleted. Deleting `./sandboxes/` by hand
  self-heals on the next `enter`.

sandboxer is **git-only**: a non-git source is rejected with an init hint.
Non-git trees come in via `extraMounts`.

## Config: sandboxer.nix

One committed nix attrset, auto-discovered in the current directory (`-f`
points at another file). It is evaluated by the host nix under a restricted
eval: no network, no reads outside its directory.

```nix
{
  name = "feature-x";
  egress.allowedDomains = [ "api.anthropic.com" "registry.npmjs.org" "github.com" ];
  srcs = [
    { src = "."; branch = "feat/feature-x"; include = [ "/src/lib/" "/shared/proto/" ]; }
  ];
  setup = '' npm ci '';           # one-time prep, run inside the sandbox
  tools = [ "node" "go" ];        # runtime packs baked into a per-profile image
}
```

| Field | What it does |
|---|---|
| `srcs` | the sources (see above). Edits apply on the next enter/exec; a running session sees them live |
| `extraMounts` | non-git trees mounted read-only |
| `env` | extra environment variables |
| `ports` | published ports (see [Network](#network)) |
| `setup` | one-time `bash -lc` script, run before you take over. Re-runs when its content changes; runs under the same egress as the sandbox; `--no-setup` skips it |
| `tools` | language packs (`node`, `python`, `go`, …) baked into a per-profile image variant |
| `image` | a prebuilt image ref or customization (`packages`, `files`, `env`, `overlay`); see [Toolbox image](#toolbox-image) |
| `limits` | machine size: `memory`, `cpus`, `disk` (see [Resource limits](#resource-limits)) |
| `hostConfigs` | seed the sandbox home with your host agent configs (default on) |
| `autoResume` | restore agent panes when a session reattaches (default on) |

Several profiles live in one file under a `profiles` attrset; `default`
names the one used when `create` gets no name. Reuse between sections is
ordinary nix (`let` + `//`). See `examples/multi-profile.nix`.

`sandboxer config edit` opens the file in `$EDITOR`; `sandboxer config
validate` checks the schema strictly: an unknown or retired key fails with a
precise message. Existing sandboxes pick changes up on their next
enter/exec.

> The config evaluates on your host, and `setup` and the image `overlay`
> run arbitrary code. That is fine for your own configs; treat someone
> else's `sandboxer.nix` like a shell script and read it first.

### Resource limits

```nix
limits = { memory = "8G"; cpus = 4; disk = "40G"; };
```

Defaults are 2 vCPU / 4 GiB / 20 GiB root disk. `cpus` takes a whole vCPU
count or a systemd-style quota (`200%`); fractional counts and unparseable
sizes are rejected up front rather than silently rounded. The 20 GiB root
disk is a sparse image: it costs host space only as the guest writes. A
microVM has no swap, so the memory cap is a hard ceiling: a workload that
exceeds it is OOM-killed with a bare `Killed`. See
[docs/troubleshooting.md](./docs/troubleshooting.md#a-process-inside-the-sandbox-dies-with-killed).

### Environment variables

Scalars come from flags and `SANDBOXER_*` env vars:

| Setting | Flag | Env |
|---------|------|-----|
| egress domains | `--allow-domains a,b` | `SANDBOXER_DOMAINS` |
| session mode | `--ephemeral` | `SANDBOXER_SESSION` (default `persistent`) |
| published ports | `-p/--port 8080:3080` | `SANDBOXER_PORTS` (csv) |
| disable egress | — | `SANDBOXER_NO_EGRESS=1` |
| disable port forwards | — | `SANDBOXER_NO_PORTS=1` |
| disable agent auto-resume | — | `SANDBOXER_NO_RESUME=1` |
| disable baked-in pi packages | — | `SANDBOXER_NO_PI_PACKAGES=1` |
| skip auto-scaffold | — | `SANDBOXER_NO_SCAFFOLD=1` |
| msb binary | — | `SANDBOXER_MSB` (default: `msb` from `PATH`) |
| image | — | `SANDBOXER_IMAGE` (default: the prebuilt toolbox image) |
| resource caps | — | `SANDBOXER_MEM` / `SANDBOXER_CPU` / `SANDBOXER_DISK` |

## Network

### Egress

Outbound traffic is default-deny, fenced by the microVM's own network policy
engine; no sidecar, nothing of sandboxer's in the network path. With the
allowlist on (the default), the machine boots with no route at all, plus one
name-bound rule per domain in `egress.allowedDomains`: the domain and its
subdomains over HTTP and HTTPS, nothing else. Rules match by name, so a raw
IP dial is refused even for an allowed domain's own address.

- `allowedDomains = [ ]` means a fully offline machine, DNS included. Delete
  the attr to get the built-in defaults (AI APIs, package registries,
  container registries and their CDNs).
- `egress.enabled = false` (or `SANDBOXER_NO_EGRESS=1`) opens the network.
- `egress.proxy` points the guest's HTTP(S) clients at one proxy. A loopback
  proxy URL is rewritten to `host.microsandbox.internal`. Setting a proxy
  next to an allowlist prints a warning: the proxy enforces the allowlist.

The policy is part of the machine's create argv, so editing egress recreates
the session on the next enter. Full detail:
[docs/microvm.md](./docs/microvm.md#egress).

### Published ports

`ports` is the sandbox's only inbound path, off unless you ask:

```nix
ports = [ "3080" "8080:3080" "0.0.0.0:8080:3080" "5353:53/udp" ];
```

or per run: `sandboxer enter feat -p 8080:3080` (the flag replaces the
profile's list). A port binds `127.0.0.1` by default; a non-loopback bind
prints a warning. A host port already in use is caught before the machine is
built. The server inside must listen on `0.0.0.0` (or `::`), not on the
guest's own loopback: the forward lands on the guest's `eth0`. dsh's web UI
is pre-adapted to bind correctly. See
[docs/architecture.md](./docs/architecture.md#ingress-published-ports).

## Sessions

`enter` attaches tmux inside a persistent machine (`tmux -L sandboxer`, mouse
on, sandboxer prompt in every pane). The prefix is `Ctrl-Space`; `Ctrl-b`
works as a second prefix, `Alt-d` and the `detach` command need no prefix at
all. **`Ctrl-Space d` detaches**: the tmux session and everything running in
it keep going, and the next `sandboxer enter feat` drops back in. `exit`
ends the tmux session, not the machine. The status bar names the sandbox you
are in.

`exec` reuses a running session; `stop` parks the machine for a later
resume; `rm` removes it with the sandbox. `list` shows every sandbox on the
host, current project first; the `ID` column is a host-wide handle, so any
command that takes a slug also takes an id or an unambiguous prefix.

When the profile changes in a way that shapes the machine (egress, ports,
limits, image), the next `enter` recreates the session, unless it still
holds a tmux session, in which case `enter` attaches as-is and says so.
`stop <slug> && enter <slug>` applies the change right away. Across a
rebuild, the saved tmux layout is restored and panes that ran an agent
relaunch it with its resume command. Opt out with `autoResume = false` or
`SANDBOXER_NO_RESUME=1`. One-shot machines: `--ephemeral` or
`SANDBOXER_SESSION=ephemeral`. Design notes:
[docs/sessions-design.md](./docs/sessions-design.md).

## Review and reset

When a sandbox's branch has been merged, re-base it onto the merged
default:

```bash
sandboxer reset feat                       # every source onto origin/main
sandboxer reset feat api                   # one source only
sandboxer reset feat --onto origin/master  # a repo whose default differs
git -C "$(sandboxer path feat)" push -u origin HEAD
```

`reset` stays on the sandbox branch (it never touches the base branch
checked out in your main repo), refuses a source with uncommitted work
unless `--force` (checked across the whole sandbox before anything moves),
and skips adopted worktrees. It is `git fetch` + `reset --hard` underneath;
a live session sees the new base immediately.

## Nested containers

The image ships **podman** with a `docker` shim and `podman-compose`, so
`docker run/build/compose` work inside the sandbox. The engine runs natively
against the guest's own kernel: full uid range, no opt-in, no seccomp/subuid
machinery. No engine socket ever comes from the host; inside, the guest's
own engine serves a docker-compatible socket at `/var/run/docker.sock` with
`DOCKER_HOST` set, so testcontainers suites run with zero configuration.
Pulls go through the egress allowlist: the defaults cover docker.io,
ghcr.io, quay.io and their blob CDNs.

## Local Kubernetes

The image ships a full local-cluster toolchain — `kubectl`, `helm`,
`kustomize`, `kind`, `k3d`, `k9s`, `kubectx`/`kubens`, `stern` and
`kubeconform` — so a sandbox can boot its own cluster instead of reaching for
the host's:

```bash
kind create cluster          # single-node k8s (kind's podman provider is preset)
k3d cluster create dev       # k3s, through the docker-compatible socket
helm install ...             # then drive it with the usual clients
```

Both runners drive the **guest's own podman**: kind through its podman
provider (its default provider is the docker CLI/daemon, so the image exports
`KIND_EXPERIMENTAL_PROVIDER=podman`), k3d through the docker-compatible API
socket at `/var/run/docker.sock`. A node is a privileged container on the
guest kernel, so the cluster and its images live and die with the sandbox —
`kubectl` inside finds it via the kubeconfig in the persistent sandbox home,
`sandboxer rm` removes it, and the host's docker or Kubernetes is never in
reach. The sandbox root is itself an overlayfs, which the kernel will not let
another overlayfs use as an upperdir, so both runners are pointed away from
containerd's default overlayfs snapshotter (kind via
`KIND_EXPERIMENTAL_CONTAINERD_SNAPSHOTTER=fuse-overlayfs`, k3d's k3s via a
thin `k3d` wrapper that pins `--snapshotter=native` on `cluster create`);
overriding either from the outside still works. The default egress allowlist
already carries `registry.k8s.io`, the Kubernetes project's registry, so the
usual addons (metrics-server, ingress-nginx) install without touching the
config. Give the machine room (`limits.memory`): the default 4 GiB fits a
single-node kind cluster and its first workload, but a multi-node or
workload-heavy one wants more. To reach a service from the host, keep using
the sandbox's `ports:` forward — the cluster's own NodePorts stay inside the
sandbox.

## Agents

`sandboxer agents` prints the catalog. The single source of truth is
`internal/registry/registry.json`, embedded in the binary and read by the
flake when building the image. Adding an agent is one JSON entry.

**dsh** (DeepSeek Harness) has no TUI: use `dsh --profile headless "job"`,
or `dsh web` with `ports = [ "3080" ]` to open its browser UI on the host.

With `hostConfigs = true` (the scaffold default), the sandbox home is seeded
with a copy of your host agent configs (`~/.claude`, `~/.codex`, `~/.gemini`,
`~/.dsh`, opencode/crush). The copy is never mounted and never written back;
your in-sandbox edits always win. The agents' auth env vars set on the host
are passed through too. Claude's rotating OAuth file is deliberately not copied:
run `claude setup-token` once and export `CLAUDE_CODE_OAUTH_TOKEN`, or
`/login` inside the sandbox.

pi ships with its orchestration package pre-registered in each sandbox
(`piPackages = false` or `SANDBOXER_NO_PI_PACKAGES=1` opts out).

## Toolbox image

The default image is the prebuilt
`ghcr.io/irasikhin/sandboxer-toolbox:latest`, republished nightly and tagged
per release. msb pulls and caches it host-side on first use (the pull honors
`HTTP(S)_PROXY`).

```bash
sandboxer image pull        # refresh a moved `latest`
sandboxer image build       # build locally with host nix (customized profiles, offline hosts)
sandboxer image rm          # drop the cached image and build tars
```

Per-profile customization (`image.packages` / `files` / `env` / `overlay`,
plus the `tools` packs) is content-addressed: profiles produce a
`sandboxer-toolbox:var-<12hex>` variant, auto-built on first use and shared
by identical profiles. The nixpkgs input tracks the remote head; pin it with
`image.nixpkgsRev`. A profile can also name a prebuilt ref (`image.ref`) and
run exactly that. Full detail:
[docs/architecture.md](./docs/architecture.md#toolbox-image-prebuilt-local-builds-with-host-nix).

## direnv

`sandboxer hook direnv` prints `export` lines naming the active sandbox (the
one set by `sandboxer use <slug>`):

```sh
# .envrc
eval "$(sandboxer hook direnv)"
```

It exports `SANDBOXER_SLUG`, `SANDBOXER_SRC`, and (when recorded)
`SANDBOXER_BACKEND` and `SANDBOXER_ALLOW_DOMAINS`. The hook only reads
already-persisted state; outside a sandboxer project it emits nothing.
Run `direnv reload` after `sandboxer use <slug>`: the marker lives outside
the repo, so direnv cannot watch it.

## Docs

- `sandboxer --help` / `sandboxer <cmd> --help` — commands, flags, examples
- [docs/architecture.md](./docs/architecture.md) — how it works: on-disk layout, lifecycle, image build, egress, registry
- [docs/troubleshooting.md](./docs/troubleshooting.md) — common problems and fixes
- [CONTRIBUTING.md](./CONTRIBUTING.md) — dev setup, Conventional Commits, releases
- [SECURITY.md](./SECURITY.md) — isolation model, vulnerability reporting
- [CHANGELOG.md](./CHANGELOG.md) — what's in each release

## Testing

```bash
go test ./...                       # all tests
go test ./... -cover                # per-package coverage
go test ./... -coverprofile=cov.out # write a profile
go tool cover -func=cov.out | tail -1
```

CI enforces a 90% total coverage gate on every push and PR. The real-msb
integration suite (`-tags integration`) runs separately; see
[CONTRIBUTING.md](./CONTRIBUTING.md#integration-tests). Backend tests pin
pure argv builders and run without a hypervisor; tests that need git or KVM
skip cleanly when the tool is absent.

## License

MIT; see [LICENSE](./LICENSE).
