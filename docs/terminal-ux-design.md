# Terminal UX, agent tooling & plugins — design

Status: **all implemented** — T1–T3 (prompt+MOTD, tooling pack, `setup:` hook +
shell drop-ins) plus tool packs (3c, per-profile nix image variant). The MCP
registry (3d) was implemented and later **removed** in v0.29.0 (see that
section). Scope: make the in-sandbox
shell pleasant and oriented, bake the baseline tooling humans and agents expect,
and lay the extensibility primitives a "plugin" would be built from. Sequenced as
four incremental PRs (T1 → T2 → T3) plus two deferred milestones.

The copy-in "pushes rw deps" language in the sketches below predates the
v0.27.0 git-worktree migration — there is no push-back today.

This is a decisions-and-why record (an RFC), not a mirror of code
(`proc-doc-as-code`): it captures the mechanism choices and the alternatives
rejected, so the implementation PRs can stay small and link back here.

## Why the shell is bare today (root cause)

`enter` launches `bash -l` (`internal/cli/lifecycle.go:144`) inside the toolbox
image. In that image:

- **`$HOME` is empty.** The per-sandbox home (`Base.EnsureHome`,
  `internal/sandbox/sandbox.go:95`) is a bare `mkdir 0700`. Host dotfiles are
  deliberately **not** mounted — that isolation is a feature
  (`internal/backend/container.go:118`) — so there is no `~/.bash_profile` /
  `~/.bashrc` to source.
- **There is no `/etc/profile` in the image.** `dockerTools.buildLayeredImage`
  ships a bare rootfs; `fakeRootCommands` creates only `/work /tmp
  /run/sandboxer` (`internal/toolbox/assets/flake.nix`). A login shell therefore
  sources **nothing**.
- **Net result:** the default `PS1='\s-\v\$'` (`bash-5.2$`), `PATH=/bin`, no
  color, no aliases, no git context — you cannot even tell a sandbox shell from
  the host shell.
- **Tooling gaps** in the image `contents`: no pager (`less` → `git log` dumps
  raw), no editor (`nvim`/`nano`), no `ps`/`top` (no `procps`), no `rg`/`fd`/
  `tree`. The list is only: `bashInteractive coreutils git rsync jq curl cacert
  gnused gawk gnugrep openssh which` + agents + the sandboxer binary.

The container already exports orientation context we can exploit:
`SANDBOXER_SLUG`, `SANDBOXER_SANDBOX_DIR`, `SANDBOXER_ALLOW_DOMAINS`,
`SANDBOXER_IN_CONTAINER` (`internal/backend/container.go:111-166`).

## Constraints this design must respect

- **Isolation** — host config must never leak in; the prompt/tooling come from
  sandboxer itself, not the host.
- **Reproducible nix image** — tooling is a `contents` edit in the embedded
  `assets/flake.nix`; cost is image size + a rebuild, not arbitrary runtime
  installs.
- **Per-sandbox `$HOME` is rw, persistent, and may hold agent auth** (e.g.
  `~/.claude.json`). Keep shell cosmetics **out** of it so there is no clobber
  policy and no entanglement with credential files.
- **Fail-closed egress** — anything that hits the network (incl. a future
  `setup` hook) runs under the allowlist; that is correct, and must be surfaced,
  never bypassed.
- **90% coverage gate, measured engine-free** + a separate `//go:build
  integration` suite (`internal/itest`, `scripts/itest.sh`) for the real-engine
  path. Pure logic is unit-tested for the gate; "does it actually run in a
  container" is integration-tagged.

---

## Tier 1 — prompt + orientation

Smallest change, the most visible win, no new config surface.

### Mechanism: baked rc + `bash --rcfile`, not HOME-seeded dotfiles

Bake `/etc/sandboxer/rc.sh` into the image (a `pkgs.writeTextDir` entry in the
flake `contents`) and change the interactive launch from `bash -l` to a guarded
`bash --rcfile /etc/sandboxer/rc.sh -i`.

