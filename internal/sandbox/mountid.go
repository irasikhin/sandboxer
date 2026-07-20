package sandbox

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// MountID pairs an individual source mount with the on-disk identity of the
// object currently at that path (see statIdentity). It is the material behind
// RunOpts.MountGen, kept as data rather than folded straight into a hash: the
// hash answers "did the mount set move?", the identities answer "how" — which
// directory appeared, which one a host-side checkout recreated under the live
// session's feet, which one is gone. That difference is the whole point, since
// a session whose bind mount is pinned to a dead inode reads as "stale" and
// used to be reported as a profile change it never was.
type MountID struct {
	Path string
	ID   string
}

// MountIdentities stats each mount and pairs it with its object identity, in
// the given order (Mounts sorts them). nil in → nil out, so a sandbox whose
// only mount is the inode-stable <slug>/ root records nothing.
func MountIdentities(mounts []string) []MountID {
	if len(mounts) == 0 {
		return nil
	}
	ids := make([]MountID, 0, len(mounts))
	for _, m := range mounts {
		ids = append(ids, MountID{Path: m, ID: inodeID(m)})
	}
	return ids
}

// FingerprintIDs is MountFingerprint's pure half: sha256 over path\x00id\x00
// per entry, truncated to 16 hex. Empty in → empty out (see MountFingerprint
// for why that emptiness is load-bearing).
func FingerprintIDs(ids []MountID) string {
	if len(ids) == 0 {
		return ""
	}
	h := sha256.New()
	for _, id := range ids {
		fmt.Fprintf(h, "%s\x00%s\x00", id.Path, id.ID)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// maxEncodedMountIDs caps the encoded label value. A sandbox with hundreds of
// view dirs would otherwise push an unbounded string through the engine's
// label store and back through an inspect template. Over the cap the value is
// dropped entirely rather than truncated: a truncated list would decode into a
// plausible-looking but WRONG diff, while an empty one degrades to the honest
// "profile changed" fallback.
const maxEncodedMountIDs = 32 << 10

// EncodeMountIDs renders ids for the sandboxer.mounts label. The payload is
// NUL-joined (a byte no POSIX path may contain) and base64url-encoded without
// padding, so the value stays within [A-Za-z0-9_-] — critically it contains no
// SPACE, because InspectSession parses its inspect template by splitting on
// single spaces and a raw path with a space in it would shift every field.
func EncodeMountIDs(ids []MountID) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ids)*2)
	for _, id := range ids {
		parts = append(parts, id.Path, id.ID)
	}
	enc := base64.RawURLEncoding.EncodeToString([]byte(strings.Join(parts, "\x00")))
	if len(enc) > maxEncodedMountIDs {
		return ""
	}
	return enc
}

// DecodeMountIDs reverses EncodeMountIDs. Every malformed shape — bad base64,
// an odd number of fields — returns nil rather than an error: the value comes
// from a container label written by a possibly older sandboxer, and an
// unreadable one means "no recorded mounts", which the caller already handles.
func DecodeMountIDs(s string) []MountID {
	if s == "" {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts)%2 != 0 {
		return nil
	}
	ids := make([]MountID, 0, len(parts)/2)
	for i := 0; i < len(parts); i += 2 {
		ids = append(ids, MountID{Path: parts[i], ID: parts[i+1]})
	}
	return ids
}

// MountPaths projects the paths out of a decoded identity list, in order — the
// SrcMounts a recorded set corresponds to. nil in → nil out.
func MountPaths(ids []MountID) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.Path)
	}
	return out
}

// MountChangeKind classifies one entry of a mount-set diff.
type MountChangeKind int

const (
	// MountAdded: a path that is mounted now and was not before — on a narrowed
	// sandbox, a directory the host grew that an include pattern now matches.
	MountAdded MountChangeKind = iota
	// MountRecreated: the same path, a different object. This is the silent one:
	// the running container's bind mount still resolves to the OLD inode, so it
	// reads a directory the host has already thrown away.
	MountRecreated
	// MountRemoved: a path that was mounted and no longer resolves to anything
	// the current mount set names.
	MountRemoved
)

// Reason renders the kind as the clause shown next to the path.
func (k MountChangeKind) Reason() string {
	switch k {
	case MountAdded:
		return "now matches include"
	case MountRecreated:
		return "recreated on the host"
	case MountRemoved:
		return "gone"
	}
	return "changed"
}

// Sign renders the kind as the leading diff marker.
func (k MountChangeKind) Sign() string {
	switch k {
	case MountAdded:
		return "+"
	case MountRecreated:
		return "~"
	case MountRemoved:
		return "-"
	}
	return "?"
}

// MountChange is one path's verdict in a mount-set diff.
type MountChange struct {
	Path string
	Kind MountChangeKind
}

// String renders one change as "<sign> <path> (<reason>)".
func (c MountChange) String() string {
	return c.Kind.Sign() + " " + c.Path + " (" + c.Kind.Reason() + ")"
}

// DiffMounts compares the mount identities a session was created with against
// the ones a fresh resolve just produced, and returns what moved, sorted by
// path for a stable message. Pure and engine-free.
//
// An empty recorded set yields no changes rather than "everything is new": it
// means the session predates the label (or the value was over the size cap),
// and inventing a full diff from an absent baseline would be a confident lie.
func DiffMounts(recorded, current []MountID) []MountChange {
	if len(recorded) == 0 {
		return nil
	}
	was := make(map[string]string, len(recorded))
	for _, id := range recorded {
		was[id.Path] = id.ID
	}
	now := make(map[string]string, len(current))
	for _, id := range current {
		now[id.Path] = id.ID
	}
	var out []MountChange
	for _, id := range current {
		switch prev, ok := was[id.Path]; {
		case !ok:
			out = append(out, MountChange{Path: id.Path, Kind: MountAdded})
		case prev != id.ID:
			out = append(out, MountChange{Path: id.Path, Kind: MountRecreated})
		}
	}
	for _, id := range recorded {
		if _, ok := now[id.Path]; !ok {
			out = append(out, MountChange{Path: id.Path, Kind: MountRemoved})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// DescribeMountChanges renders a diff as one line: "+ /a (now matches
// include); ~ /b (recreated on the host)". Empty in → empty out.
func DescribeMountChanges(changes []MountChange) string {
	if len(changes) == 0 {
		return ""
	}
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		parts = append(parts, c.String())
	}
	return strings.Join(parts, "; ")
}
