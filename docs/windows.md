# sandboxer on Windows (via WSL2, unverified)

There is **no native Windows build**. On Windows, run sandboxer inside
**WSL2**: the Linux `sandboxer` binary and the Linux `msb` run there, using
**nested KVM**. This needs no Windows-specific code — but the nested-KVM path
has **not been verified on real hardware** (hosted Windows runners are Azure
nested VMs where it does not initialize — see
[e2e-checklist.md](./e2e-checklist.md)); treat it as experimental until
confirmed on your machine.

## Setup

1. Install WSL2 and a Linux distro (`wsl --install`).
2. Enable nested virtualization. In `%UserProfile%\.wslconfig`:

   ```ini
   [wsl2]
   nestedVirtualization=true
   ```

   Then `wsl --shutdown` from Windows and reopen the distro.
3. Install sandboxer and its dependencies **inside the distro** (nix for config
   evaluation and the image build, git, and microsandbox from
   <https://microsandbox.dev>) exactly as on Linux.

## Verify nested KVM

```console
$ ls -l /dev/kvm          # must exist inside the WSL2 distro
$ sandboxer doctor        # the microsandbox (msb) row should be ✓
```

If `/dev/kvm` is absent after the `.wslconfig` change and a `wsl --shutdown`,
nested KVM is unavailable on your host/Windows build and sandboxer cannot run
inside WSL2 — there is no fallback backend. `sandboxer doctor` prints this
hint when it detects it is running under WSL without `/dev/kvm`.

## What about a native Windows `.exe`?

A native sandboxer on Windows is **out of scope**:

- sandboxer's config is `sandboxer.nix`, evaluated by host `nix-instantiate` —
  there is no native Windows nix — and the toolbox image is built with host
  nix as well.
- microsandbox's Windows support would carry the hypervisor question; WSL2's
  nested KVM already provides a working substrate for the Linux binary.

A native `.exe` (evaluating the config inside a microVM, translating `C:\…`
paths to guest paths) is designed but deferred. Until then, WSL2 is the Windows
story.
