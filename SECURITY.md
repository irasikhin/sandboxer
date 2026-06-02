# Security Policy

## Supported Versions

sandboxer is pre-1.0; the CLI flags, config schema, and on-disk layout may
still change between minor versions. Security fixes are provided for the
latest release only.

## Reporting a Vulnerability

Do not open a public issue for a vulnerability. Report security issues by
emailing the maintainer listed in the GitHub repository profile, or by using
GitHub private vulnerability reporting if it is enabled for this repository.

Please include:

- affected sandboxer version or commit;
- operating system and install method;
- a minimal reproduction that does not include real credentials or API keys.

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

- **Credentials.** Agent auth-config directories and API-key environment
  variables are bind-mounted or passed ephemerally into the sandbox. Treat the
  sandbox as having full access to whatever credentials you give it; only wire
  in the agents (and keys) you actually need for the task.

- **Native backend.** The `native` backend relies on Claude Code's own
  `/sandbox` (bubblewrap) for OS-level isolation; its containment is only as
  strong as that mechanism.

- **Not a multi-tenant boundary.** sandboxer is **not** a hardened
  multi-tenant isolation layer. Do not run untrusted or malicious agents and
  assume they are contained — assume an adversarial agent can reach anything
  the sandbox can reach.
