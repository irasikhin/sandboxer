# Running sandboxer on macOS (unverified)

sandboxer is Linux-first. On macOS the same microsandbox backend runs on
**Hypervisor.framework** instead of KVM (libkrun supports both), on **Apple
Silicon** only — Intel Macs are not a target, and macOS 11+ is required.

**Status: the code is cross-platform, but this path has not been verified on
real Apple hardware.** There is no hosted-CI route to it either — Apple forbids
nested virtualization, and every hosted macOS runner is itself a VM — so it
stays unverified until someone runs it on a real Mac
(see [e2e-checklist.md](./e2e-checklist.md)). Treat it as experimental and
expect sharp edges; reports are welcome.

## Setup

1. Install **nix** (a hard requirement of the CLI) and **git**.
2. Install **microsandbox** (`msb`) from <https://microsandbox.dev>
   (`SANDBOXER_MSB` points at a binary off `PATH`).
3. Build or install sandboxer (`nix profile install github:irasikhin/sandboxer`,
   or `go build ./cmd/sandboxer` from a checkout).
4. `sandboxer doctor` — the `microsandbox (msb)` row should be green; there is
   no `/dev/kvm` on macOS, Hypervisor.framework is used implicitly.

Then use it exactly as on Linux (`sandboxer create`, `enter`, `exec`). The
first `enter` builds the toolbox image with host nix (minutes, network-bound).

## Known unknowns

These are the things a real-hardware pass needs to confirm (they all passed on
Linux/KVM):

- boot of the multi-GB toolbox image under HVF;
- virtio-fs shares: guest writes landing on the host owned by the invoking
  user, live in both directions;
- the egress policy engine (name-bound allowlist, default-deny) on the macOS
  network stack;
- nested containers (podman/docker inside the guest) against the guest kernel.

If you hit a wall, run `sandboxer doctor --json` and `msb doctor`, and open an
issue with both outputs plus the failing command's stderr.
