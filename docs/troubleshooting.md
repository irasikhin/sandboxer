# Troubleshooting

Common problems and how to fix them. Start with `sandboxer doctor`, which checks
nix, git, the microsandbox runner (with KVM and `MSB_HOME`), the toolbox image
and the environment in one shot. For how things fit together see
[docs/architecture.md](./architecture.md).

## `msb` not found, or the backend "cannot run here"

sandboxer runs every sandbox in a real microVM on
[microsandbox](https://microsandbox.dev) (`msb`) — it is **not bundled**.
Symptoms: a `doctor` warning on the `microsandbox (msb)` row, or a per-profile
`backend "microsandbox" cannot run here` warning.

- Install msb from <https://microsandbox.dev>; `SANDBOXER_MSB` points at a
  binary off `PATH`.
- On **NixOS** the upstream release binary does not run out of the box
  (stub-ld) — use this repo's package: `nix run
  github:irasikhin/sandboxer#microsandbox -- doctor`, or add the flake's
  `microsandbox` package to `environment.systemPackages` (the devShell has it
  on `PATH`).
- A profile naming a **retired** backend (`docker`, `podman`, `microvm`,
  `native`) fails with a migration error — set `backend = "microsandbox"` (or
  drop the key; it is the default). Container engines now run *inside* the
  sandbox, not as host backends.

## `/dev/kvm` is missing

On Linux the microVM needs KVM: `/dev/kvm` must exist and be accessible
(typically membership in the `kvm` group). `sandboxer doctor` reports this on
the msb row.

- Bare metal: enable virtualization (VT-x/AMD-V) in firmware; add yourself to
  the `kvm` group.
- **WSL2**: enable nested virtualization — `%UserProfile%\.wslconfig` with
  `[wsl2]` `nestedVirtualization=true`, then `wsl --shutdown` and reopen the
  distro (see [windows.md](./windows.md)); doctor prints this hint when it
  detects WSL. If `/dev/kvm` still does not appear, your hardware/Windows
  build does not support nested KVM and sandboxer cannot run there.
- A cloud VM needs nested virtualization support from the hypervisor.
- macOS uses Hypervisor.framework instead — no `/dev/kvm` involved (Apple
  Silicon only, and not yet live-verified — see [macos.md](./macos.md)).

## Every `create` fails: `MSB_HOME` too deep

Every sandbox's agent-relay UNIX socket path derives from `MSB_HOME`, and the
kernel caps a socket path at 108 bytes (`sun_path`) — a deep `MSB_HOME` makes
every `create` fail with a socket-path error. `sandboxer doctor` warns before
that happens.

- The default (`~/.msb`) is fine. If you override it, keep it short:
  `MSB_HOME=/var/lib/msb`, not a deep per-project path.

## A share under `/tmp` is refused

The guest mounts a tmpfs over `/tmp` *after* the host shares, so anything
shared below `/tmp` is invisible inside the sandbox. sandboxer refuses such a
configuration up front (a silent empty mount would be worse). This only bites
a profile that deliberately points `worktreesDir` or an `extraMounts` target
under `/tmp` — move it anywhere else.

Related refusals with the reason in the message: an `extraMounts` whose
`source` is a regular **file** (virtio-fs shares directories only — mount the
directory that holds it), a **fractional** `limits.cpus`, and an unparseable
`limits.memory`.

## An agent can't reach a host

Outbound traffic is **default-deny** through the egress allowlist: the machine
boots with no route at all plus one name-bound rule per allowed domain
(HTTP/HTTPS only). A failed download, `git clone`, or package install almost
always means the host isn't allowed — the connection is refused at the network
layer (there is no proxy returning 403 anymore).

- Add the host to the profile's `egress.allowedDomains`, or pass
  `--allow-domains a.com,b.com` for the run. A rule covers the domain **and
  its subdomains**.
- Rules are matched by **name**: dialing a raw IP fails even for an allowed
  domain's own address — that is the point, not a bug.
- Remember transitive hosts: a package install often needs the registry **and** a
  CDN/mirror (e.g. `registry.npmjs.org` plus its CDN).
- Container pulls inside the sandbox are the classic case: the registry
  answers but the **blobs redirect to a CDN** — docker.io sends some
  regions/accounts to `*.cloudfront.net`, and `public.ecr.aws` sends *every*
  blob there, so a pull that dies halfway with `Forbidden` usually means
  `cloudfront.net` is missing. Current defaults include it (plus
  `public.ecr.aws`) — but a `sandboxer config init` from an older release
  **froze the then-defaults into your `sandboxer.nix`**, and
  `egress.allowedDomains` replaces the default set wholesale: add
  `"public.ecr.aws" "cloudfront.net"` to the list (or delete the attr to fall
  back to the current built-in defaults).
- `allowedDomains = [ ]` means what it says: a **fully offline** machine, DNS
  included. Delete the attr (not empty it) to get the built-in defaults.
- The one-time `setup:` hook runs under the **same** allowlist — a network step
  in `setup:` needs its domains allowed too.
- To rule egress out while debugging, disable it deliberately:
  `SANDBOXER_NO_EGRESS=1` or `egress.enabled = false` in the profile. (With an
  `egress.proxy` set, the machine has an open network and the *proxy* is the
  control point — its policy decides what's reachable, and an `allowedDomains`
  set alongside is enforced by the proxy, not the VM; sandboxer warns about
  that pairing.)
- Egress not taking effect after editing the config? The policy is baked into
  the machine at create (part of the session's config hash), so an edit marks
  a **persistent** session stale and the next `enter` recreates it
  automatically — except while another client is attached, when it refuses
  (detach or `sandboxer stop <slug>` first). The banner's `egress:` line shows
  what's actually in effect.

## `sandboxer image build` fails or crawls

The stock image normally is not built at all — it comes prebuilt from GHCR
and msb pulls it on first use (host-side, honoring your shell's
`HTTP(S)_PROXY`; refresh with `sandboxer image pull`). A failed **create**
hinting "network needed to pull the prebuilt image" means that pull failed —
fix the network/proxy, or build locally. The local build (customized
profiles, offline hosts) runs with **host nix** (no builder container), so
its network behavior is your host's nix behavior:

- Behind a proxy: configure the host environment/nix the way any `nix build`
  needs (`http_proxy`/`https_proxy` for the daemon, or `~/.config/nix`); there
  is no builder container to lose the variables anymore.
- A substituter that answers but crawls (repeated `Timeout was reached …`) is
  worse than one that is down, because nix retries each path. Cap the attempts
  so nix gives up and builds from source: `NIX_CONFIG='download-attempts = 1'`.
- The first build is minutes-long and network-bound (it realizes the agents);
  later builds reuse the host nix store. `enter`/`exec` auto-build a missing
  `var-` variant — set `SANDBOXER_NO_AUTOBUILD=1` if you'd rather it error and
  let you build explicitly.
- The built tar lands in `<state>/images/` and is then imported into msb's own
  store — a failure naming `msb load` is an import problem, not a build one
  (check `msb --version` and disk space under `MSB_HOME`).

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

## Permission denied on the state dir

All runtime state lives under `$XDG_STATE_HOME/sandboxer/<project>` (default
`~/.local/state/sandboxer/...`); the repo holds only the committed
`sandboxer.nix`. The state tree is created by the CLI as your user, and guest
writes land owned by **you** (virtio-fs maps the guest's uid 0 to the invoking
user); `_home/<slug>` is `0700` (it holds login tokens). A `permission denied`
here usually means the tree was created under a different user — e.g. a
container-era rootful engine, or a stray `sudo sandboxer` run.

- Check ownership: `ls -la "${XDG_STATE_HOME:-$HOME/.local/state}/sandboxer"`.
  If anything is owned by `root`, reclaim it:
  `sudo chown -R "$USER:$USER" "${XDG_STATE_HOME:-$HOME/.local/state}/sandboxer"`.
- If a sandbox is wedged, `sandboxer rm <slug>` removes its state; re-`create` it.

## A published port doesn't answer on the host

The browser says *unable to connect* (or spins forever) on
`http://127.0.0.1:<port>/` even though the server inside the sandbox started
fine. Three causes, in the order they actually happen:

1. **The session machine predates the port.** A forward lives in the machine's
   create argv, so a session created before you added `ports` simply does not
   have it — and `enter` attaches to a running session rather than rebuilding it
   under your tmux (it says so, and now also warns that the forwards are not in
   that machine). Apply it:

   ```bash
   sandboxer stop <slug> && sandboxer enter <slug>
   ```

   `sandboxer show <slug>` has a `== ports ==` block that states this outright:
   a forward is either "open http://…" or "NOT live yet" with the reason.

2. **You opened the LAN address the app printed.** dsh prints
   `dsh web: http://127.0.0.1:3080 (LAN: http://172.16.0.50:3080)`. That LAN
   address is the GUEST's own address inside the microVM's NAT: from the host it
   is not routable (the host sends it to your default gateway and the connection
   times out). Always open the host side — `http://127.0.0.1:<host port>/`,
   the URL `create`/`enter` print.

3. **The server inside bound the guest's loopback.** The forward is delivered to
   the guest's `eth0`, never to its `127.0.0.1`, so a loopback-bound server is
   unreachable. Bind `0.0.0.0` (or `::`) inside. Check from within the sandbox:

   ```bash
   ss -ltn | grep <port>      # want 0.0.0.0:<port>, not 127.0.0.1:<port>
   ```

   dsh needs nothing here — inside a sandbox its web UI binds the wildcard by
   default (see [architecture.md](./architecture.md#ingress-published-ports)).

One more thing worth ruling out early: a shell/browser HTTP proxy that does not
exempt localhost turns the same request into a hang. `curl --noproxy '*'
http://127.0.0.1:<port>/` answering while a plain `curl` does not is the tell.

## Persistent session won't reattach

`enter` shells into a persistent session machine; a later `enter`
reattaches (full semantics: README "Persistent sessions"). A session survives
client disconnects; across a host restart the machine dies but the saved tmux
layout comes back.

- After a reboot, the session machine is gone — just `sandboxer enter <slug>`
  again: it recreates the machine and restores the saved layout, relaunching
  any pane's recorded agent with its resume command (`claude --continue`);
  other programs must be restarted by hand. Opt out with `autoResume = false`
  in the profile or `SANDBOXER_NO_RESUME=1`.
- If reattach refuses because the **profile changed or the image was rebuilt**,
  the next `enter` recreates an idle session — but it refuses while another client
  is still attached. Detach the other client (Ctrl-Space d, Ctrl-b d, Alt-d, or
  the `detach` command) or `sandboxer stop <slug>`
  first.
- Force a clean slate: `sandboxer stop <slug>` then `enter`, or `rm <slug>` and
  re-`create`.
- An escape hatch to one-shot machines: `--ephemeral` (or
  `SANDBOXER_SESSION=ephemeral`).
- A machine whose project directory was deleted behind sandboxer's back
  (`rm -rf` instead of `sandboxer rm`) surfaces in `sandboxer doctor` as an
  **orphan session**, with the exact `msb remove` command to reclaim it.

## FAQ

**Where did the docker/podman backend go?** Removed — every sandbox is a
microVM now (`backend = "microsandbox"`), which is a strictly stronger
boundary. Container engines did not disappear, they moved **inside**: the
toolbox image ships podman with a `docker` shim and `podman-compose`, running
natively against the guest kernel (full uid range, no opt-in — the old
`nestedContainers` key is retired). Anything that expects the *host's* Docker
API socket won't find one; inside the guest, tools talk to the guest's own
engine — which also serves a docker-compatible socket at
`/var/run/docker.sock` (started on demand by the image's `podman-socket`
helper), so testcontainers suites run with zero configuration.

**Which platforms are supported?** Linux with KVM is the supported, exercised
platform. macOS (Apple Silicon, Hypervisor.framework) and Windows (inside
WSL2 with nested KVM) are **compiled for but not live-verified** — see
[macos.md](./macos.md) and [windows.md](./windows.md); there is no native
Windows build.

**Does it need Kubernetes / a cloud?** No. It runs entirely locally: one
microVM per sandbox on your own machine. No orchestrator, no daemon of its
own, nothing in the cloud.

**Are sessions shared across machines?** No — sessions are **per-host**. A
persistent session is a microVM on the local host; it does not follow you to
another machine and does not survive a host restart (the tmux layout does —
see above).

**How much disk does a sandbox use?** The `<slug>/` dir holds a git worktree
per source — checkouts whose object stores are shared with their repos, not
duplicated — plus a small private `_home/<slug>`. The big cost is shared, not
per-sandbox: the toolbox image exists once in msb's store (pulled prebuilt —
plus, for locally built images, once as a build tar in `<state>/images/`),
reused by every sandbox; `sandboxer image rm` reclaims both.

**How do I debug inside a sandbox?** `sandboxer exec <slug> -- <cmd>` runs a
one-off command in it; `sandboxer enter <slug>` drops you into an interactive
shell. From there you have the same view the agent does — check the state dir's
`_logs/` for captured output. To review the agent's pending work use ordinary
git ON THE HOST (the sandbox has no git access): uncommitted edits sit in the
source worktrees under `./sandboxes/<slug>/<branch>/<repo>/` (or wherever the
profile's `worktreesDir` points), committed work on
each source's configured branch (`git log <branch>`,
`git diff main...<branch>`).
