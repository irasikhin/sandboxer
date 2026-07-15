# Troubleshooting

Common problems and how to fix them. Start with `sandboxer doctor`, which checks
the engine, the toolbox image and the environment in one shot. For how things fit
together see [docs/architecture.md](./architecture.md).

## Docker / podman not installed or not running

sandboxer needs a container engine on the host — it is **not bundled**. Symptoms:
`no container engine found`, `Cannot connect to the Docker daemon`, or a hang on
the first `create`/`enter`.

- Install docker or podman from your distro, then confirm it works:
  `docker info` (or `podman info`) should succeed **without** sudo.
- For docker, your user must be in the `docker` group (`sudo usermod -aG docker
  $USER`, then log out and back in) or the daemon must be running
  (`systemctl status docker`).
- Pin the engine explicitly when both are installed:
  `--backend docker` / `--backend podman` or `SANDBOXER_ENGINE=podman`.
- `sandboxer doctor` reports which engine it found and whether the toolbox image
  is present.

## Permission denied on the state dir

All runtime state lives under `$XDG_STATE_HOME/sandboxer/<project>` (default
`~/.local/state/sandboxer/...`); the repo holds only the committed
`sandboxer.nix`. The state tree is created by the CLI as your user;
`_home/<slug>` is `0700` (it holds login tokens). A `permission denied` here
usually means the tree was created under a different user — commonly a rootful
container that wrote back as root.

- Check ownership: `ls -la "${XDG_STATE_HOME:-$HOME/.local/state}/sandboxer"`.
  If anything is owned by `root`, reclaim it:
  `sudo chown -R "$USER:$USER" "${XDG_STATE_HOME:-$HOME/.local/state}/sandboxer"`.
- Prefer **rootless** podman, or docker via the user's `docker` group, so
  in-container writes land as your uid.
- If a sandbox is wedged, `sandboxer rm <slug>` removes its state; re-`create` it.

## An agent can't reach a host

Outbound traffic is **fail-closed** through the egress allowlist — any host not
on the list gets a `403` from the proxy. A failed download, `git clone`, or
package install almost always means the host isn't allowed.

- Add the host to the profile's `egress.allowedDomains`, or pass
  `--allow-domains a.com,b.com` for the run.
- Remember transitive hosts: a package install often needs the registry **and** a
  CDN/mirror (e.g. `registry.npmjs.org` plus its CDN).
- The one-time `setup:` hook runs under the **same** allowlist — a network step
  in `setup:` needs its domains allowed too.
- To rule egress out while debugging, disable it deliberately:
  `SANDBOXER_NO_EGRESS=1` or `egress.enabled = false` in the profile. (In direct
  mode a configured `proxy` *replaces* the allowlist — then the proxy's own policy
  decides what's reachable. In the default allowlist mode, the proxy is chained
  through the allowlist, which still applies.)
- Proxy not taking effect after editing the config? A **persistent** session
  reuses its container, so proxy/egress changes only apply to a fresh one — run
  `sandboxer recreate`. The banner's `egress:` line shows what's actually in
  effect (`on→proxy (N domains)` when chained).

## Setup hook failed

`setup:` is a one-time `bash -lc` script run inside the sandbox before you take
over. A failed setup is **fatal by default**, so the `enter`/`exec` aborts.

- Read the captured output under `$XDG_STATE_HOME/sandboxer/<project>/_logs/`
  (`<slug>.*`).
- Skip it for one run with `--no-setup` to get a shell and debug interactively.
- A common cause is a network step under the egress allowlist — see the section
  above and allow the domains the script needs.
- The hook re-runs only when its **content changes** (a per-sandbox hash stamp,
  `_meta/<slug>.setup`). Edit the script and the next run re-triggers it.

## Persistent session won't reattach

`enter` shells into a persistent session container; a later `enter`
reattaches (full semantics: README "Persistent sessions"). A session survives
client disconnects but **not** a host/engine restart.

- After a reboot or `docker`/`podman` restart, the session container is gone —
  just `sandboxer enter <slug>` again to recreate it. Resuming the agent's own
  conversation is the agent's job (e.g. `claude --continue`).
- If reattach refuses because the **profile changed or the image was rebuilt**,
  the next `enter` recreates an idle session — but it refuses while another client
  is still attached. Detach the other client (Ctrl-q) or `sandboxer stop <slug>`
  first.
- Force a clean slate: `sandboxer stop <slug>` then `enter`, or `rm <slug>` and
  re-`create`.
- An escape hatch to one-shot containers: `--ephemeral` (or
  `SANDBOXER_SESSION=ephemeral`).

## FAQ

**What is an "upstream" / "parent" proxy?** Old terminology. There is no separate
upstream vs corporate proxy mode anymore — there is one `egress.proxy` URL and the
`egress.enabled` toggle decides how it is used (on = chained through the allowlist
sidecar, http:// only; off = the agent talks to it directly, http/https,
`egress.noProxy` applies). If you see `parent` in squid logs that is the same single
`egress.proxy` — squid's internal name for the peer it forwards to. See "An agent
can't reach a host" above for the behavior.

**Which platforms are supported?** Linux only. There is no macOS or Windows
build — sandboxer relies on a Linux container engine (docker/podman) on the host.

**Does it need Kubernetes / a cloud?** No. It is container-only and entirely
local: a docker/podman container per sandbox on your own machine. No
orchestrator, no daemon of its own, nothing in the cloud.

**Are sessions shared across machines?** No — sessions are **per-host**. A
persistent session is a container on the local engine; it does not follow you to
another machine and does not survive a host/engine restart.

**How much disk does a sandbox use?** The `<slug>/` dir holds a git worktree
per source — checkouts (narrowed by the `srcs` include patterns) whose object
stores are shared with their repos, not duplicated — plus a small private
`_home/<slug>`. The big cost is shared, not per-sandbox: the
`sandboxer-toolbox:latest` image (and any `var-*` variant) is pulled/built once
and reused by every sandbox.

**How do I debug inside a sandbox?** `sandboxer exec <slug> -- <cmd>` runs a
one-off command in it; `sandboxer enter <slug>` drops you into an interactive
shell. From there you have the same view the agent does — check the state dir's
`_logs/` for captured output. To review the agent's pending work use ordinary
git ON THE HOST (the container has no git access): uncommitted edits sit in the
source worktrees under `<slug>/`, committed work on `feat/<slug>-sb`
(`git log feat/<slug>-sb`, `git diff main...feat/<slug>-sb`).
