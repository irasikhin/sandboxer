# sandboxer

[![CI](https://github.com/irasikhin/sandboxer/actions/workflows/ci.yml/badge.svg)](https://github.com/irasikhin/sandboxer/actions/workflows/ci.yml)
[![Coverage](https://img.shields.io/badge/coverage-%E2%89%A590%25-brightgreen.svg)](#testing)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Run **several autonomous coding agents in parallel** — or work by hand in a
single sandbox — each fully isolated in its **own microVM**, on your **local
Linux machine**. A Go CLI, shipped as a static binary, `go install`, or a Nix
flake. Human designs, AI drives.

> 🧪 **Experimental — a personal project, not a product.** sandboxer is in
> **active development** and is **not intended for production use**. It's my
> personal tool for running AI coding agents locally, shipped with no stability
> or support guarantees. Use at your own risk.

> ⚠️ **Pre-1.0.** CLI flags, the `sandboxer.nix` schema and the on-disk layout may change
> between minor versions until 1.0. Sandboxes expose **sources** — git repos
> checked out into host-side worktrees, narrowed by `srcs` include patterns
> (see below). Any future change will be called out in the changelog.

## How it works

A **sandbox** exposes **sources**: git repos checked out into per-sandbox
worktrees right in the project — `./sandboxes/<slug>/<branch>/<repo>`,
grouped by branch with a directory per repo inside, so your
sandboxes are ordinary folders you can browse. The dir is auto-added to the
project's `.gitignore` (worktrees never land in a commit); relocate it with
the profile's `worktreesDir` (absolute, `~`, or project-relative). Run sandboxer inside a repo and it just works — the
scaffolded config pins the whole repo as the one source, on an explicit
branch (always explicit, never implied). The
sandbox sees **only the files the sources select — git itself never enters
it**: no `.git` shares, no history, no hooks. The agent edits files; the edits
land live in the host-side worktree, where you review and commit them with
plain git. Your working tree and current branch are never touched, and nothing
is copied.

- **Sandbox** — a set of sources materialized as git worktrees under one dir.
- **slug** — a short sandbox name (`feat`, `bugfix-auth`, …), set at `create`.
- **srcs** — the sources, always explicit: each entry is `src:` (path to a
  repo root — relative to the project root, so other repos work too — **or a
  git URL**), a REQUIRED `branch:` (the branch the worktree lives on — it also
  names the directory the worktree sits under; a branch already checked out elsewhere is
  adopted as-is) and an optional `include:` (directory paths or patterns — only
  the selected directories exist in the sandbox). The scaffold seeds
  `srcs = [ { src = "."; branch = "feat/<name>"; } ]` — this repo, whole.
  Editing srcs applies on the next `enter`/`exec` — a running session sees the
  change live, no recreate.
- **remote srcs** — a `src` that is a git URL (`https://`, `ssh://`, `git://`,
  `file://`, or `git@host:org/repo`) is **cloned once** into a host-side cache
  under the state dir and worktree'd from there, exactly like a local repo. The
  clone uses your host git credentials and never enters the sandbox;
  `branch:` checks out that remote branch. `recreate` re-fetches; `clean` wipes
  the cache.
- **review** — on the HOST, per source repo: `git -C <repo> log <branch>`,
  `git add`/`commit` in the worktree, then merge or cherry-pick (for a remote,
  `git -C <repo> push origin <branch>`).

sandboxer is **git-only**: every `src` must be a git repo (a local one needs at
least one commit — `git init && git add -A && git commit -m init`). Non-git
trees come in via `extraMounts`.

Isolation — every sandbox is a real **microVM** on
[microsandbox](https://microsandbox.dev) (`msb`, libkrun: KVM on Linux, HVF on
macOS): its own guest kernel behind a hardware-virtualization boundary, booted
from the toolbox image; see [docs/microvm.md](./docs/microvm.md). The
image has the agents baked in (claude, opencode, crush, aider, pi, gemini) plus an
everyday toolchain: python3, node/npm, jdk+maven, redocly, ripgrep/fd/jq/…,
tmux (auto-attached by `enter`), and **docker/podman for nested containers** —
they run natively against the guest's own kernel, no opt-in and no engine
socket from the host (pulls go through the egress allowlist, whose defaults
include docker.io/ghcr.io/quay.io and
mirror.gcr.io). Each sandbox gets its own isolated home, and network/proxy
are wired per config. Agent auth is yours to choose: with `hostConfigs = true`
(the scaffolded default) the sandbox home is seeded with a COPY of your host
agent configs — `~/.claude` (settings, skills, memory) + `~/.claude.json`,
`~/.codex`, `~/.gemini`, opencode/crush/aider — transcripts/caches excluded,
never mounted, never written back, and your in-sandbox edits always win; the
agents' auth env vars set on the host (`ANTHROPIC_API_KEY`,
`CLAUDE_CODE_OAUTH_TOKEN`, …) are passed through as well. Claude's rotating
OAuth file is deliberately not copied — a copy goes 401 on the next refresh
either side performs: for subscription auth run `claude setup-token` once and
export `CLAUDE_CODE_OAUTH_TOKEN`, or `/login` once inside the sandbox (its
private $HOME persists). Without `hostConfigs`, credentials never come from
the host at all.

For the full picture — on-disk layout, sandbox lifecycle, how the toolbox image
is built and cached, the egress policy and the agent registry — see
[docs/architecture.md](./docs/architecture.md). Hitting a wall? See
[docs/troubleshooting.md](./docs/troubleshooting.md).

## Install

Three host requirements, none bundled:

- **nix** — a hard requirement of the CLI (it evaluates `sandboxer.nix` and
  builds the toolbox image).
- **microsandbox** (`msb`) — the microVM runner, from
  <https://microsandbox.dev> (`SANDBOXER_MSB` overrides the looked-up path; on
  NixOS use this flake's `microsandbox` package — see
  [docs/microvm.md](./docs/microvm.md)).
- **`/dev/kvm`** on Linux (typically the `kvm` group). On macOS (Apple
  Silicon) msb uses Hypervisor.framework instead; on Windows run everything
  inside WSL2 with nested KVM — both paths are **cross-platform in code but
  not yet live-verified**, see [docs/macos.md](./docs/macos.md) /
  [docs/windows.md](./docs/windows.md).

`sandboxer doctor` checks all three.

```bash
nix run    github:irasikhin/sandboxer -- help                   # try without installing
nix profile install github:irasikhin/sandboxer                  # Nix
go install github.com/irasikhin/sandboxer/cmd/sandboxer@latest  # Go
```

Or grab a [pre-built binary](https://github.com/irasikhin/sandboxer/releases)
(linux amd64/arm64).

The Nix install also puts a short alias **`sb`** on your `PATH` (with shell
completions) — `sb create`, `sb enter`, `sb exec …` all work exactly like
`sandboxer`. (A `go install` or a raw binary gives you just `sandboxer`; symlink
`sb` yourself if you want the shorthand.)

## Quick start

```bash
sandboxer config init                     # scaffold a commented sandboxer.nix to edit (optional)
sandboxer create feat                     # create a sandbox named "feat" (worktree on branch feat/feat)
sandboxer enter  feat                     # attach a shell (persistent session; Ctrl-Space d detaches)
sandboxer exec   feat -- claude           # run an agent/command inside it
git log feat/feat                      # the work is an ordinary branch (commit it on the host)
sandboxer stop   feat                     # park the session machine (enter resumes it)
sandboxer list                            # every sandbox on the host, all projects (alias: status)
sandboxer rm     feat                     # delete the sandbox and its session (keeps the branch)
```

A config file is optional — one is scaffolded on first use, seeding the whole
repo as the single source (`srcs` itself is never implicit: an empty list is an
error). To narrow the sandbox or add
setup/tools/env, edit the `sandboxer.nix` in the cwd (auto-discovered) or pass
another file with `-f`; several profiles live in ONE file under a `profiles`
attrset and are picked by name (`create <name>`). The sandbox slug comes from
the profile's `name`.

Commands fall into four `--help` groups: forming the **image**
(`sandboxer image build|rm`) and **config** (`sandboxer config
init|edit|validate`, plus `sandboxer profile list|use` for picking one),
**entering/working** in the sandbox (`create` / `enter` / `exec` / `stop` /
`recreate` / `reset` / `rm` / `list` / `use`), inspecting **data** (`clean` /
`show` / `path`), and the rest (`agents` / `doctor` / `hook`).

## Config vs data

The committed config is ONE file at your repo root — `sandboxer.nix`,
image-customization hook included — checked in as-is. It is a nix attrset,
evaluated by the **host nix** under a restricted eval (no network, no reads
outside its directory); nix on the host is a hard requirement. The sandbox worktrees live in the
project's `./sandboxes/` (auto-git-ignored; relocatable via the profile's
`worktreesDir`); the rest of the runtime state (the private agent homes, logs
and metadata) lives under the XDG state dir
(`$XDG_STATE_HOME/sandboxer/<project>`, default `~/.local/state/...`), outside
the repo. Secrets and scratch data can never be committed. `sandboxer clean`
wipes both for the project (the config stays; a user-chosen worktrees dir is
never removed wholesale — only the sandbox dirs in it); a dropped source's
worktree is removed when clean (its commits live on the branch, which is
kept), and set aside under `_detached/` only when it holds uncommitted work —
sweep those with `sandboxer clean --detached --force` once reviewed, or name
the branch in `srcs` again and the worktree is re-attached, work intact.
Deleting `./sandboxes/` by hand is fine too: the next `enter` prunes the stale
git registrations, checks the branches out fresh and rebuilds the session
machine (uncommitted work is the only thing an `rm -rf` forfeits).

## How changes flow

Changes flow through git — on the host. Each source is a **git worktree** on
the branch you configured (`srcs branch:`); the sandbox's edits appear
there live (virtio-fs share), and **you** commit/review them with plain git
(`git -C <worktree> add/commit`, `git log`/`git diff`/`git merge <branch>`).
The sandbox itself has no git access at all: no object store, no hooks, no
history. There is no copy-in and no push-back. Teardown (`rm`, `recreate`)
keeps the branches; `recreate --full` also deletes the branches sandboxer
itself created, for a fresh start (never one that existed before the
sandbox).

### Continuing after a merged PR

When a sandbox's branch has been merged and its remote branch deleted, re-base
the same branch onto the freshly-merged default:

```bash
sandboxer reset feat                   # fetch + move every source's branch onto origin/main
sandboxer reset feat api               # just the "api" source
sandboxer reset feat --onto origin/master   # a repo whose default differs
git -C "$(sandboxer path feat)" push -u origin HEAD   # re-create the remote branch for the next PR
```

`reset` stays ON the sandbox branch (never a `git checkout main`, so the base
branch checked out in your main repo is never contended), refuses a source
with uncommitted work unless `--force` (checked across the whole sandbox
BEFORE anything moves — never a half-reset), and skips adopted worktrees. A
live session sees the new base immediately (the worktree is a live share), so
no `recreate` is needed. It is plain `git fetch` + `reset --hard` under the
hood — the manual equivalent is always available via `sandboxer path`.

## Persistent sessions

By default `enter` attaches a **tmux session inside a persistent session
machine** (`tmux -L sandboxer`, mouse scrolling on, sandboxer prompt in
every pane; the prefix is `Ctrl-Space`, so `Ctrl-Space c` opens a new window):
**`Ctrl-Space d` detaches** — the tmux session and whatever is running in it
keep going, and a later `sandboxer enter feat` drops straight back in; a second
terminal attaches the same session in parallel (`--session <name>` opens a
separate one in the same machine). **Exiting the shell is not the same
thing**: it closes the session's last pane, which ends the tmux session (and,
with tmux's default `exit-empty`, the in-guest server), so the next `enter`
opens a fresh one. The machine survives either way — it is the tmux session
that does not. Detach when you want to come back to a running agent. The status bar names the sandbox you are in and
flips to `PREFIX` while the prefix is armed:

```text
 ▌ sbx feat-auth   main   1·claude  2·shell                     14:32   20 Jul
```

It is a Catppuccin Mocha bar drawn with Block Elements only — no powerline
separators, because those glyphs need a patched Nerd Font on *your* terminal
and degrade to tofu boxes without one. `Ctrl-Space r` reloads the config.
Yanks travel to the host clipboard over OSC 52, and `escape-time` is 10ms so
ESC stays instant inside vim and the agent TUIs. `exec` reuses a running session;
`stop` parks the machine for a later resume; `rm` removes it along with the
sandbox. `list`'s STATE column shows `running`/`stopped`/`-` per sandbox, and
the listing is **host-wide**: every project sandboxer holds state for, current
project first (`*` marks its active sandbox, `!` a project whose directory is
gone), because the sandbox worth being reminded of is the one in a repo you are
not standing in. Project paths print in full — they shorten only when the
terminal cannot fit them (never when the output is piped), and `-w` stops
truncating altogether. The `ID` column is each sandbox's **host-wide handle**:
any command that takes a slug takes an id — or any unambiguous prefix — and acts
on that sandbox in its own project, with no `cd` and no `--src`
(`sandboxer rm 9f0e 3c4d` removes two sandboxes wherever they live, and is the
only way to clear a `!` row, whose project directory no longer exists). A bare
slug still means the current project, and `--src <path>` narrows the listing
back to one. When
the profile changes or the toolbox image is rebuilt, the next `enter` recreates
the session (announced) — unless that session is still holding a tmux session,
in which case `enter` attaches to it as-is rather than rebuilding it under
whatever is running, and says so. The new configuration then lands on the next
`enter` after that session is empty, or right away with
`sandboxer stop <slug> && sandboxer enter <slug>`.

Escape hatches back to one-shot machines: `--ephemeral` (enter/exec),
`session: ephemeral` in the profile, or `SANDBOXER_SESSION=ephemeral` (the env
wins over the profile — an operator kill-switch). A session survives client
disconnects with everything in it running. Across a machine **replacement**
(profile change, image rebuild, host restart) the tmux layout is saved
to the host — after every `enter` and before any teardown — and restored on
the next `enter`: panes come back as shells in their old directories, and a
pane that was running a cataloged agent relaunches it with its resume command
(`claude --continue`), which picks the conversation back up. `sandboxer agents`
prints each agent's resume command; one that has none restores as a plain
shell. When several panes ran the same agent in the same directory, each gets
the agent's conversation picker (`claude --resume`) instead — resuming "the
latest" in all of them would open one conversation many times. Opt out of the
relaunch with `autoResume = false` in the profile, or `SANDBOXER_NO_RESUME=1`.
Design notes: [docs/sessions-design.md](./docs/sessions-design.md).

## Config

Scalars come from **flags** and `SANDBOXER_*` env vars:

| Setting | Flag | Env |
|---------|------|-----|
| backend | `--backend` | `SANDBOXER_BACKEND` (default and only value: `microsandbox` — a real microVM per sandbox, [docs/microvm.md](./docs/microvm.md); retired values error with a migration hint) |
| session mode | `--ephemeral` | `SANDBOXER_SESSION` (default `persistent`; the env wins over a profile's `session:`) |
| egress domains | `--allow-domains a,b` | `SANDBOXER_DOMAINS` |
| disable egress | — | `SANDBOXER_NO_EGRESS=1` |
| disable agent auto-resume | — | `SANDBOXER_NO_RESUME=1` (or `autoResume = false` in the profile) |
| skip auto-scaffold | — | `SANDBOXER_NO_SCAFFOLD=1` (create/enter writes a default `sandboxer.nix` otherwise) |
| msb binary | — | `SANDBOXER_MSB` (default: `msb` from `PATH`) |
| image | — | `SANDBOXER_IMAGE` (default `sandboxer-toolbox:latest`) |
| resource caps | — | `SANDBOXER_MEM` / `SANDBOXER_CPU` (or the profile's `limits:` — see below) |

The sandbox machine's resource caps come from the profile's `limits:` block
(`memory`, `cpus`), overriding the `SANDBOXER_MEM`/`SANDBOXER_CPU` env
defaults. A microVM must be given a size, so empty means the deliberately
modest default — **2 vCPU / 4 GiB** (several agents run in parallel); raise it
per profile. `cpus` takes a whole vCPU count (or a systemd-style quota like
`200%`); a fractional count or an unparseable memory size is rejected up
front rather than silently rounded.

Structured fields (`srcs`, `extraMounts`, `env`, `setup`, `tools`, `image`, `limits`) live in an **optional**
`sandboxer.nix`. With nothing given, the `sandboxer.nix` in the cwd is
auto-discovered; `-f`/`--config` points at another config **file**. Several
profiles live in one file under a `profiles` attrset. See `examples/config.nix`,
`examples/with-srcs.nix` and `examples/multi-profile.nix`.

> `sandboxer.nix` is meant to be **committed** with your repo — don't
> gitignore it (`sandboxer doctor` warns when a rule hides it).

```nix
{
  name = "feature-x";
  egress.allowedDomains = [ "api.anthropic.com" "registry.npmjs.org" "github.com" ];
  srcs = [                  # the sources the sandbox sees (this repo, narrowed)
    { src = "."; branch = "feat/feature-x"; include = [ "/src/lib/" "/shared/proto/" ]; }
  ];
  setup = ''                # one-time prep, run once inside the sandbox
    npm ci
  '';
  tools = [ "node" "go" ];  # runtime tool packs baked into a per-profile image
}
```

Each `srcs` entry is a repo (`src` — `.`, a path to another repo's root, or a
**git URL** that is cloned once into a host-side cache and re-fetched by
`recreate`) narrowed by `include` — **a list of directories** (`/src/proto`,
`/shared/lib`; anchored at the repo root, trailing slash optional; omit for the
whole repo — or an ant-style directory pattern: `/services/*/`, `**/proto/`,
where a whole `**` segment matches any depth) — and pinned to a **required**
`branch` — for a local repo, naming a branch whose worktree already exists
(even your main checkout) **adopts** it instead of creating one; for a remote,
it checks out that branch from `origin`. Editing `srcs` applies on the next
`enter`/`exec` and is visible to a **running** session immediately. To bring in
**non-git** trees, use `extraMounts`.

```nix
srcs = [
  { src = "."; branch = "feat/api"; include = [ "/services/api/" ]; }
  { src = "https://github.com/org/proto"; branch = "main"; }   # remote → cloned
  { src = "git@github.com:org/lib"; branch = "next"; }         # remote branch
];
```

`include` narrows **what the sandbox sees, and nothing else**: the host's
worktree is always a complete checkout, so your IDE opens the branch and
indexes it normally. The narrowing is enforced by sharing only the listed
directories into the guest — what is not listed is not shared, and
therefore does not exist inside. This is why `include` selects directories
rather than files: a file set is something a mount cannot express — mounting
files one by one would break atomic saves (write-temp + rename over a
mountpoint fails). Patterns are matched against *directory names only*
(`**/proto/` means every `proto/` directory at any depth; a pattern matching
nothing is an error), so file-granular mounts stay impossible; a negation
(`!/vendor/`) or unanchored path is refused by `sandboxer config validate`
with the form to use instead.

`setup` is a one-time shell script (`bash -lc`) run inside the sandbox before
you take over — e.g. `npm ci`, a build, a DB seed. It runs on the first
`enter`/`exec` and again only when the script changes (a per-sandbox
stamp tracks it), under the **same egress allowlist** as the sandbox (so a
network install needs its domains allowed). A failed setup is fatal by default;
skip it with `--no-setup`. The baked shell can also be extended without
rebuilding the image: drop `*.sh` files in `/etc/sandboxer/rc.d/` (image
plugins) or write `~/.config/sandboxer/rc` (per-sandbox `$HOME`).

> ⚠️ The config itself **evaluates on your host** (restricted: no network, no
> reads outside its directory), and `setup` / the image `overlay` run
> **arbitrary code** — setup inside the sandbox under its egress allowlist,
> the overlay at image-build time with host nix. That is the
> intended trust level for *your own* configs; treat a third-party
> sandboxer.nix like a shell script someone sent you — read it first.

`tools` names language/runtime packs (`node`, `python`, `go`, `rust`, … — see
`internal/registry/tools.json`) baked into a **per-profile toolbox image**
variant, built on demand and content-addressed (see
[Custom toolbox image](#custom-toolbox-image-image)).

MCP servers need no sandboxer wiring: the sandbox contains your repo's files,
so agent-level MCP config committed there (e.g. a `.mcp.json`) works as-is —
just add the servers' domains to `egress.allowedDomains`.

### Editing the config

The file is the whole interface: `sandboxer config edit` opens it in `$EDITOR`
(scaffolding the commented starter first if missing), `sandboxer config
validate` evaluates it and checks the schema strictly — an unknown attr or a
retired key fails with a precise message. Existing sandboxes pick changes up
on their next `enter`/`exec` — `srcs` included: even a running session sees
new sources live.

### Custom toolbox image (`image:`)

A profile can customize the toolbox image itself — without forking it (the
build runs with host nix, like everything else nix-shaped here).
Everything is **flat data** in `image`; the one thing that needs `pkgs` at
build time (an overlay) is a separate file:

```nix
{
  image = {
    packages = [ "gh" "python3Packages.requests" ];  # nixpkgs attr names baked in
    files."/etc/sandboxer/rc.d/10-aliases.sh" = "alias mci='mvn clean install'";
    env.SANDBOX_FLAVOR = "custom";                    # static image OCI env
    # overlay = "./overlay.nix";  # a PLAIN nixpkgs overlay, for computed pkgs
    # nixpkgsRev = "<commit>";    # PIN nixpkgs+agents (full 40-hex); default: track latest
  };
}
```

- **`packages`** — nixpkgs attribute names (dotted paths like
  `python3Packages.requests` allowed). They resolve against the overlaid
  package set, so an attr your overlay defines is listed here like any other.
- **`files`** — static text at absolute in-image paths (shell drop-ins under
  `/etc/sandboxer/rc.d/*.sh` are sourced by every interactive shell).
- **`env`** — static additions to the image's OCI env (the profile's own
  top-level `env` still overrides at run time).
- **`overlay`** — a file with a **plain nixpkgs overlay**, `final: prev: { … }`,
  for anything that needs `pkgs` at build time (patched or computed packages).
  Expose those as overlay attrs and name them in `packages`:

  ```nix
  # overlay.nix
  final: prev: {
    greet = prev.writeShellScriptBin "greet" "echo hi";
  }
  ```

The customization is **content-addressed**: the sandbox runs
`sandboxer-toolbox:var-<12hex>` — hashed over the effective input pins, the
package set (`tools` packs + `packages`), `files`, `env` and the overlay's
content — auto-built on first use and shared by identical profiles; the stock
`sandboxer-toolbox:latest` is untouched. Any change is a new tag, and an idle
persistent session recreates itself on the next `enter`. Full commented
example: [examples/custom-image.nix](./examples/custom-image.nix).

The image's `nixpkgs` flake input — the single input everything, agents
included, comes from — **tracks the remote head by default**:
`sandboxer image build` re-resolves it, stamps the result into the per-user
pins cache (`~/.cache/sandboxer/image-pins.json`) and builds from it — so a
rebuild + `recreate` is how agents update. `enter`/`exec` only ever reuse the
stamp, never re-resolve — nothing moves behind your back. `--no-refresh`
builds from the existing stamp. To hold the input still, set `nixpkgsRev` to a
full 40-hex commit — a pin selects a content-addressed `var-` image that never
moves.

### Nested containers

The toolbox image ships **podman** (with a `docker` shim that execs it, and
`docker compose` / `podman compose` via the bundled `podman-compose`), so a
sandbox can build and run containers of its own — `docker run postgres`,
`docker build`, `docker ps` all work. Because the sandbox is a real VM, the
engine runs **natively against the guest's own kernel**: full uid range (images
whose entrypoint switches user — postgres and friends — just work), no opt-in,
no seccomp/subuid machinery, and nothing of it touches the host. There is no
`nestedContainers` knob anymore — the key is retired and errors with a
migration hint.

No engine socket ever comes from the host (this is not docker-in-docker), and
pulls go through the sandbox's **egress allowlist** like any other traffic —
allow the registry's domains or the pull is refused (the defaults cover
docker.io/ghcr.io/quay.io and their CDNs). What does *not* work is anything
that expects the HOST's Docker API socket; inside the guest, tools talk to the
guest's own engine.

### Multiple profiles in one file

Instead of one profile per file, a `sandboxer.nix` can hold many under a
`profiles` attrset. Every section is **self-contained** — there is no shared
defaults layer and no merging between files. Reuse between sections is
ordinary nix — a `let`-bound base merged in with `//` (the section's own attrs
win). `default` names the one used when you don't name a section. The flat
one-profile form above still works. See `examples/multi-profile.nix`.

```nix
let
  api = {
    egress.allowedDomains = [ "api.anthropic.com" "github.com" ];
    srcs = [ { src = "."; branch = "feat/api"; include = [ "/shared/proto/" ]; } ];
  };
in {
  profiles = {
    inherit api;                                  # sandboxer create api
    api-prod = api // { env.NODE_ENV = "production"; };  # sandboxer create api-prod
  };
  default = "api";                                # sandboxer create  (no name → api)
}
```

`sandboxer create <name>` selects the section by name (that name becomes the
sandbox slug); a name that matches no section stays a plain slug. With no
name, `create` uses the `default` (or sole) profile. An explicit file also
works: `sandboxer create ./feat.nix` or `-f other.nix`; `sandboxer profile
list` shows the file's sections.

## Egress allowlist

Outbound traffic is fenced by microsandbox's **name-bound network policy
engine** — there is no proxy sidecar and nothing of sandboxer's in the network
path. With the allowlist on (the default) the machine boots `--no-net`
(**default-deny: no route at all**) plus one rule pair per domain in
`egress.allowedDomains`: `allow@*.domain:tcp:80` and `allow@*.domain:tcp:443` —
the domain **and its subdomains**, over HTTP and HTTPS, and nothing else. Rules
match by **name at connect time**, so a raw-IP dial is refused even for an
allowed domain's own address. An explicitly **empty** list
(`allowedDomains = [ ]`) is a fully offline machine, DNS included. The policy
is part of the machine's create argv, so editing it recreates the session
automatically — it can never drift live. Disable with
`egress.enabled = false` in the profile or `SANDBOXER_NO_EGRESS=1`.

A single `egress.proxy` URL switches to **proxy-delegated egress**: the machine
boots with an open network and the guest's HTTP(S) clients are pointed at the
proxy (`HTTP_PROXY`/`HTTPS_PROXY`; `egress.noProxy` → `NO_PROXY`; the env
default is `SANDBOXER_PROXY`) — the proxy IS the egress control point, so its
policy, not `allowedDomains`, decides what is reachable (sandboxer warns when
both are set). A `localhost`/`127.0.0.1` proxy is rewritten to
`host.microsandbox.internal` (the guest has a real network stack, where
loopback is guest-local) and the policy opens exactly the host proxy's port —
so a tunnel client on your host works with the obvious URL.

`egress.routes` (per-domain upstream proxies) was a squid-era feature and is
retired — the key errors with a migration hint; use one `egress.proxy` that
routes by destination itself.

## direnv

`sandboxer hook direnv` surfaces the **active** sandbox (the one set by
`sandboxer use <slug>`) to your host shell, so an `.envrc` — or any prompt /
editor that reads the environment — knows which sandbox is selected for the
project. It prints host-shell `export` lines for evaluation:

```sh
# .envrc
eval "$(sandboxer hook direnv)"
```

or, with the bundled [direnv](https://direnv.net) helper (copy
`contrib/direnv/use_sandboxer.sh` into `~/.config/direnv/direnvrc` once):

```sh
# .envrc
use sandboxer
```

Run `direnv reload` after `sandboxer use <slug>`: the active-sandbox marker
lives in the XDG state dir OUTSIDE the repo, so direnv cannot watch it.

What gets exported (only when a sandbox is active):

| var | source |
| --- | --- |
| `SANDBOXER_SLUG` | the active sandbox slug |
| `SANDBOXER_SRC` | the project root (absolute) |
| `SANDBOXER_BACKEND` | the recorded backend, if any |
| `SANDBOXER_ALLOW_DOMAINS` | the egress allowlist (csv), if any |

The hook is **read-only**: it prints already-persisted state and never builds or
starts anything — a `cd` into a project costs nothing. Outside a sandboxer
project, or with no active sandbox, it emits nothing (exit 0), so an `.envrc`
can call it unconditionally.

## Agents

```bash
sandboxer agents   # catalog: bin, image inclusion, auth env vars (set them INSIDE)
```

The registry is a single source of truth, `internal/registry/registry.json` (the
flake reads it too, to build the image). Adding an agent = one entry.

## Toolbox image

The agents run inside the bundled `sandboxer-toolbox:latest` image. It is
built with **host nix** (already a hard requirement of the CLI — no builder
container anywhere in the path):

```bash
sandboxer image build      # build with host nix + import into msb's image store
```

The build realizes a minimal OCI image (agents are plain nixpkgs packages,
prebuilt on cache.nixos.org; pi is vendored in the binary) as a docker-save
tar in the microVM image store (`<state>/images/<tag>.tar`), then imports it
into microsandbox's own image store (`msb load`) — after that every create is
boot-only, never a re-import. The `sandboxer` binary is **not** baked in — it
is a host tool. `sandboxer image rm` drops both the cached image and the tar.

`create`/`enter`/`exec` **auto-build** the image on first use when missing
(disable with `SANDBOXER_NO_AUTOBUILD=1`) — the stock image and any `var-`
variant alike; a rebuilt image reads as stale, so the next `enter` recreates
the session on the fresh rootfs.

Every `image build` first re-resolves the nixpkgs flake input to its current
remote head — on the host, via `git ls-remote` — and stamps it into the pins
cache, so a rebuild picks up new agent releases; `--no-refresh` builds from
the existing stamp instead (see
[Custom toolbox image](#custom-toolbox-image-image)).

`sandboxer image build [profile]` (a positional name or `-f`, resolved like
enter/exec) builds that profile's customized variant instead of the stock
image. `--nixpkgs-rev` overrides the input rev for this one build, on top of
the profile's value. A **concrete** rev override selects a `var-` tag: a
concrete rev flag **without** a profile pre-builds a pinned variant, but the
stock image profile-less sandboxes run is not touched — only a profile pinning
the same rev uses the result (the command prints a note).
Variant tags hash the effective input revs, so an `image build` that moves the
stamp rebuilds a tracking variant once on first use.

```bash
nix run .#build-image   # maintainer/dev equivalent (places the tar in the microVM store)
```

## Docs

- `sandboxer --help` / `sandboxer <cmd> --help` — commands, flags, examples
- [docs/architecture.md](./docs/architecture.md) — how it works: on-disk layout, lifecycle, image build, egress, registry
- [docs/troubleshooting.md](./docs/troubleshooting.md) — common problems, fixes, and FAQ
- [CONTRIBUTING.md](./CONTRIBUTING.md) — dev setup, Conventional Commits, releases
- [SECURITY.md](./SECURITY.md) — isolation model, vulnerability reporting
- [CHANGELOG.md](./CHANGELOG.md) — what's in each release

## Testing

```bash
go test ./...                                 # run all tests
go test ./... -cover                          # per-package coverage
go test ./... -coverprofile=cov.out           # write a profile
go tool cover -func=cov.out | tail -1         # total coverage
go tool cover -html=cov.out -o cov.html       # browseable report
```

CI enforces an engine-free **90% total coverage gate** on every push and PR
([ci.yml](./.github/workflows/ci.yml)); the badge above reflects that floor.
The real-msb integration suite (`-tags integration`) runs separately —
see [CONTRIBUTING.md](./CONTRIBUTING.md#integration-tests).

Backend tests pin pure argv builders and use fake engine/agent stubs on
`PATH`, so they run without a hypervisor or touching real credentials; the few
tests that shell out (`git`, real-msb integration) skip gracefully when the
tool/KVM is absent.

## License

MIT — see [LICENSE](./LICENSE).
