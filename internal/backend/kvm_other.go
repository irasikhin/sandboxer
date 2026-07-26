//go:build !linux

package backend

// devKVMPresent is Linux-specific; off Linux the microVM backend uses the
// platform hypervisor (macOS: Hypervisor.framework), so KVM is not applicable
// and never reported as missing.
func devKVMPresent() bool { return true }