Chosen because:

- `$HOME` is empty and isolated, so there is nothing to source today; `--rcfile`
  forces our file deterministically, with no login-source-order guessing and no
  need to synthesize `/etc/profile`.
- **No writes into the per-sandbox `$HOME`** → no clobber of user edits, no
  interaction with agent auth files, no "write-if-absent" staleness logic. `$HOME`
  stays purely agent state.
- The rc is **versioned with the image** (rebuilt from the binary's embedded
  flake). It is static text but reads runtime env (`$SANDBOXER_SLUG`, …), so the
  prompt is still dynamic.

Rejected alternatives:

- *Seed `~/.bash_profile` into `$HOME` from Go (`EnsureHome`).* More moving parts
  (embed asset in Go, write-if-absent, clobber policy), couples shell cosmetics
  to the sandbox-state package, and only benefits `enter` anyway. Keep cosmetics
  in the image.
- *Pass `PS1`/`PROMPT_COMMAND` via `--env`.* Interactive bash resets `PS1` from
  its rc; an env `PS1` is unreliable, and you still need an rc for aliases. A
  half-measure.

### Stale-image safety (important)

Flipping the Args means the image **must** contain `/etc/sandboxer/rc.sh`. A user
with an already-cached `sandboxer-toolbox:latest` from before this ships will not
auto-rebuild (the image exists, so `ensureImage` skips the build,
`internal/backend/container.go:192`) and would hit `bash: /etc/sandboxer/rc.sh:
No such file`. So the launcher is **guarded**:

```
Args: []string{"bash", "-c",
    "test -r /etc/sandboxer/rc.sh && exec bash --rcfile /etc/sandboxer/rc.sh -i || exec bash -i"}
```

Falls back to a plain interactive shell on a stale image; a `sandboxer
image build` (or first build on a clean host) picks up the prompt. Call this out
in CHANGELOG.

### `rc.sh` contents (sketch)

```bash
# only for interactive shells
case $- in *i*) ;; *) return ;; esac

__sbx_git() {                 # cheap branch, only inside a repo
  local b; b=$(git rev-parse --abbrev-ref HEAD 2>/dev/null) || return
  printf ' (%s)' "$b"
}
# distinct color so a sandbox shell is never mistaken for the host
PS1='\[\e[1;35m\]sbx:'"${SANDBOXER_SLUG:-?}"'\[\e[0m\] \[\e[36m\]\w\[\e[0m\]$(__sbx_git)\$ '

alias ls='ls --color=auto'  ll='ls -alh --color=auto'  la='ls -A'
alias grep='grep --color=auto'  ..='cd ..'
export EDITOR=${EDITOR:-nvim} PAGER=${PAGER:-less} LESS=${LESS:--R}

# plugin / user extension points (see Tier 3)
for f in /etc/sandboxer/rc.d/*.sh; do [ -r "$f" ] && . "$f"; done
[ -r "$HOME/.config/sandboxer/rc" ] && . "$HOME/.config/sandboxer/rc"
```

### MOTD banner (Go)

Replace the single line at `internal/cli/lifecycle.go:138` with a compact,
stderr-only banner (stderr so it never pollutes a piped stdout, consistent with
the existing `configLine` prints). Built from the already-resolved `rt`:

```
sandbox feat  ·  .sandboxer/feat
egress ON · 3 domains   agents: claude codex
exit → pushes rw deps back to their origins
```

Factor the body into a pure `enterBanner(rt, slug, dir) string` so it is
unit-tested engine-free (feeds the gate); the `enter` wiring is integration-
covered.

### Tests

- `enterBanner` — pure unit test (gate).
- `assets/flake.nix` — a `toolbox` unit test asserting the embedded asset
  declares the `/etc/sandboxer/rc.sh` entry (string check, gate).
