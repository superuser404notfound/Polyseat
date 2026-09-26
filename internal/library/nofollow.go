package library

import (
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Walking a member's library without following what the member put there.
//
// The daemon runs as root and every directory it reads from or writes into on
// behalf of a member belongs to somebody else. A seat's library is owned by the
// seat's mapped uid and mounted into the container, so the player can put a
// symlink anywhere in it, including absolute ones that the daemon resolves on
// the host rather than in the container. The host's own library and
// ~/Games/shared belong to the desktop user, who is not root either. A path
// under any of them that the daemon hands to the kernel as a string is a path
// the member chooses the meaning of, and before this file every one of them
// was: steamapps made a link to /etc had Ensure chown the host's configuration
// to the seat, a link planted where a manifest was written overwrote any file on
// the host, and a linked shared/ was harvested into the pool and handed to every
// other seat.
//
// So below the point where a member's directory starts, nothing is opened by
// path. The member's directory is opened once, and everything under it is
// reached one component at a time relative to the directory it is in, with
// O_NOFOLLOW, and acted on through the descriptor that open returned: fchown
// rather than chown, a rename between two held directories rather than between
// two paths. A symlink met on the way is refused, which the kernel does
// atomically as part of the open, so there is no moment between looking at an
// entry and using it in which it can be swapped.
//
// Symlinks inside a game are a different thing and still work: a game tree is
// copied link for link, as links, and nothing ever resolves them on the host.
//
// This is done with openat and O_NOFOLLOW component by component rather than
// with openat2 and RESOLVE_NO_SYMLINKS. The two refuse the same links, but
// openat2 arrived in Linux 5.6, and the Debian and Fedora hosts this runs on
// are not all guaranteed to be newer; a guard that turns into ENOSYS on an
// older kernel has to fall back to something, and that something would be this.
//
// Considered and not done: switching the thread to the member's uid with
// setfsuid, so that following a member's link would only reach what the member
// could reach anyway. It does not fit the work. Every clone reads with one
// identity and writes with the other, since the pool keeps each file's mode and
// belongs to root, so a 0600 file there cannot be read as the seat's uid, while
// the harvest direction writes into the pool, which must not be done as the
// seat. FICLONE wants both descriptors at once. The credential is also per
// thread, so it would hold only while the goroutine stayed locked to its thread
// and nothing on the way started another one, which is a property of every
// future edit to this package rather than of this one. And it could not be
// tested here without root, which is the kind of second layer CONTRIBUTING
// warns about: one that reads as protection and has never been seen to fire.

// errLink is what a symlink met where the library does not follow one becomes.
var errLink = errors.New("is a symlink, and the shared library does not follow links it did not make")

// errNotRegular is a manifest or a game file that is not a plain file.
var errNotRegular = errors.New("is not a regular file")

// dirFlags opens a directory without following a link in its place.
const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// fdOf is the descriptor of an open directory or file.
//
// The *os.File has to stay referenced until the caller is done with the number,
// or its finalizer can close it underneath; every caller here holds it in a
// variable with a deferred Close, which is what keeps it alive.
func fdOf(f *os.File) int { return int(f.Fd()) }

// pathError names what failed the way the rest of the package does, and turns
// the kernel's ELOOP from an O_NOFOLLOW open into something a person reading
// the report can act on.
func pathError(op string, dir *os.File, name string, err error) error {
	if errors.Is(err, unix.ELOOP) {
		err = errLink
	}

	return &os.PathError{Op: op, Path: filepath.Join(dir.Name(), name), Err: err}
}

// component refuses a name that is not a single directory entry. Every name
// reaching the functions below is one, and this is what keeps a slash in one
// from quietly turning a relative open back into a walk the kernel does.
func component(name string) error {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return fmt.Errorf("%q is not a single path component", name)
	}

	return nil
}

func retry(fn func() (int, error)) (int, error) {
	for {
		n, err := fn()
		if !errors.Is(err, unix.EINTR) {
			return n, err
		}
	}
}

