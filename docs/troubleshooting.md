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

## Permission denied on `.sandboxer/`

The `.sandboxer/` tree is created by the CLI as your user; `_home/<slug>` is
`0700` (it holds login tokens). A `permission denied` here usually means the tree
was created under a different user — commonly a rootful container that wrote back
as root.

- Check ownership: `ls -la .sandboxer .sandboxer/_home`. If anything is owned by
  `root`, reclaim it: `sudo chown -R "$USER:$USER" .sandboxer`.
- Prefer **rootless** podman, or docker via the user's `docker` group, so
  in-container writes land as your uid.
- If a sandbox is wedged, `sandboxer rm <slug>` removes its state; re-`create` it.

## An agent can't reach a host

Outbound traffic is **fail-closed** through the egress allowlist — any host not
on the list gets a `403` from the proxy. A failed download, `git clone`, or
package install almost always means the host isn't allowed.

- Add the host to the profile's `network.allowedDomains`, or pass
  `--allow-domains a.com,b.com` for the run.
- Remember transitive hosts: a package install often needs the registry **and** a
  CDN/mirror (e.g. `registry.npmjs.org` plus its CDN).
- The one-time `setup:` hook runs under the **same** allowlist — a network step
  in `setup:` needs its domains allowed too.
- To rule egress out while debugging, disable it deliberately:
  `SANDBOXER_NO_EGRESS=1` or `egress: false` in the profile. (A configured
  upstream `proxy.http`/`proxy.https` *replaces* the allowlist — then the proxy's
  own policy decides what's reachable.)

## Setup hook failed

`setup:` is a one-time `bash -lc` script run inside the sandbox before you take
over. A failed setup is **fatal by default**, so the `enter`/`exec`/`run` aborts.

- Read the captured output under `.sandboxer/_logs/` (`<slug>.*`).
- Skip it for one run with `--no-setup` to get a shell and debug interactively.
- A common cause is a network step under the egress allowlist — see the section
  above and allow the domains the script needs.
- The hook re-runs only when its **content changes** (a per-sandbox hash stamp,
  `_meta/<slug>.setup`). Edit the script and the next run re-triggers it.

## Persistent session won't reattach

`enter` attaches to a persistent session container (tmux inside); a later `enter`
reattaches. A session survives client disconnects but **not** a host/engine
restart.

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

**Which platforms are supported?** Linux only. There is no macOS or Windows
build — sandboxer relies on a Linux container engine (docker/podman) on the host.

**Does it need Kubernetes / a cloud?** No. It is container-only and entirely
local: a docker/podman container per sandbox on your own machine. No
orchestrator, no daemon of its own, nothing in the cloud.

**Are sessions shared across machines?** No — sessions are **per-host**. A
persistent session is a container on the local engine; it does not follow you to
another machine and does not survive a host/engine restart.

**How much disk does a sandbox use?** The `<slug>/` working dir holds only the
`deps` you listed (flat copies — nothing by default), plus a small private
`_home/<slug>`. The big cost is shared, not per-sandbox: the
`sandboxer-toolbox:latest` image (and any `var-*` variant) is pulled/built once
and reused by every sandbox.

**How do I debug inside a sandbox?** `sandboxer exec <slug> -- <cmd>` runs a
one-off command in it; `sandboxer enter <slug>` drops you into an interactive
shell. From there you have the same view the agent does — check `.sandboxer/_logs/`
for captured output, and `sandboxer diff <slug>` to see pending changes before
you `push`.