- Integration (`//go:build integration`) — run `bash --rcfile
  /etc/sandboxer/rc.sh -i -c 'type ll; echo "$PS1"'` in a real container; assert
  the alias and the `sbx:` prompt marker are present.

---

## Tier 2 — baseline tooling

One `contents` edit in `internal/toolbox/assets/flake.nix`, one image rebuild.

### Package set

- **Core (must):** `less` (pager — git unusable without it), `neovim` (no editor
  exists; `EDITOR=nvim`), `procps` (`ps`/`top`/`free` — debug inside the box), `ripgrep`
  (agents shell out to `rg`), `fd`, `tree`, `gnutar` + `gzip` (agents extract
  archives).
- **Git UX:** `delta` (pager; pairs with `less`).
- **Polish (open question — default in unless noted):** `gnumake` (agents run
  `make`), `unzip`; *optional:* `bat`, `fzf`, `htop`.

All are in the nixpkgs binary cache (the builder runs with
`--accept-flake-config`, so nothing compiles from source — unlike the `codex`
agent, which is excluded from the image for exactly that reason,
`internal/registry/registry.json`). The layered image (`maxLayers = 120`) dedups
shared deps; rough cost **+60–120 MB**.

### git pager config

Bake a minimal `/etc/gitconfig` (another `writeTextDir`) rather than setting
`GIT_PAGER` only in `rc.sh`, so it also applies to the **non-interactive `exec`**
path and to agent `git`:

```ini
[core]        pager = delta
[interactive] diffFilter = delta --color-only
[delta]       navigate = true
```

Safe for headless agents: git disables the pager when stdout is not a TTY, so
porcelain/parseable output is unaffected; `delta` only touches the
human-facing pager.

### Tests

- `toolbox` unit test: assert the new package names appear in the embedded flake
  `contents` (gate).
- Integration: `command -v rg fd nvim less tree delta ps make` all resolve in a
  real container.

---

## Tier 3 — plugins / extensibility

Three sub-features, designed to compose. The "plugin" concept is the *sum* of
these primitives — build the primitives first, name the bundle later.

### 3a. `setup:` profile hook (run-once per sandbox) — the real win

Today every `enter` lands in a cold tree. A `setup` hook is what makes a sandbox
usable for an actual stack.

**Config** — add `Setup` to the profile schema (`internal/config`). A shell
script run via `bash -lc`:

```yaml
setup: |
  npm ci
  npm run build
```

**Lifecycle** — run inside the container, in the sandbox workdir, **after**
`MakeSandbox` (deps copied in) and **before** handing control to the user/agent.
Reuse the full `backend.Run` wiring (same mounts, same egress allowlist), with
`Interactive=false`, output streamed to stderr and logged to
`_logs/<slug>.setup`. A new `RunSetup` seam keeps `enter`/`exec` thin.

**Run-once** — sentinel in the per-sandbox meta (`<slug>.setup-done`) storing a
**hash of the setup script**; re-run when the hash changes (edited setup) or the
sandbox is recreated.

**Gotchas (must document):**

- *Egress* — `npm ci` et al. run under the allowlist, so their registries must
  be in `network.allowedDomains`; fail-closed surfaces a missing domain rather
  than silently succeeding on an open network. Correct, but a documentation
  must.
- *Failure policy* — open question. Recommend **fail-by-default** for
  `create` (correctness) and a `--no-setup` escape hatch; for
  interactive `enter`, either fail or warn-and-drop-into-shell so a human can fix
  it (recommend fail + `--no-setup`).
- *In-container guard* — `PersistentPreRunE` blocks `create`/`enter`/`exec` when
  run **inside** the container; setup is host-side orchestration through those
  commands, so it is unaffected.

**Tests** — schema parse + sentinel/hash logic are pure (gate); integration
covers "runs once, re-runs on change, egress is allowlisted".

### 3b. Shell drop-ins (composes with T1)

