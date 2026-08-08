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
- Container pulls are the classic case: the registry answers but the **blobs
  redirect to a CDN** — docker.io sends some regions/accounts to
  `*.cloudfront.net`, and `public.ecr.aws` sends *every* blob there, so a pull
  that dies halfway with `Forbidden` usually means `cloudfront.net` is missing.
  Current defaults include it (plus `public.ecr.aws`) — but a
  `sandboxer config init` from an older release **froze the then-defaults into
  your `sandboxer.nix`**, and `egress.allowedDomains` replaces the default set
  wholesale: add `"public.ecr.aws" "cloudfront.net"` to the list (or delete the
  attr to fall back to the current built-in defaults).
- The one-time `setup:` hook runs under the **same** allowlist — a network step
  in `setup:` needs its domains allowed too.
- To rule egress out while debugging, disable it deliberately:
  `SANDBOXER_NO_EGRESS=1` or `egress.enabled = false` in the profile. (In direct
  mode a configured `proxy` *replaces* the allowlist — then the proxy's own policy
  decides what's reachable. In the default allowlist mode, the proxy is chained
  through the allowlist, which still applies.)
- Proxy not taking effect after editing the config? The egress settings are part
  of the session's config hash, so an edit marks a **persistent** session stale
  and the next `enter` recreates it automatically — except while another client
  is attached, when it refuses (detach or `sandboxer stop <slug>` first). The
  banner's `egress:` line shows what's actually in effect (`on→proxy
  (N domains)` when chained).

## `sandboxer image build` fails or crawls behind a proxy

The image is built inside an ephemeral `nixos/nix` container, and a container
starts with an EMPTY environment — so on a machine whose access goes through a
proxy the builder used to reach nothing at all. sandboxer now passes the host's
`http_proxy`/`https_proxy`/`all_proxy`/`no_proxy` into it and rewrites a
localhost proxy to the host gateway.

- **Proxy bound to loopback only** (a SOCKS5 tunnel client is usually like
  this): the gateway address is not where it listens, and a host firewall often
  drops bridge traffic anyway. Give the builder the host's network, where
  localhost really is localhost — sandboxer then passes the URL through
  unrewritten: `sandboxer image build --cache --builder-arg=--network=host`.
- **Symptom without any of this:** `curl: (28) Connection timed out` fetching an
  agent's release tarball from GitHub, after ~5 minutes. The binary cache was
  unreachable, so nix fell back to building every agent from source.
- **A substituter that answers but crawls** — repeated
  `Timeout was reached ... Less than 1 bytes/sec transferred the last 30 seconds`
  — is worse than one that is down, because nix retries each path. Cap the
  attempts so it gives up and builds from source instead:
  `--builder-arg=--env --builder-arg='NIX_CONFIG=download-attempts = 1'`.
- Always pass `--cache` while debugging this: it keeps the nix-store volume, so
  a retry resumes instead of re-downloading everything.

## Setup hook failed

`setup:` is a one-time `bash -lc` script run inside the sandbox before you take
over. A failed setup is **fatal by default**, so the `enter`/`exec` aborts.

- Read the captured output: `$XDG_STATE_HOME/sandboxer/<project>/_logs/<slug>.setup.log`
  (the failure message prints the exact path).
- Skip it for one run with `--no-setup` to get a shell and debug interactively.
- A common cause is a network step under the egress allowlist — see the section
  above and allow the domains the script needs.
- The hook re-runs only when its **content changes** (a per-sandbox hash stamp,
  `_meta/<slug>.setup`). Edit the script and the next run re-triggers it.

## Nested podman: an image that switches user fails (postgres, EINVAL)

Inside a `nestedContainers = true` sandbox, `podman run … postgres` (or any
image whose entrypoint drops to its own user) dying with
`setresuid/setresgid …: Invalid argument` — or `newuidmap: write to uid_map
failed: Operation not permitted` — means the nested podman only has a
**single-uid mapping**, so no other uid exists to switch to.

- **Podman engine:** this works out of the box (sandboxer generates
  `/etc/subuid`/`/etc/subgid` and grants ambient `SETUID`/`SETGID`) — *if the
  host user has a subordinate range*. `enter` warns when it is missing; grant
  one on the host and re-enter:
  `sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 "$USER"`.
- **Sandbox created before the multi-uid grant** (or first run after
  upgrading): the nested podman's stored images in the persistent `$HOME` were
  unpacked under the old single-uid mapping. Run `podman system migrate` once
  inside the sandbox (or `podman system reset` to drop the stored images) and
  pull again.
- **Docker engine:** single-uid is a hard limit (docker grants a non-root user
  no ambient capabilities). Pulls work; user-switching containers do not —
  run that workload under a podman engine, or `--user 0:0` the nested
  container when the image tolerates running as (namespaced) root.

## Nested containers: the engine rejects the seccomp profile

A `nestedContainers = true` sandbox runs under sandboxer's own seccomp profile
(`_meta/seccomp-<hash>.json`, see SECURITY.md). An old engine that cannot parse
it refuses to create the container — the error names the profile file, e.g.
`decoding seccomp profile failed` / `invalid seccomp profile`. Known-good
engines are docker ≥ 20.10 and podman ≥ 4.

- First try upgrading the engine — the profile is the standard containers
  format, and the filter is a real part of the sandbox's posture.
