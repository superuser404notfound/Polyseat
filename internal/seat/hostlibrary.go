package seat

import (
	"bytes"
	"os"
	"os/user"
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

	uid, gid := int(st.Uid), int(st.Gid)
	folders := hostFolders(uid)

	return []library.Member{{
		Name:    hostMember,
		Apps:    apps,
		Folders: folders,
		Owner:   library.Owner{UID: uid, GID: gid},

		// Both halves, because this one flag governs both. Without the second
		// term the daemon would ask whether anybody is using the Steam library
		// and then replace a folder game that is running out of the other
		// directory on the strength of that answer. hostIdle takes the
		// directory it is asked about, so the same probe covers it; an empty
		// path is nobody's and answers yes.
		Updatable: hostIdle(apps, uid) && hostIdle(folders, uid),
	}}
}

// hostSharedDir is where the host keeps the launcher agnostic folders, below
// the home of whoever owns the library the pool gives games to.
//
// The same shape a seat has, deliberately. A seat's Lutris is built with
// game_path at /home/player/games and its shared folders are the shared/
// beneath that, so the host reads ~/Games/shared and its Lutris wants game_path
// at ~/Games. That is also Lutris's own default on a fresh install, which means
// the ordinary way of installing a game is already the right one.
const hostSharedDir = "Games/shared"

// hostFolders is the host's side of the launcher agnostic pool, or empty when
// it is not taking part.
//
// Taking part is the directory existing, and that is the whole switch. The
// alternative was another path in the pool's state with the API and the
// interface to set it, for a question that has one sensible answer; and the
// daemon must not create it, because Ensure deliberately does nothing for an
// external member and making directories in somebody's home on their behalf is
// not a thing to start doing here. `mkdir -p ~/Games/shared` turns it on and
// removing it turns it off, which is a switch somebody can find without being
// told where the setting is.
//
// The home comes from the uid that owns the Steam library rather than from the
// environment, because the daemon runs as root and root's home is not where
// anybody's games are.
func hostFolders(uid int) string {
	who := ownerOf(uid)
	if who == nil || who.HomeDir == "" {
		return ""
	}

	return sharedIn(who.HomeDir)
}

// ownerOf is the passwd entry for one uid, or nil when the system has none.
//
// One lookup in one place, because two callers want different fields of the
// same answer and looking it up twice invites them to disagree about what to do
// when there is no answer at all.
func ownerOf(uid int) *user.User {
	who, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return nil
	}

	return who
}

// sharedIn is the folder directory below one home, or empty when it is not
// there.
//
// Split from hostFolders so the half with the decision in it can be tested
// against a temporary directory, rather than against whatever the person
// running the tests happens to have in their own home.
func sharedIn(home string) string {
	dir := filepath.Join(home, hostSharedDir)

	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ""
	}

	return dir
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
	// No directory is nobody's, and saying so here rather than at the call site
	// is what keeps the needle below from becoming "/", which every process on
	// the machine matches. A member that does not take part in one half of the
	// pool passes an empty path for it.
	if dir == "" {
		return true
	}

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
