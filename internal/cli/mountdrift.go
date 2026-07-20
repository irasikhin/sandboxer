package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

// mountDriftWhy explains why a running session's config hash no longer matches,
// and reports whether the source mounts are part of the answer.
//
// The hash is one opaque value, so every mismatch used to be announced as
// "profile changed" — which is wrong most of the time it matters. A narrowed
// sandbox re-expands its include patterns against the live worktree on every
// enter, so the mount set moves whenever the HOST moves: a directory that now
// matches a pattern, or (the silent one) a mounted directory a checkout or a
// build removed and recreated, leaving the running container's bind mount
// pinned to an inode nobody writes to any more. Told "profile changed" for a
// profile they never touched, the user has no reason to act on it.
//
// The recorded identities (LabelMounts) make the diff possible. Callers that
// have no baseline — a session created before the label existed, or one whose
// set was over the encoder's size cap — get the honest old answer rather than
// a fabricated "everything is new".
// The reason comes back in two pieces on purpose. why is short enough to read
// inside a banner sentence ("the session is stale (…)"); detail is the
// per-path diff, printed once on its own line. Folding the diff into why made
// the banner a paragraph and, when the prompt showed it too, printed the same
// list of paths twice in one screen.
func mountDriftWhy(o backend.RunOpts, info backend.SessionInfo, currentIDs string) (why, detail string, drift bool) {
	recorded := sandbox.DecodeMountIDs(info.Mounts)
	changes := sandbox.DiffMounts(recorded, sandbox.DecodeMountIDs(currentIDs))
	if len(changes) == 0 {
		return "profile changed", "", false
	}
	why = "mounts moved"
	detail = "sandboxer: mounts moved: " + sandbox.DescribeMountChanges(changes)
	// The mount set and the profile can move in the same breath, and naming
	// only the mounts would be a fresh instance of the very bug this fixes.
	// Rebuild the hash the session WOULD have if only the mounts had changed:
	// equal to the recorded one means the mounts are the whole story.
	was := o
	was.SrcMounts = sandbox.MountPaths(recorded)
	was.MountGen = sandbox.FingerprintIDs(recorded)
	was.MountIDs = info.Mounts
	if backendWantHash(was) != info.Hash {
		why += "; the profile also changed"
	}
	return why, detail, true
}

// confirmRecreate asks whether to rebuild a stale-but-busy session whose source
// mounts moved. It is the one interactive prompt in the CLI, and it earns that
// exception: the session is ALREADY broken (its bind mounts name directories
// the host has moved on from), but rebuilding it destroys whatever the tmux
// session is running — an unattended agent, most of the time. Neither outcome
// is safe to pick on the user's behalf, and only they can weigh the two.
//
// Answer anything but yes and nothing happens: enter attaches as-is, exactly as
// it did before. `--recreate` remains the non-interactive answer, which is why
// this is never asked when stdin is not a terminal — a scripted enter must
// never block on a question nobody is there to read.
func confirmRecreate(in io.Reader, out io.Writer, slug string) bool {
	fmt.Fprintf(out, "sandboxer: this session's mounts point at directories the host has replaced — what runs in there sees the old ones.\n")
	fmt.Fprintf(out, "sandboxer: rebuilding fixes that and DESTROYS the running tmux session (its work in %s survives on the host).\n", slug)
	fmt.Fprint(out, "Rebuild the session now? [y/N] ")
	line, err := readLine(in)
	if err != nil {
		fmt.Fprintln(out)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// readLine reads one line one BYTE at a time. A buffered reader would be the
// obvious choice and is the wrong one here: it reads ahead into its buffer, and
// whatever it swallowed past the newline never reaches the container that takes
// this same stdin moments later. Reading exactly the bytes of the answer leaves
// the stream untouched for the session shell.
func readLine(r io.Reader) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return b.String(), nil
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			if b.Len() > 0 {
				return b.String(), nil
			}
			return "", err
		}
	}
}
