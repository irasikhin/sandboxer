package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// IDLen is how many hex characters of a sandbox's fingerprint the listing
// prints, and MinIDPrefix the shortest prefix a command accepts in place of it
// — docker's handle model: show a short id, take any prefix that is
// unambiguous. The floor exists so a short ordinary slug ("beef", "add") is not
// silently read as an id.
const (
	IDLen       = 8
	MinIDPrefix = 4
)

// ErrNoSuchID reports that a token matched no sandbox on this host. It is a
// sentinel because a caller that only GUESSED the token was an id (a bare
// positional could equally be a slug) must be able to fall back, while a real
// ambiguity — several sandboxes share the prefix — has to surface.
var ErrNoSuchID = errors.New("no such sandbox id")

// ID is a sandbox's stable short handle: a hash of the project's state
// directory and the slug. It is DERIVED, not assigned — no new bookkeeping, so
// nothing can go stale or be lost, and two projects that both hold a "feat"
// sandbox still get different handles. The state dir is keyed by the project's
// absolute path, so moving a project mints new ids for its sandboxes; the id is
// a handle for what `list` shows now, not a name to write down.
func ID(stateDir, slug string) string {
	sum := sha256.Sum256([]byte(stateDir + "\x00" + slug))
	return hex.EncodeToString(sum[:])[:IDLen]
}

// ID returns the short handle for one of this project's sandboxes.
func (b *Base) ID(slug string) string { return ID(b.Dir, slug) }

// Ref is a sandbox resolved host-wide: which project holds it and its slug.
type Ref struct {
	Project
	Slug string
}

// LooksLikeID reports whether tok is shaped like an id or an id prefix. Only
// lowercase hex of at least MinIDPrefix characters qualifies, which is what
// keeps the id lookup from hijacking ordinary slugs — a slug that happens to
// be short hex is still resolved as a slug first (see the caller).
func LooksLikeID(tok string) bool {
	if len(tok) < MinIDPrefix || len(tok) > IDLen {
		return false
	}
	for _, r := range tok {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// FindByID resolves an id (or an unambiguous prefix of one) to the sandbox it
// names, anywhere on this host — including a project whose directory is gone,
// which is exactly the state no cwd can reach. An ambiguous prefix is an error
// naming the candidates; an unknown one wraps ErrNoSuchID so a caller guessing
// at a bare positional can fall back to treating it as a slug.
func FindByID(prefix string) (Ref, error) {
	var hits []Ref
	for _, p := range Projects() {
		for _, slug := range p.Agents() {
			if strings.HasPrefix(p.ID(slug), prefix) {
				hits = append(hits, Ref{Project: p, Slug: slug})
			}
		}
	}
	switch len(hits) {
	case 0:
		return Ref{}, fmt.Errorf("%w: %s", ErrNoSuchID, prefix)
	case 1:
		return hits[0], nil
	}
	names := make([]string, 0, len(hits))
	for _, h := range hits {
		names = append(names, fmt.Sprintf("%s (%s in %s)", h.ID(h.Slug), h.Slug, h.Src))
	}
	return Ref{}, fmt.Errorf("id %q is ambiguous — matches %s", prefix, strings.Join(names, ", "))
}
