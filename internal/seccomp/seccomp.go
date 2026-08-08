// Package seccomp builds the syscall filter a nestedContainers sandbox runs
// under. Before it existed the opt-in cost was `seccomp=unconfined` — no
// syscall filter at all; now the sandbox keeps the containers default filter
// and only the syscalls a nested ROOTLESS engine actually needs are opened.
//
// The base is vendored from containers/common (the profile podman itself
// ships) rather than generated, so the file is inspectable and the filter is
// identical on both engines — docker parses the same profile format. Drift
// against the engine's own default is acceptable by construction: any pinned
// profile is strictly tighter than unconfined, which is what it replaces.
//
// Vendored file: base_profile.json = pkg/seccomp/seccomp.json from
// https://github.com/containers/common at v1.0.1
// (commit a970d99549001503ae19f492f55a269270833e75), Apache-2.0 — full text in
// LICENSE.containers-common. The file is embedded verbatim; the modifications
// live here, at runtime (strip + append, see build).
package seccomp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "embed"
)

//go:embed base_profile.json
var baseProfile []byte

// nestedSyscalls are opened unconditionally on top of the base profile. Two
// reasons a syscall lands here, both rooted in the same blind spot: the
// filter is resolved against the OUTER container's capabilities (cap-drop=ALL),
// while the nested engine holds these capabilities in its OWN user namespace —
// which a seccomp filter cannot see.
//
//   - caps-conditional in the base: sethostname/setdomainname/setns need
//     CAP_SYS_ADMIN and chroot needs CAP_SYS_CHROOT, EPERM otherwise; the
//     nested crun setting its container's hostname, or containers/image
//     unpacking a layer through its chroot fallback, holds those caps in the
//     nested namespace and still gets EPERM from the outer filter. (chroot is
//     not theoretical: without it every nested pull dies with "after fallback
//     to chroot: operation not permitted".)
//   - absent from the base entirely (= the profile's ENOSYS default): the new
//     mount API (fsopen and friends are present, but mount_setattr,
//     open_tree_attr, statmount, listmount are not) and the key-management
//     pair add_key/request_key beside the already-allowed keyctl.
//
// The rest (clone/clone3/unshare with CLONE_NEWUSER, mount, pivot_root, the
// fsopen family) is unconditionally allowed by the vendored base already —
// they are listed anyway so the guarantee this package makes does not depend
// on what a future base bump happens to contain. Sorted; the merged entry
// keeps this order.
//
// What is deliberately NOT here, though the same argument would open it: the
// caps-conditional escape primitives. bpf and perf_event_open (CAP_SYS_ADMIN),
// open_by_handle_at (CAP_DAC_READ_SEARCH — the shocker escape), ptrace and
// process_vm_readv (CAP_SYS_PTRACE), the module loaders, iopl/ioperm. A nested
// engine runs without them; a nested container that genuinely needs one (a
// debugger, eBPF tooling) is a case for a wider hand-written profile, not for
// widening every sandbox.
var nestedSyscalls = []string{
	"add_key",
	"chroot",
	"clone",
	"clone3",
	"fsconfig",
	"fsmount",
	"fsopen",
	"fspick",
	"keyctl",
	"listmount",
	"mount",
	"mount_setattr",
	"move_mount",
	"open_tree",
	"open_tree_attr",
	"pivot_root",
	"request_key",
	"setdomainname",
	"sethostname",
	"setns",
	"statmount",
	"umount",
	"umount2",
	"unshare",
}

// build merges the vendored base with nestedSyscalls: every base entry is
// STRIPPED of those names first (the base carries caps-conditional ALLOW/ERRNO
// pairs for some of them — overlapping rules with different actions are
// runtime-dependent, so strip-then-append is the only reliable shape), then
// one unconditional SCMP_ACT_ALLOW group is appended. Decoding to maps keeps
// unknown base fields intact, and json.Marshal's sorted keys make the output
// deterministic — the content hash in the file name depends on that.
func build() ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(baseProfile, &doc); err != nil {
		return nil, fmt.Errorf("parse embedded base profile: %w", err)
	}
	entries, ok := doc["syscalls"].([]any)
	if !ok {
		return nil, fmt.Errorf("embedded base profile: no syscalls array")
	}
	drop := make(map[string]bool, len(nestedSyscalls))
	for _, s := range nestedSyscalls {
		drop[s] = true
	}
	kept := make([]any, 0, len(entries)+1)
	for _, e := range entries {
		entry, ok := e.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("embedded base profile: malformed syscalls entry")
		}
		names, ok := entry["names"].([]any)
		if !ok {
			return nil, fmt.Errorf("embedded base profile: syscalls entry without names")
		}
		filtered := make([]any, 0, len(names))
		for _, n := range names {
			if name, ok := n.(string); !ok || !drop[name] {
				filtered = append(filtered, n)
			}
		}
		if len(filtered) == 0 {
			continue
		}
		entry["names"] = filtered
		kept = append(kept, entry)
	}
	allowed := make([]any, len(nestedSyscalls))
	for i, s := range nestedSyscalls {
		allowed[i] = s
	}
	kept = append(kept, map[string]any{
		"action": "SCMP_ACT_ALLOW",
		"args":   []any{},
		"names":  allowed,
	})
	doc["syscalls"] = kept
	return json.Marshal(doc)
}

var merged = sync.OnceValues(build)

// Profile returns the merged profile bytes — what Write puts on disk.
func Profile() ([]byte, error) { return merged() }

// Hash is the short content hash of the merged profile (12 hex chars).
func Hash() (string, error) {
	data, err := merged()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12], nil
}

// FileName is the content-addressed name the profile is written under. The
// hash in the name is what folds profile CONTENT into the session ConfigHash:
// the argv carries only the path, so a binary upgrade that changes the merged
// profile must change the path too, or live sessions would keep running under
// the old filter while reading as fresh (the image `var-` tag pattern).
func FileName() (string, error) {
	h, err := Hash()
	if err != nil {
		return "", err
	}
	return "seccomp-" + h + ".json", nil
}

// Write puts the profile into dir under FileName and returns the full path.
// Idempotent: an existing file is left alone (same name = same content), and
// the write goes through a temp file + rename so a concurrent invocation can
// never expose a half-written filter to the engine. Old-hash files from
// previous binaries are deliberately NOT pruned here: a stopped session's
// `start` re-reads the profile path it was created with, so the files live as
// long as the _meta dir they sit in.
func Write(dir string) (string, error) {
	data, err := merged()
	if err != nil {
		return "", err
	}
	name, err := FileName()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	tmp, err := os.CreateTemp(dir, ".seccomp-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}