Already wired by the `rc.sh` tail: image/plugin fragments in
`/etc/sandboxer/rc.d/*.sh` and a user file at `~/.config/sandboxer/rc`. No code
beyond T1 — this PR just formalizes and tests the contract. A "plugin" ships an
`rc.d` fragment to add aliases/env without forking the image.

### 3c. Tool packs (`tools:` profile field) — IMPLEMENTED (option 1)

Extra toolchains (node/python/rust/go) are project-specific; baking them all into
the default image bloats it for everyone. Options:

1. **Per-profile image overlay** — build a derived image (toolbox + extra nix
   pkgs) on demand, cached by a tool-set hash. Reuses the nix builder; most
   reproducible; fits the architecture. Cost: per-set build + storage. **(Pick
   this when we do it.)**
2. Curate common runtimes into the default image — simple, bloats.
3. `extraMounts` host toolchains — already possible, zero work, but leaks the
   host and breaks reproducibility.

Key a `tools:` list off a curated nix-package map, mirroring the agent-registry
single-source pattern (a `tools.json` embedded **and** consumed by the flake,
like `registry.json` / `llm-agents.nix`). Its own milestone.

### 3d. MCP-server registry — REMOVED in v0.29.0

Was: a catalog of MCP servers (name → package/command), with config seeded into
the agent's per-sandbox HOME (`~/.claude.json` `mcpServers`) and the server's
domains folded into the allowlist. Removed because the git-worktree redesign
(v0.27.0) made it redundant: the sandbox **is** the repo, so agent-level MCP
config committed there (e.g. `.mcp.json`) travels in by itself, and the seeding
was claude-only anyway — the same agent-specific-config category the `agent:`/
`model:` cleanup already dropped. Egress domains for MCP servers are declared
explicitly via `network.allowedDomains`.

### The plugin model (unifying)

A **sandboxer plugin** = a named bundle providing any of: (1) nix packages →
tool pack, (2) an `rc.d` fragment → 3b, (3) a `setup` snippet → 3a, (4) an MCP
registration → 3d, (5) extra allowed domains. Start **in-repo and
profile-driven** — everything expressible in the project config plus the curated
registries. Defer an external third-party plugin loader (a plugin dir + manifest
format) until the primitives exist: don't design a loader before the things it
loads.

---

## Sequencing (incremental PRs — `flow-incremental-pr`, one logical change each)

| PR | Type | Content |
|----|------|---------|
| PR1 | `feat` (T1) | baked `/etc/sandboxer/rc.sh` + guarded enter Args + MOTD banner + `enterBanner` unit test |
| PR2 | `feat` (T2) | flake `contents` += tool pack; `/etc/gitconfig` delta pager; `EDITOR`/`PAGER` wired |
| PR3 | `feat` (T3a) | `setup:` profile field + run-once lifecycle + sentinel/hash + `--no-setup` |
| PR4 | `feat`/`docs` (T3b) | formalize + test the `rc.d` / `~/.config/sandboxer/rc` drop-in contract |
| later | — | tool packs (3c) — separate design doc; MCP registry (3d) shipped then removed in v0.29.0 |

Each PR green-gates (`gofmt` + `go vet` + `golangci-lint run` + `go test ./...`,
90% engine-free) before it lands; conventional-commit `feat` → minor bump.

## Open questions

1. **Editor** — `neovim` (decided). Also ship `nano` for quick human edits?
   (recommend no — `nvim` covers it)
2. **`setup` failure on interactive `enter`** — abort, or warn + drop into shell?
   (recommend abort + `--no-setup`)
3. **Prompt git branch** — always (one `git` call per prompt, guarded by being in
   a repo) or behind a toggle? (recommend always)
4. **Polish tools** — `make`/`unzip` in the default pack (recommend yes);
   `bat`/`fzf`/`htop` in or out? (recommend out, available via tool packs later)
