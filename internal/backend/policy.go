package backend

import (
	"errors"
	"strings"
)

// This file holds the backend-neutral session policy: the pure decisions that
// converge a persistent session, plus the data shape an inspect reveals. None
// of it shells out to an engine, so it is unit-testable without one AND shared
// verbatim by every isolation backend (container today, microVM next) — a
// second backend supplies its own inspect and argv, but reuses this decision
// table so the two can never disagree on when a session is stale.

// errEmptyAllowlist rejects an egress-required run with nothing on the
// allowlist: that is always a misconfiguration, never a "block everything"
// mode. Shared by the one-shot Run and the persistent-session paths so both
// fail closed with the same guidance.
var errEmptyAllowlist = errors.New("egress allowlist is enabled but no domains are allowed — " +
	"set --allow-domains / egress.allowedDomains, or disable the allowlist " +
	"(egress.enabled = false, or SANDBOXER_NO_EGRESS=1)")

// egressRequired reports whether o must run behind the egress allowlist
// sidecar: enabled (egress.enabled = true, the default) and not killed by the
// operator (NoEgress / SANDBOXER_NO_EGRESS). A configured proxy no longer
// disables the allowlist — with the allowlist on the proxy is CHAINED through
// the sidecar; only egress.enabled = false drops to direct mode. The single
// policy predicate for Run and the session lifecycle — they must never disagree,
// because the session ConfigHash depends on it.
func egressRequired(o RunOpts) bool {
	return !o.NoEgress && o.RT.Egress
}

// SessionInfo is what a single engine inspect reveals about a session
// container: whether it exists at all, whether it is currently running, the
// ConfigHash it was created with (from the LabelHash label; "" when the label
// is missing, which compares as stale against any wanted hash), and the ID of
// the image it runs (normalized without the "sha256:" prefix; "" when
// unreadable, which skips the image-freshness check), and the encoded mount
// identities it was created with (from LabelMounts; "" on a session created
// before that label existed, or one whose set was over the encoder's size cap
// — "unknown", never "nothing was mounted").
type SessionInfo struct {
	Exists  bool
	Running bool
	Hash    string
	ImageID string
	Mounts  string
}

// ImageFresh reports whether the session container's image (got, from
// SessionInfo.ImageID) still is the image the engine would run today (want,
// from ImageID). Either side unknown ("") skips the check — freshness then
// rests on the config hash alone — so a not-yet-built image never reads as
// stale. IDs are compared after trimming the "sha256:" prefix because docker
// reports it and podman does not.
func ImageFresh(got, want string) bool {
	if got == "" || want == "" {
		return true
	}
	return strings.TrimPrefix(got, "sha256:") == strings.TrimPrefix(want, "sha256:")
}

// sessionAction is planSession's verdict on how to converge a session.
type sessionAction int

const (
	actCreate   sessionAction = iota // no container — create one
	actStart                         // stopped container, fresh config — start it
	actExec                          // running container, fresh config — use as-is
	actRecreate                      // config changed — replace it (announced)
)

// planSession is the pure session-lifecycle policy: given what exists (info)
// and what the caller wants (wantHash + wantImageID), it picks the one action
// that converges the session. Fresh means the recorded config hash matches
// AND the container still runs the image the engine would use today
// (ImageFresh — a rebuilt image under an unchanged tag keeps the hash but
// flips the ID; either ID unknown skips that half). A stale session is always
// recreated (with a notice): sandboxer tracks no in-container clients —
// park long-running work (the in-container tmux dies with its container).
// Engine-free so the decision table is unit-testable:
//
//	not found        → create
//	stopped + fresh  → start
//	stopped + stale  → recreate
//	running + fresh  → exec
//	running + stale  → recreate
func planSession(info SessionInfo, wantHash, wantImageID string) sessionAction {
	fresh := info.Hash == wantHash && ImageFresh(info.ImageID, wantImageID)
	switch {
	case !info.Exists:
		return actCreate
	case !info.Running:
		if fresh {
			return actStart
		}
		return actRecreate
	case fresh:
		return actExec
	default:
		return actRecreate
	}
}

// staleReason names what invalidated a session, for the user-facing notices:
// a config-hash mismatch means the profile (or environment) changed, while a
// hash-fresh container that still planned as stale runs an image that was
// rebuilt under its unchanged tag.
func staleReason(info SessionInfo, wantHash string) string {
	if info.Hash == wantHash {
		return "image rebuilt"
	}
	return "profile changed"
}
