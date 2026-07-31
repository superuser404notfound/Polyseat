package seat

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/superuser404notfound/Polyseat/internal/library"
)

// hostMember is the name the host's own Steam library goes by in the pool.
//
// The machine everybody is sitting at is a member like any seat: games
// installed in a seat are cloned into it, and games installed on it are taken
// into the pool. Without this the pool was one directional for the host, and a
// game somebody installed in a seat could only be got at by downloading it
// again on the host, which is the exact cost this whole thing exists to avoid.
//
// ValidateName reserves the name, so no seat can take these records over.
const hostMember = "host"

// hostMembers builds the host's side of the pool from the libraries it tracks.
//
// Only the first one. A game cloned into two library folders of the same Steam
// client is installed twice as far as that client is concerned, and it has no
// good way to decide which copy is the real one. The others stay what they were
// before, libraries the pool takes games from, and the interface says which one
// receives.
func (m *Manager) hostMembers() []library.Member {
	if m.pool == nil {
		return nil
	}

	sources := m.pool.Sources()
	if len(sources) == 0 {
		return nil
	}

	apps := sources[0]

	// The ownership comes off the directory rather than out of the
	// configuration. The daemon runs as root, so files it writes would be
	// root's, and a Steam running as the person who owns that library could
	// then neither update nor delete the game it was handed.
	info, err := os.Stat(apps)
	if err != nil {
		return nil
	}

	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}

	return []library.Member{{
		Name:      hostMember,
		Apps:      apps,
		Owner:     library.Owner{UID: int(st.Uid), GID: int(st.Gid)},
		Updatable: hostIdle(apps, int(st.Uid)),
	}}
}

// hostIdle answers, for the host, the same question libraryIdle answers for a
// seat: is anything using these files right now.
//
// Written in Go against /proc rather than as the shell fragment the seats get,
// because here there is no container to reach into and no reason to fork. The
// walk is restricted to processes belonging to the library's owner, which is
// what keeps it cheap on a host that also runs every seat's processes: those
// belong to a mapped uid and cannot have this directory open anyway.
//
// Answers no on any doubt. Leaving a title one build behind for another minute
// costs nothing and the interface says an update is waiting; replacing files
// under a running game corrupts an install.
func hostIdle(dir string, uid int) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}

	// The trailing separator is what makes this a path test rather than a text
	// test. Without it a library at /home/x/Steam/steamapps would be reported
	// busy by anything holding /home/x/Steam/steamapps-backup open.
	needle := []byte(dir + string(filepath.Separator))

	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}

		proc := filepath.Join("/proc", entry.Name())

		info, err := os.Stat(proc)
		if err != nil {
			// Exited between the listing and now, so it is using nothing.
			continue
		}

		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != uid {
			continue
		}

		// Mapped files first: a game that is running has its executable and
		// most of its data mapped, and this is one read per process rather than
		// one per open file.
		if data, err := os.ReadFile(filepath.Join(proc, "maps")); err == nil {
			if bytes.Contains(data, needle) {
				return false
			}
		}

		if using(filepath.Join(proc, "cwd"), needle) {
			return false
		}

		fds, err := os.ReadDir(filepath.Join(proc, "fd"))
		if err != nil {
			continue
		}

		for _, fd := range fds {
			if using(filepath.Join(proc, "fd", fd.Name()), needle) {
				return false
			}
		}
	}

	return true
}

// using reports whether a /proc symlink points inside the directory.
func using(link string, needle []byte) bool {
	target, err := os.Readlink(link)
	if err != nil {
		return false
	}

	return bytes.HasPrefix([]byte(target), needle)
}