// openOwn opens a directory that belongs to the daemon, following whatever
// links are on the way to it. Only for the pool's own directories and for the
// path wrappers the tests use, where every component was put there by root.
func openOwn(path string) (*os.File, error) {
	fd, err := retry(func() (int, error) {
		return unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	})
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}

	return os.NewFile(uintptr(fd), path), nil
}

// openDirAt opens one directory entry of dir, refusing a link in its place.
func openDirAt(dir *os.File, name string) (*os.File, error) {
	if err := component(name); err != nil {
		return nil, err
	}

	fd, err := retry(func() (int, error) {
		return unix.Openat(fdOf(dir), name, dirFlags, 0)
	})
	if err != nil {
		// With O_DIRECTORY the kernel answers ENOTDIR for a link rather than
		// the ELOOP O_NOFOLLOW alone gives, the same as for a plain file. The
		// open has already refused it either way; this only decides which of
		// the two the message says, and a link that turns into a file in the
		// meantime is reported as a file, which is then true.
		var st unix.Stat_t
		if errors.Is(err, unix.ENOTDIR) &&
			unix.Fstatat(fdOf(dir), name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil &&
			st.Mode&unix.S_IFMT == unix.S_IFLNK {
			err = unix.ELOOP
		}

		return nil, pathError("open", dir, name, err)
	}

	return os.NewFile(uintptr(fd), filepath.Join(dir.Name(), name)), nil
}

// openRel opens a relative path below dir one component at a time.
func openRel(dir *os.File, rel string) (*os.File, error) {
	cur, err := dupDir(dir)
	if err != nil {
		return nil, err
	}

	for _, part := range strings.Split(filepath.Clean(rel), "/") {
		if part == "." {
			continue
		}

		next, err := openDirAt(cur, part)
		cur.Close()

		if err != nil {
			return nil, err
		}

		cur = next
	}

	return cur, nil
}

// dupDir is a second handle on an open directory, so that a walk starting from
// it can close what it opens without closing what the caller holds.
func dupDir(dir *os.File) (*os.File, error) {
	fd, err := unix.FcntlInt(uintptr(fdOf(dir)), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "dup", Path: dir.Name(), Err: err}
	}

	return os.NewFile(uintptr(fd), dir.Name()), nil
}

// ensureDirAt opens a directory entry, creating it first when it is missing,
// and reports whether it did. A link in its place is refused like anywhere else:
// mkdirat does not follow one, it answers EEXIST, and the open then refuses it.
func ensureDirAt(dir *os.File, name string, perm os.FileMode) (*os.File, bool, error) {
	if err := component(name); err != nil {
		return nil, false, err
	}

	created := true

	if err := unix.Mkdirat(fdOf(dir), name, uint32(perm.Perm())); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return nil, false, pathError("mkdir", dir, name, err)
		}

		created = false
	}

	f, err := openDirAt(dir, name)

	return f, created, err
}

// lstatAt describes an entry without following it.
func lstatAt(dir *os.File, name string) (unix.Stat_t, error) {
	var st unix.Stat_t

	if err := component(name); err != nil {
		return st, err
	}

	if err := unix.Fstatat(fdOf(dir), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return st, pathError("lstat", dir, name, err)
	}

	return st, nil
}

// isDirAt reports whether an entry is a directory, a link to one not counting.
func isDirAt(dir *os.File, name string) bool {
	st, err := lstatAt(dir, name)

	return err == nil && st.Mode&unix.S_IFMT == unix.S_IFDIR
}