- The escape hatch is `SANDBOXER_NESTED_SECCOMP=unconfined`: it restores the
  pre-v0.72 posture (**no syscall filter at all**, the loud notice on every
  enter is deliberate). The argv changes, so the session reads stale once and
  is rebuilt on the next enter — the same happens in reverse when you unset it.
- A sandboxer upgrade that changes the profile content moves the file (the name
  is a content hash), so nested sessions read stale **once** after upgrading
  and rebuild on the next enter. That is expected, not drift.

## microvm (smolvm): `krun_start_enter returned: -22 (EINVAL)` on every enter

Symptom — every `enter`/`exec` on `backend = "microvm"` dies in a few seconds:

```
Error: agent operation failed: start machine: … start vm:
krun_start_enter returned: -22 (EINVAL — libkrun rejected the VM configuration …)
```

This is an upstream smolvm/libkrun limit, not a misconfiguration, and it hits
the DEFAULT profile: sandboxer always shares three directories (the sandbox
dir, the private home, and the read-only `/run/sandboxer` profile dir), and an
allowlist adds `--allow-host`. Three shares **plus** a network flag **plus** a
large image is the combination libkrun refuses. Measured on smolvm 1.6.13 /
libkrunfw 5.6.0 with the ~3.4 GB toolbox image; minimal reproducer:

```console
$ smolvm machine run -I toolbox.tar -v A:A -v B:B -v C:C:ro \
    --allow-host example.com -- /bin/sh -c true      # EINVAL
$ smolvm machine run -I toolbox.tar -v A:A -v B:B -v C:C:ro \
    -- /bin/sh -c true                               # boots
$ smolvm machine run -I toolbox.tar -v A:A -v B:B \
    --allow-host example.com -- /bin/sh -c true      # boots
```

Any of these gets you running today:

- **`backend = "microsandbox"`** — the other microVM runner takes the identical
  profile (same image, same three shares, same allowlist) and boots. This is the
  recommended route; its allowlist is enforced by the runner and keeps
  subdomains.
- **`SANDBOXER_NO_EGRESS=1`**, or `egress.enabled = false` — drops
  `--allow-host` and the machine boots. Note what you are giving up: outbound
  traffic is then unrestricted unless an `egress.proxy` polices it.
- **`egress.proxy`** — proxy-delegated egress also omits `--allow-host`
  (the proxy is the control point instead).
- **A container backend** (`docker`/`podman`), which is the default and is
  unaffected.

`TestVM_EgressAllowlist_RealEngine` covers this shape, but only reproduces it
when pointed at a large image — run the microVM legs with
`SANDBOXER_ITEST_VM_IMAGE=<toolbox tar>`, not the default `alpine`.

## podman: "no policy.json file found" / short name did not resolve

podman needs its own configuration to exist before it can pull, load or run
anything: a `policy.json` and a `registries.conf`, in `~/.config/containers/`
or `/etc/containers/`. A distro podman package ships them (`containers-common`);
the sandboxer devShell puts the podman **binary** on `$PATH` and nothing else,
so on a host that has no system-wide podman install the first `podman` call
fails with one of:

```
Error: no policy.json file found at any of the following: …
Error: short-name "alpine:latest" did not resolve to an alias and no containers-registries.conf(5) was found
```

Write the two files yourself:

```console
$ mkdir -p ~/.config/containers
$ printf '{"default":[{"type":"insecureAcceptAnything"}]}\n' > ~/.config/containers/policy.json
$ printf 'unqualified-search-registries = ["docker.io"]\n' > ~/.config/containers/registries.conf
```

`newuidmap`/`newgidmap` must also be reachable for rootless multi-uid images
(NixOS keeps them in `/run/wrappers/bin`, which `nix develop` does not put on
`$PATH`) — see "Nested podman" above.

## Persistent session won't reattach

`enter` shells into a persistent session container; a later `enter`
reattaches (full semantics: README "Persistent sessions"). A session survives
client disconnects; across a host/engine restart the container dies but the
saved tmux layout comes back.

- After a reboot or `docker`/`podman` restart, the session container is gone —
  just `sandboxer enter <slug>` again: it recreates the container and restores
  the saved layout, relaunching any pane's recorded agent with its resume
  command (`claude --continue`); other programs must be restarted by hand.
  Opt out with `autoResume = false` in the profile or `SANDBOXER_NO_RESUME=1`.
- If reattach refuses because the **profile changed or the image was rebuilt**,
  the next `enter` recreates an idle session — but it refuses while another client
  is still attached. Detach the other client (Ctrl-Space d) or `sandboxer stop <slug>`
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

**Which platforms are supported?** Linux is the supported platform; macOS is
experimental — see [macos.md](./macos.md). Windows is supported **inside WSL2**
(the Linux binary + a container engine or smolvm run there) — see
[windows.md](./windows.md); there is no native Windows build. The default
backend relies on a container engine (docker/podman) reachable from the host;
the experimental microVM backend uses smolvm instead — see
[microvm.md](./microvm.md).

**Does it need Kubernetes / a cloud?** No. It runs entirely locally: a
docker/podman container (or a smolvm microVM) per sandbox on your own machine.
No orchestrator, no daemon of its own, nothing in the cloud.

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
source worktrees under `./sandboxes/<slug>/<branch>/<repo>/` (or wherever the
profile's `worktreesDir` points), committed work on
each source's configured branch (`git log <branch>`,
`git diff main...<branch>`).
