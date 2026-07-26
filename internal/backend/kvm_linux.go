//go:build linux

package backend

import "os"

// devKVMPresent reports whether /dev/kvm exists — the microVM backend needs KVM
// on Linux. Off Linux the check is not applicable (see kvm_other.go).
func devKVMPresent() bool {
	_, err := os.Stat("/dev/kvm")
	return err == nil
}