// openFileAt opens a regular file for reading and refuses anything else.
//
// O_NONBLOCK because the check that it is a regular file can only be made once
// it is open, and opening a fifo for reading blocks until somebody opens the
// other end. A seat that named a fifo like a manifest held the whole sync loop
// with it, for every seat, forever.
func openFileAt(dir *os.File, name string) (*os.File, unix.Stat_t, error) {
	var st unix.Stat_t

	if err := component(name); err != nil {
		return nil, st, err
	}

	fd, err := retry(func() (int, error) {
		return unix.Openat(fdOf(dir), name,
			unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	})
	if err != nil {
		return nil, st, pathError("open", dir, name, err)
	}

	f := os.NewFile(uintptr(fd), filepath.Join(dir.Name(), name))

	if err := unix.Fstat(fd, &st); err != nil {
		f.Close()

		return nil, st, pathError("stat", dir, name, err)
	}

	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		f.Close()

		return nil, st, pathError("open", dir, name, errNotRegular)
	}

	return f, st, nil
}

// createFileAt creates a file that must not exist yet. O_EXCL never follows a
// link either, so a link planted under the name fails the create rather than
// redirecting it.
func createFileAt(dir *os.File, name string, perm os.FileMode) (*os.File, error) {
	if err := component(name); err != nil {
		return nil, err
	}

	fd, err := retry(func() (int, error) {
		return unix.Openat(fdOf(dir), name,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm.Perm()))
	})
	if err != nil {
		return nil, pathError("create", dir, name, err)
	}

	return os.NewFile(uintptr(fd), filepath.Join(dir.Name(), name)), nil
}

// tempName is a name for something the daemon makes next to what it replaces.
func tempName(prefix string) string {
	return prefix + strconv.FormatUint(uint64(rand.Uint32()), 36)
}

// mkdirTempAt creates a fresh directory under dir, readable by root alone until
// the caller says otherwise, and returns its name and a handle on it.
func mkdirTempAt(dir *os.File, prefix string) (string, *os.File, error) {
	for range 100 {
		name := tempName(prefix)

		err := unix.Mkdirat(fdOf(dir), name, 0o700)
		if errors.Is(err, unix.EEXIST) {
			continue
		}

		if err != nil {
			return "", nil, pathError("mkdir", dir, name, err)
		}

		f, err := openDirAt(dir, name)
		if err != nil {
			return "", nil, err
		}

		return name, f, nil
	}

	return "", nil, fmt.Errorf("%s: no free name for a temporary directory", dir.Name())
}

// readNames lists a directory from its start, whatever was read from it before.
func readNames(dir *os.File) ([]string, error) {
	if _, err := dir.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	return dir.Readdirnames(-1)
}

// removeAllAt removes an entry and everything below it without following a
// link anywhere, which os.RemoveAll does not promise for the path leading to
// the entry.
func removeAllAt(dir *os.File, name string) error {
	if err := component(name); err != nil {
		return err
	}

	err := unix.Unlinkat(fdOf(dir), name, 0)
	if err == nil || errors.Is(err, unix.ENOENT) {
		return nil
	}

	if !errors.Is(err, unix.EISDIR) && !errors.Is(err, unix.EPERM) {
		return pathError("unlink", dir, name, err)
	}

	sub, err := openDirAt(dir, name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		return err
	}

	err = emptyDir(sub)
	sub.Close()

	if err != nil {
		return err
	}

	if err := unix.Unlinkat(fdOf(dir), name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return pathError("rmdir", dir, name, err)
	}

	return nil
}

// emptyDir removes everything inside an open directory.
func emptyDir(dir *os.File) error {
	names, err := readNames(dir)
	if err != nil {
		return err
	}

	for _, name := range names {
		if err := removeAllAt(dir, name); err != nil {
			return err
		}
	}

	return nil
}

// renameAt moves an entry between two held directories. rename(2) never
// follows a link in either name, it moves the link.
func renameAt(from *os.File, fromName string, to *os.File, toName string) error {
	if err := component(fromName); err != nil {
		return err
	}

	if err := component(toName); err != nil {
		return err
	}

	if err := unix.Renameat(fdOf(from), fromName, fdOf(to), toName); err != nil {
		return pathError("rename", from, fromName, err)
	}

	return nil
}

