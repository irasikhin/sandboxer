# Running sandboxer on macOS (experimental)

sandboxer is Linux-first. On macOS it runs the same container backend, but the
engine executes Linux containers inside a **VM** (Docker Desktop or
`podman machine`), and the backend's uid / bind-mount assumptions were written
for a native Linux host. macOS support is therefore **experimental and being
validated** — this page is the setup + the known sharp edge + how to work around
it while we confirm the right default.

## Setup

Pick one engine and make sure it is running before using sandboxer:

- **Docker Desktop** — install it, start it, `docker version` must succeed.
- **podman machine** — `podman machine init --cpus 4 --memory 8192 --disk-size 40`
  then `podman machine start`. Give it enough RAM: the toolbox image is built by
  a nix-in-container step.

Build the binary from a checkout (`nix develop -c go build ./cmd/sandboxer`, or
`go build` with Go 1.24+). Then use it exactly as on Linux (`sandboxer create`,
`enter`, `exec`).

## The known sharp edge: container uid vs bind mounts

On Linux the container runs as your host uid:gid (`--user`), so files it writes
in the mounted worktree, `$HOME` and git object store stay owned by you. Inside a
macOS VM the host uid (typically `501`) does not always map cleanly through the
engine's file-sharing layer, which can surface as **"permission denied"** when
the agent tries to write the worktree or commit.

If you hit that, the `SANDBOXER_CONTAINER_USER` environment variable overrides
the `--user` value without a rebuild:

```sh
# Let the container run as the engine's default user (common fix on Docker
# Desktop, whose file sharing maps ownership back to you on the host side):
export SANDBOXER_CONTAINER_USER=

# …or pin a specific mapping the VM accepts:
export SANDBOXER_CONTAINER_USER=1000:1000
```

The default (variable unset) is unchanged from Linux, so this affects nothing
until you set it.

## Help us finish macOS support

Run the diagnostic and share the output — it tells us exactly where the uid /
bind-mount / egress path breaks on your engine, so the correct macOS default can
be baked in instead of relying on the override:

```sh
bash scripts/macos-diagnose.sh 2>&1 | tee macos-report.txt
```
