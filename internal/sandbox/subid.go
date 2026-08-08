package sandbox

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// nestedUser is the name the generated identity files give the sandbox uid.
// Purely container-local: nothing on the host knows it, and the subordinate
// ranges are looked up by this name (newuidmap resolves the calling uid to a
// name via the mounted passwd, then finds its ranges in the mounted subuid).
const nestedUser = "sandbox"

// NestedIDFiles are the per-sandbox generated identity files a
// nestedContainers profile mounts read-only over /etc/{passwd,group,subuid,
// subgid} — the material the nested rootless podman builds a MULTI-uid user
// namespace from. Field-identical to backend.NestedIDFiles so the CLI can
// convert directly.
type NestedIDFiles struct {
	Passwd, Group, Subuid, Subgid string
}

// NestedIDFiles locates the four generated identity files under _meta.
func (b *Base) NestedIDFiles(slug string) NestedIDFiles {
	return NestedIDFiles{
		Passwd: filepath.Join(b.metaDir(), slug+".passwd"),
		Group:  filepath.Join(b.metaDir(), slug+".group"),
		Subuid: filepath.Join(b.metaDir(), slug+".subuid"),
		Subgid: filepath.Join(b.metaDir(), slug+".subgid"),
	}
}

// WriteNestedIDFiles generates the identity files for slug from the INVOKING
// host user: a minimal passwd/group naming the sandbox uid, and subuid/subgid
// handing it every other id available in the outer container's user
// namespace. They exist because the sandbox uid is host-dependent, so nothing
// buildable into the image can carry them.
//
// The subordinate ranges must name ids that EXIST in the outer namespace —
// under --userns=keep-id that namespace is exactly the host user's rootless
// namespace: ids 0..N where N is the user's total /etc/subuid allocation. So
// the ranges are [1..N] minus the sandbox's own uid (podman maps that itself,
// as the nested root), sized by reading the HOST's /etc/subuid|subgid.
//
// Returns false (writing nothing, and removing any stale files so they are
// never mounted) when the host user has no subordinate ranges — the outer
// namespace is then single-id and a multi-uid nested podman is impossible by
// construction — or when sandboxer runs as uid 0, where the rootless model
// does not apply.
func (b *Base) WriteNestedIDFiles(slug string) (bool, error) {
	uid, gid := os.Getuid(), os.Getgid()
	// BOTH files are keyed by the USER (login name or numeric uid — subuid(5));
	// /etc/subgid maps users to gid ranges, so the lookup id is uid either way.
	uidCount := hostSubIDCount(hostSubuidPath, uid)
	gidCount := hostSubIDCount(hostSubgidPath, uid)
	files := b.NestedIDFiles(slug)
	if uid == 0 || uidCount == 0 || gidCount == 0 {
		for _, p := range []string{files.Passwd, files.Group, files.Subuid, files.Subgid} {
			_ = os.Remove(p)
		}
		return false, nil
	}
	passwd := "root:x:0:0:root:/root:/bin/bash\n" +
		fmt.Sprintf("%s:x:%d:%d:%s:%s:/bin/bash\n", nestedUser, uid, gid, nestedUser, b.HomeDir(slug)) +
		"nobody:x:65534:65534:nobody:/nonexistent:/bin/false\n"
	group := "root:x:0:\n"
	if gid != 0 && gid != 65534 {
		group += fmt.Sprintf("%s:x:%d:\n", nestedUser, gid)
	}
	group += "nobody:x:65534:\n"
	for _, f := range []struct {
		path, content string
	}{
		{files.Passwd, passwd},
		{files.Group, group},
		{files.Subuid, subIDLines(uid, uidCount)},
		{files.Subgid, subIDLines(gid, gidCount)},
	} {
		if err := os.WriteFile(f.path, []byte(f.content), 0o644); err != nil {
			return false, err
		}
	}
	return true, nil
}

// Host subordinate-id databases; vars so tests can point at fixtures.
var (
	hostSubuidPath = "/etc/subuid"
	hostSubgidPath = "/etc/subgid"
)

// HostSubIDCounts reports how many subordinate uids and gids the host grants
// the invoking user — the material a MULTI-uid nested podman is built from.
// (0, 0) means a multi-uid nested namespace is impossible on this host. For
// doctor; the generation path reads the same data through WriteNestedIDFiles.
func HostSubIDCounts() (uids, gids int) {
	uid := os.Getuid()
	return hostSubIDCount(hostSubuidPath, uid), hostSubIDCount(hostSubgidPath, uid)
}

// hostSubIDCount sums the subordinate ranges /etc/subuid-style file path
// grants the invoking user, matched by login name or numeric id (subuid(5)
// allows either in the first field). 0 = no ranges (or no readable file).
func hostSubIDCount(path string, id int) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	names := []string{strconv.Itoa(id)}
	if u, err := user.Current(); err == nil && u.Username != "" {
		names = append(names, u.Username)
	}
	total := 0
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(strings.TrimSpace(line), ":")
		if len(fields) != 3 {
			continue
		}
		owner := fields[0]
		matched := false
		for _, n := range names {
			if owner == n {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if n, err := strconv.Atoi(fields[2]); err == nil && n > 0 {
			total += n
		}
	}
	return total
}

// subIDLines renders the subuid/subgid entries handing nestedUser every id of
// the outer namespace's [1..count] except its own — podman maps `own` itself
// (as the nested root), and a subordinate range containing it would be a
// double mapping newuidmap rejects.
func subIDLines(own, count int) string {
	var b strings.Builder
	writeRange := func(start, n int) {
		if n > 0 {
			fmt.Fprintf(&b, "%s:%d:%d\n", nestedUser, start, n)
		}
	}
	if own >= 1 && own <= count {
		writeRange(1, own-1)
		writeRange(own+1, count-own)
	} else {
		writeRange(1, count)
	}
	return b.String()
}