// omit leaves a timestamp as it is.
var omit = unix.Timespec{Nsec: unix.UTIME_OMIT}

// mtimeOf is a stat's modification time.
func mtimeOf(st *unix.Stat_t) time.Time {
	return time.Unix(int64(st.Mtim.Sec), int64(st.Mtim.Nsec))
}

// setMtime stamps an open file or directory with a modification time.
//
// Through /proc/self/fd, which is how a descriptor is named to utimensat on
// Linux; glibc's futimens does the same. It reaches the inode the descriptor
// holds, not whatever the path it was opened by leads to now.
func setMtime(f *os.File, mtime unix.Timespec) error {
	err := unix.UtimesNanoAt(unix.AT_FDCWD, "/proc/self/fd/"+strconv.Itoa(fdOf(f)),
		[]unix.Timespec{omit, mtime}, 0)
	if err != nil {
		return &os.PathError{Op: "utimes", Path: f.Name(), Err: err}
	}

	return nil
}

// setMtimeAt stamps an entry itself, a link included, without following it.
func setMtimeAt(dir *os.File, name string, mtime unix.Timespec) error {
	if err := unix.UtimesNanoAt(fdOf(dir), name, []unix.Timespec{omit, mtime}, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return pathError("utimes", dir, name, err)
	}

	return nil
}

// openAnchored opens a directory given as an absolute path that runs through
// somebody else's files, which is what an external member's library is.
//
// A symlink on the way is followed only where the directory holding it can be
// written by nobody but root. That keeps what systems ship working, /home as a
// link to /var/home or /lib to usr/lib, and refuses every link the owner of the
// library could have made: in their home, in the library, or in a directory
// they share with others. Such a link is not an error of theirs to be worked
// around; it is the one thing that would let them aim the daemon's root at
// files that are not theirs.
func openAnchored(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%s is not an absolute path", path)
	}

	for range 40 {
		dir, next, err := walkAnchored(path)
		if err != nil {
			return nil, err
		}

		if dir != nil {
			return dir, nil
		}

		path = next
	}

	return nil, &os.PathError{Op: "open", Path: path, Err: unix.ELOOP}
}

// walkAnchored makes one pass over a path. It answers with the directory when
// the path had no link in it, and with the path to try next when it met a link
// it may follow.
func walkAnchored(path string) (*os.File, string, error) {
	cur, err := openOwn("/")
	if err != nil {
		return nil, "", err
	}

	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
	resolved := "/"

	for i, part := range parts {
		if part == "" {
			continue
		}

		next, err := openDirAt(cur, part)
		if err == nil {
			cur.Close()
			cur = next
			resolved = filepath.Join(resolved, part)

			continue
		}

		if !errors.Is(err, errLink) || !rootOnly(cur) {
			cur.Close()

			return nil, "", err
		}

		target, err := readlinkAt(cur, part)
		cur.Close()

		if err != nil {
			return nil, "", err
		}

		rest := filepath.Join(parts[i+1:]...)

		if filepath.IsAbs(target) {
			return nil, filepath.Join(target, rest), nil
		}

		// Lexically, which is right here and only here: resolved has no links
		// left in it, so a .. in the target means the directory it names.
		return nil, filepath.Join(resolved, target, rest), nil
	}

	return cur, "", nil
}

// rootOnly reports whether nobody but root can change what a directory holds.
func rootOnly(dir *os.File) bool {
	var st unix.Stat_t

	if err := unix.Fstat(fdOf(dir), &st); err != nil {
		return false
	}

	return st.Uid == 0 && st.Mode&0o022 == 0
}

// readlinkAt reads a link's target.
func readlinkAt(dir *os.File, name string) (string, error) {
	for size := 256; ; size *= 2 {
		buf := make([]byte, size)

		n, err := unix.Readlinkat(fdOf(dir), name, buf)
		if err != nil {
			return "", pathError("readlink", dir, name, err)
		}

		if n < size {
			return string(buf[:n]), nil
		}
	}
}
