# Security Policy

## Supported Versions

sandboxer is pre-1.0; the CLI flags and on-disk layout may still change between
minor versions (the `.sandboxer.yaml` schema has settled on `roots`+`deps` and is
treated as stable through 0.x).

While pre-1.0, **only the latest `0.x` release** receives security fixes — there
are no maintained back-ports to earlier `0.y` lines. A fix ships as the next
patch (or minor) release; upgrade to the newest tag to receive it.

| Version            | Supported          |
|--------------------|--------------------|
| latest `0.x`       | :white_check_mark: |
| any older release  | :x:                |

## Reporting a Vulnerability

Do not open a public issue for a vulnerability. Report security issues by
emailing the maintainer listed in the GitHub repository profile, or by using
GitHub private vulnerability reporting if it is enabled for this repository.

Please include:

- affected sandboxer version or commit;
- operating system and install method;
- a minimal reproduction that does not include real credentials or API keys.

This is a single-maintainer open-source project, so reports are handled on a
**best-effort** basis. Expect an initial acknowledgement within a few days
(typically under a week); a fix then ships in the next release once confirmed.

## Security model notes

sandboxer is a workflow tool for running coding agents in isolation, not a
hardened security boundary. Understand the model before trusting it:

- **Separate working copy.** sandboxer isolates a coding agent in a *separate
  directory* containing only the `deps` you list, so the agent's changes never
  touch their origins until you review (`sandboxer diff`) and copy them back
  (`sandboxer push`). No git is involved.

- **Push overwrites the origin.** `sandboxer push` (and the automatic copy-back
  after `enter`/`exec`) replaces each origin *wholesale* with the sandbox copy,
  with no merge and no signature check — an out-of-band edit to the origin is
  lost. Run `sandboxer diff` before pushing to see what will change.

- **Container backend.** The `podman`/`docker` backend runs the agent
  unprivileged: `--user` (non-root), `--cap-drop=ALL`, and
  `--security-opt no-new-privileges`. This reduces, but does not eliminate,
  the blast radius of a misbehaving agent.

- **Egress allowlist.** The agent runs on an `--internal` network whose sole
  exit is an allowlist forward-proxy, restricting outbound network traffic to
  the configured domains. The container backend **fails closed**: if the
  allowlist is required but the proxy cannot start (or no domains are allowed),
  the run is refused rather than silently falling back to an open network.
  Disable it deliberately with `egress: false` or `SANDBOXER_NO_EGRESS=1`. Even
  when active it is a **best-effort guardrail, not a guarantee** against a
  determined adversary — DNS tricks, abuse of an allowed domain, and similar
  techniques can defeat it.

  > **A configured upstream proxy replaces the allowlist.** If you set
  > `proxy.http`/`proxy.https` (e.g. a corporate proxy), sandboxer assumes that
  > proxy is the egress boundary and does **not** start the allowlist sidecar —
  > outbound traffic is governed by your proxy's policy, not by
  > `network.allowedDomains`. Don't set an upstream proxy expecting the domain
  > allowlist to also apply.

- **Credentials (container backend).** Each sandbox has its own private agent
  home (`.sandboxer/_home/<slug>`), mounted as `$HOME`. The host's real agent
  config — `~/.claude`, `~/.claude.json`, tokens, project history, MCP servers —
  is **never** mounted in, so nothing from the host leaks into the sandbox and
  parallel sandboxes never share or race on one config. Authenticate *inside*
  the sandbox (e.g. `claude login`); the credentials persist in that sandbox's
  home and are isolated from every other sandbox and from the host. API-key
  environment variables (e.g. `ANTHROPIC_API_KEY`) are passed through only when
  you have set them on the host — an explicit opt-in. Treat the sandbox as
  having full access to whatever credentials you give it; only wire in the
  agents (and keys) you actually need for the task.

- **Container environment.** The agent runs in a podman/docker container that
  starts from a clean, explicit environment: it does **not** inherit your host
  shell, so an `AWS_*` / `GITHUB_*` / `*_TOKEN` left in your environment is not
  visible to the agent unless you wire it in (an agent's API-key env vars are
  passed through only when you have set them — see Credentials above).

- **Not a multi-tenant boundary.** sandboxer is **not** a hardened
  multi-tenant isolation layer. Do not run untrusted or malicious agents and
  assume they are contained — assume an adversarial agent can reach anything
  the sandbox can reach.
