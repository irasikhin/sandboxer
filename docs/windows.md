# sandboxer on Windows (via WSL2)

There is **no native Windows build**. On Windows, run sandboxer inside **WSL2**:
the Linux `sandboxer` binary and either a Linux container engine or the Linux
`smolvm` run there. This is the same approach podman machine and Docker Desktop
take, and it needs no Windows-specific code.

Two backends are possible inside WSL2:

- **Container backend** (docker/podman inside the WSL2 distro) — works today,
  the same as native Linux. This is the recommended Windows path.
- **microVM backend** (`backend = "microvm"`) — needs **nested KVM** inside
  WSL2. This is the plan's biggest unverified Windows assumption; treat it as
  experimental until confirmed on your hardware (see the checklist below).

## Setup

1. Install WSL2 and a Linux distro (`wsl --install`).
2. Install sandboxer and its dependencies **inside the distro** (nix for config
   evaluation, plus docker/podman or smolvm) exactly as on Linux.
3. For the microVM backend, enable nested virtualization. In
   `%UserProfile%\.wslconfig`:

   ```ini
   [wsl2]
   nestedVirtualization=true
   ```

   Then `wsl --shutdown` from Windows and reopen the distro.

## Verify nested KVM (microVM backend only)

```console
$ ls -l /dev/kvm          # must exist inside the WSL2 distro
$ sandboxer doctor        # microvm (smolvm) row should be ✓
```

If `/dev/kvm` is absent after the `.wslconfig` change and a `wsl --shutdown`,
nested KVM is unavailable on your host/Windows build and the microVM backend
cannot run inside WSL2 — use the container backend instead. `sandboxer doctor`
prints this hint when it detects it is running under WSL without `/dev/kvm`.

## What about a native Windows `.exe`?

smolvm ships a Windows build (WHP, via a fork of libkrun), but a native
sandboxer on Windows is **out of scope**:

- sandboxer's config is `sandboxer.nix`, evaluated by host `nix-instantiate` —
  there is no native Windows nix.
- The Windows smolvm path is fork-maintained, single-vCPU, TSI-only networking,
  and virtio-fs symlinks require Developer Mode.

A native `.exe` (evaluating the config inside a microVM, translating `C:\…`
paths to guest paths) is designed but deferred. Until then, WSL2 is the Windows
story.
