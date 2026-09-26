// Package library shares game installs between seats without downloading them
// twice.
//
// The mechanism is reflink, not sharing. Every seat keeps its own private,
// fully writable Steam library; the daemon replicates game directories between
// them with the FICLONE ioctl, which copies metadata and leaves the data blocks
// shared. Install a game in one seat and it appears in the others in seconds
// and costs no additional space.
//
// The obvious alternative, mounting one directory into every seat, was
// rejected for reasons that are all fatal on their own:
//
//   - Two Steam clients writing the same steamapps directory corrupt it, and
//     there is no lock that reaches across containers.
//   - A read-only shared library makes Steam refuse to update and complain
//     about it continuously.
//   - OverlayFS copies a whole file up on first write, so patching a 60 GB game
//     costs 60 GB per seat, which defeats the entire point.
//
// With reflink none of that applies, because at the POSIX level nothing is
// shared. Each Steam sees an ordinary library it fully owns. Only when a seat
// updates a game do the copies diverge, and then only by the changed blocks.
//
// What this cannot do is grant licenses. Cloning the files into another seat
// does not give that seat's Steam account ownership of the game. Where the
// account owns it, Steam finds the files, validates them and plays without
// downloading; where it does not, Steam refuses. The saving is real for the
// common cases, two people who both own a game and one account used on several
// seats, and it is not a way around buying anything.
package library

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// ErrNoReflink is returned when the filesystem holding the pool cannot share
// blocks between files.
//
// A distinct error because the daemon has to say so plainly rather than fall
// back to something worse. A pool that silently turns into full copies fills a
// disk quietly, and by the time anybody notices there is no space left to fix
// it in.
var ErrNoReflink = errors.New("this filesystem cannot share blocks between files")

// SupportsReflink reports whether dir is on a filesystem that can clone.
//
// Measured rather than inferred from the filesystem name. Reflink support is a
// property of the mount and the kernel, not of the label: XFS only reflinks
// when the filesystem was created with reflink=1, and a btrfs subvolume with
// nodatacow behaves differently again. Writing a real block and cloning it is
// the only answer that is not a guess.
func SupportsReflink(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	src, err := os.CreateTemp(dir, ".reflink-probe-src-")
	if err != nil {
		return err
	}

	defer os.Remove(src.Name())
	defer src.Close()

	// A full block. An empty file can be "cloned" on filesystems that have
	// nothing to clone, so the probe has to carry data to mean anything.
	if _, err := src.Write(make([]byte, 4096)); err != nil {
		return err
	}

	if err := src.Sync(); err != nil {
		return err
	}

	dst, err := os.CreateTemp(dir, ".reflink-probe-dst-")
	if err != nil {
		return err
	}

	defer os.Remove(dst.Name())
	defer dst.Close()

	if err := unix.IoctlFileClone(int(dst.Fd()), int(src.Fd())); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrNoReflink, dir, err)
	}

	return nil
}

// SharesBlocks reports whether a file in from can be cloned into to.
//
// SupportsReflink asks whether one directory can clone at all. This asks the
// question the pool actually depends on, which is about a pair: cloneFile falls
// back to a byte copy on EXDEV, so two directories on two filesystems that both
// support reflinks perfectly well still cost a full copy of every game between
// them. On btrfs the cheap test would also be the wrong one, since every
// subvolume has its own device number while clones across them work.
//
// The probe is written into from, which is somebody's Steam library rather than
// ours. It is 4 KiB, it is named for us, and it is removed on every path out of
// here, including the error ones. Writing there is not a liberty we would not
// take anyway: the first library the pool tracks is the one it clones games
// into.
//
// Reached the way an external member is, since that is what from is about to
// become: a link somebody put on the way would otherwise have the daemon write
// the probe wherever it pointed.
func SharesBlocks(from, to string) error {
	dir, err := openAnchored(from)
	if err != nil {
		return err
	}

	defer dir.Close()

	name := tempName(".polyseat-probe-src-")

	// Read and write, since the clone below reads from it. O_EXCL does not
	// follow a link in the name's place either.
	fd, err := unix.Openat(fdOf(dir), name,
		unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return pathError("create", dir, name, err)
	}

	src := os.NewFile(uintptr(fd), filepath.Join(from, name))

	defer unix.Unlinkat(fdOf(dir), name, 0)
	defer src.Close()

	// A full block, for the reason SupportsReflink gives: an empty file can be
	// "cloned" where there is nothing to clone.
	if _, err := src.Write(make([]byte, 4096)); err != nil {
		return err
	}

	if err := src.Sync(); err != nil {
		return err
	}

	dst, err := os.CreateTemp(to, ".polyseat-probe-dst-")
	if err != nil {
		return err
	}

	defer os.Remove(dst.Name())
	defer dst.Close()

	if err := unix.IoctlFileClone(int(dst.Fd()), int(src.Fd())); err != nil {
		return fmt.Errorf("%w: %s and %s: %v", ErrNoReflink, from, to, err)
	}

	return nil
}

// Result reports what a clone did.
type Result struct {
	Files    int
	Dirs     int
	Symlinks int
	Bytes    int64

	// Copied counts files that had to be read and written in full because they
	// could not be cloned. Should be zero; anything else means the pool and the
	// destination are not on the same filesystem after all, and the saving is
	// gone.
	Copied int
}

// Owner is the uid and gid to give the cloned tree.
//
// Needed because the destination is read by an unprivileged user inside a
// container, and the daemon writes it as root on the host. Every seat maps
// container uid 1000 to the same host uid, so this is one number, but it is
// read from the container rather than assumed.
//
// Minus one in either field means leave it as it lands, following the
// convention of chown itself. That is what makes this package testable without
// root, which matters: the alternative is a clone path only ever exercised by
// the daemon on the real machine.
type Owner struct {
	UID int
	GID int
}

// Keep is an owner that changes nothing.
var Keep = Owner{UID: -1, GID: -1}

// Clone reflink-copies the tree at src to dst.
//
// The tree is built next to dst under a temporary name and moved into place at
// the end, so an interrupted clone never leaves a half-populated game directory
// that Steam would report as installed and then fail to launch.
//
// Every directory on the way to src and to dst is trusted, which is right for
// the tests that call this and for nothing else: the pool reaches a member's
// library through cloneAt, with the member's directory already open.
func Clone(src, dst string, owner Owner) (Result, error) {
	return clonePaths(src, dst, owner, false)
}

// CloneFolder is Clone for the shared folders, where what the seat owns lives
// inside the tree being copied rather than next to it.
//
// A destination that already has a wine prefix keeps that prefix's user
// directory: it is left out of the copy and carried across the swap, so an
// update to the game does not take the saves with it. A destination that has
// none is given the source's, which is what makes a game playable in a seat
// that has never seen it. See private.go for where the line is drawn and why.
func CloneFolder(src, dst string, owner Owner) (Result, error) {
	return clonePaths(src, dst, owner, true)
}

func clonePaths(src, dst string, owner Owner, private bool) (Result, error) {
	from, err := openOwn(filepath.Dir(src))
	if err != nil {
		return Result{}, err
	}

	defer from.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return Result{}, err
	}

	to, err := openOwn(filepath.Dir(dst))
	if err != nil {
		return Result{}, err
	}

	defer to.Close()

	return cloneAt(from, filepath.Base(src), to, filepath.Base(dst), owner, private)
}

// beforeOpen runs between looking at an entry and opening it. Only a test sets
// it, to swap a directory for a link at exactly the moment the old walk trusted
// what it had just been told.
var beforeOpen func(path string)

// cloneAt copies the directory srcName in srcDir to dstName in dstDir.
//
// Both sides are directories already open, and nothing below them is reached by
// path; see nofollow.go. private says the destination's seat private
// directories survive, which is what CloneFolder wants and Clone does not.
func cloneAt(srcDir *os.File, srcName string, dstDir *os.File, dstName string, owner Owner, private bool) (Result, error) {
	var result Result

	src, err := openDirAt(srcDir, srcName)
	if err != nil {
		return result, err
	}

	defer src.Close()

	var info unix.Stat_t

	if err := unix.Fstat(fdOf(src), &info); err != nil {
		return result, err
	}

	carry := map[string]bool{}
	if private {
		carry = privateDirsAt(dstDir, dstName)
	}

	// Made 0700 and owned by root, and kept that way until everything that
	// has to happen inside it has happened. It sits in the member's directory,
	// where the member can see its name; closed like this the member cannot
	// look into it or put anything there while the daemon is working.
	stagingName, staging, err := mkdirTempAt(dstDir, ".polyseat-clone-")
	if err != nil {
		return result, err
	}

	defer staging.Close()

	// The staging directory is removed on every path out of here that did not
	// move it into place, and it is what keeps a failed clone from occupying
	// space forever. Emptied through the handle and then removed by name only
	// if it is empty, so that if something else has taken the name in the
	// meantime it is left alone.
	keep := false

	defer func() {
		if !keep {
			emptyDir(staging)
			unix.Unlinkat(fdOf(dstDir), stagingName, unix.AT_REMOVEDIR)
		}
	}()

	if err := cloneTree(src, staging, "", owner, carry, &result); err != nil {
		return result, err
	}

	// Whatever was there before is moved aside rather than deleted first, so a
	// failure between the two renames leaves the old copy recoverable instead
	// of leaving the seat with nothing.
	previous := ""

	if _, err := lstatAt(dstDir, dstName); err == nil {
		previous = dstName + ".polyseat-old"

		if err := removeAllAt(dstDir, previous); err != nil {
			return result, err
		}

		if err := renameAt(dstDir, dstName, dstDir, previous); err != nil {
			return result, err
		}
	}

	var (
		old   *os.File
		moved map[string]bool
	)

	// Every failure from here on has to leave the destination as it was, and
	// that is two steps in this order: what was carried goes back out of the
	// staging tree, and then the old copy is renamed back. The other order, or
	// only the second step, restores the old copy without the saves that were
	// taken out of it, and the staging directory holding them is about to be
	// removed. That is how a carry that failed half way used to lose the half
	// it had already moved.
	//
	// Should putting them back fail as well, the staging directory is kept
	// rather than removed, and the error says where it is. Saves somebody has
	// to fish out by hand are still saves.
	rollback := func(err error) (Result, error) {
		if len(moved) > 0 {
			if _, back := carryPrivate(staging, old, moved, owner); back != nil {
				keep = true

				return result, fmt.Errorf("%w, and the saves could not be put back, they are in %s: %v",
					err, filepath.Join(dstDir.Name(), stagingName), back)
			}
		}

		if previous != "" {
			renameAt(dstDir, previous, dstDir, dstName)
		}

		return result, err
	}

	// What the destination owned moves into the tree about to replace it. A
	// move and not a copy: it is a rename inside one directory, so the saves
	// are never duplicated and never rewritten, however large they have got.
	if previous != "" && len(carry) > 0 {
		old, err = openDirAt(dstDir, previous)
		if err != nil {
			return rollback(err)
		}

		defer old.Close()
	}

	moved, err = carryPrivate(old, staging, carry, owner)
	if err != nil {
		return rollback(err)
	}

	// The root of the tree needs the same treatment as everything inside it,
	// and this is easy to forget because the staging directory already exists
	// by the time the contents are written.
	//
	// Forgotten once, and it broke the feature completely while looking like it
	// worked: the staging directory is created 0700 and owned by the daemon, so
	// every game directory arrived in a seat as root with no permissions for
	// anybody else. The files were there, the sizes were right, the blocks
	// were shared, and the player inside the container could not open a single
	// one of them.
	//
	// Last, after the carry, because this is the moment the member can reach
	// into the tree.
	if err := staging.Chown(owner.UID, owner.GID); err != nil {
		return rollback(err)
	}

	if err := staging.Chmod(os.FileMode(info.Mode).Perm()); err != nil {
		return rollback(err)
	}

	if err := setMtime(staging, info.Mtim); err != nil {
		return rollback(err)
	}

	if err := renameAt(dstDir, stagingName, dstDir, dstName); err != nil {
		return rollback(err)
	}

	keep = true

	if previous != "" {
		removeAllAt(dstDir, previous)
	}

	return result, nil
}

// carryPrivate moves the listed directories from one tree into another.
//
// Used across the swap at the end of a clone, in both directions: forwards to
// keep a seat's saves, and backwards to put them where they were if the swap
// then fails. It answers with what it actually moved, which is what makes the
// backwards call possible and is not the same as what it was asked to move: a
// directory that has since gone is not an error, it is a seat that uninstalled
// something while the daemon was working.
//
// A link anywhere on the way, on either side, is refused. The tree being moved
// into came from the pool, which is to say from whichever seat shared the
// folder, and a drive_c there that is a link to a directory of that seat's
// choosing would otherwise have been where this seat's saves were moved to.
func carryPrivate(from, to *os.File, carry map[string]bool, owner Owner) (map[string]bool, error) {
	moved := map[string]bool{}

	if from == nil || len(carry) == 0 {
		return moved, nil
	}

	names := make([]string, 0, len(carry))
	for name := range carry {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		parent, base := filepath.Split(name)

		source, err := openRel(from, parent)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}

			return moved, err
		}

		if _, err := lstatAt(source, base); err != nil {
			source.Close()

			continue
		}

		// A rename changes the modification time of both directories it
		// touches, and a folder's version is the newest time inside it. The
		// drive_c the saves were moved into had just been given its source's
		// time by the clone, and the move stamped it with the present, so
		// every copy that carried saves measured newer than what it was
		// copied from: the pool took it back from the seat, gave it to the
		// others, took theirs back in turn, and so on after every update to
		// every folder with a prefix in it. So the times of every directory
		// on both sides are taken before the move and put back after it.
		var times stamps

		err = times.keep(source, nil)

		// The parent is normally already there, because it came from the tree
		// being copied. It is not when the game dropped the directory that
		// used to hold the prefix, and then the saves still have to land
		// somewhere rather than being thrown away.
		var target *os.File

		if err == nil {
			target, err = ensureRel(to, parent, owner, from, &times)
		}

		if err == nil {
			err = renameAt(source, base, target, base)
			target.Close()

			if err == nil {
				moved[name] = true
			}
		}

		source.Close()

		if back := times.restore(); err == nil {
			err = back
		}

		if err != nil {
			return moved, err
		}
	}

	return moved, nil
}

// ensureRel opens a relative directory below dir, making what is missing on
// the way with the given owner.
//
// Every directory on the way is added to times with the modification time it
// should have once the caller is done: the one it has now, or for one this
// made, the time of the directory at the same place under like, which is the
// tree the missing part is being recreated from.
func ensureRel(dir *os.File, rel string, owner Owner, like *os.File, times *stamps) (*os.File, error) {
	cur, err := dupDir(dir)
	if err != nil {
		return nil, err
	}

	if err := times.keep(cur, nil); err != nil {
		cur.Close()

		return nil, err
	}

	prefix := ""

	for _, part := range strings.Split(filepath.Clean(rel), "/") {
		if part == "." {
			continue
		}

		prefix = filepath.Join(prefix, part)

		next, created, err := ensureDirAt(cur, part, 0o755)
		if err == nil && created {
			err = next.Chown(owner.UID, owner.GID)
		}

		cur.Close()

		if err == nil {
			var when *unix.Timespec
			if created {
				when = mtimeIn(like, prefix)
			}

			err = times.keep(next, when)
		}

		if err != nil {
			if next != nil {
				next.Close()
			}

			return nil, err
		}

		cur = next
	}

	return cur, nil
}

// mtimeIn is the modification time of a directory below dir, or nil when there
// is none to be had.
func mtimeIn(dir *os.File, rel string) *unix.Timespec {
	f, err := openRel(dir, rel)
	if err != nil {
		return nil
	}

	defer f.Close()

	var st unix.Stat_t

	if err := unix.Fstat(fdOf(f), &st); err != nil {
		return nil
	}

	return &st.Mtim
}

// stamps are directories and the modification times to give them back.
type stamps []stamp

type stamp struct {
	dir   *os.File
	mtime unix.Timespec
}

// keep remembers a directory with the time it has now, or with when if given.
// It holds a handle of its own, so the caller may close the one it passed.
func (s *stamps) keep(dir *os.File, when *unix.Timespec) error {
	var st unix.Stat_t

	if when == nil {
		if err := unix.Fstat(fdOf(dir), &st); err != nil {
			return &os.PathError{Op: "stat", Path: dir.Name(), Err: err}
		}

		when = &st.Mtim
	}

	held, err := dupDir(dir)
	if err != nil {
		return err
	}

	*s = append(*s, stamp{dir: held, mtime: *when})

	return nil
}

// restore gives every directory its time back and lets go of the handles,
// answering with the first thing that failed.
func (s stamps) restore() error {
	var first error

	for i := len(s) - 1; i >= 0; i-- {
		if err := setMtime(s[i].dir, s[i].mtime); err != nil && first == nil {
			first = err
		}

		s[i].dir.Close()
	}

	return first
}

func cloneTree(src, dst *os.File, rel string, owner Owner, carry map[string]bool, result *Result) error {
	names, err := readNames(src)
	if err != nil {
		return err
	}

	for _, name := range names {
		here := filepath.Join(rel, name)

		// Left out because the destination has its own and is about to get it
		// back. Copying it first and overwriting it afterwards would be the
		// same result and several gigabytes of work.
		if carry[here] {
			continue
		}

		st, err := lstatAt(src, name)
		if err != nil {
			return err
		}

		if beforeOpen != nil {
			beforeOpen(filepath.Join(src.Name(), name))
		}

		// What an entry is decides how it is opened, and the open itself
		// refuses a link. So an entry replaced after the lstat above is either
		// refused or is again what it claimed to be; it is never followed.
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			if err := cloneDir(src, dst, name, here, owner, carry, result); err != nil {
				return err
			}

		case unix.S_IFLNK:
			if err := cloneLink(src, dst, name, &st, owner, result); err != nil {
				return err
			}

		case unix.S_IFREG:
			if err := cloneFile(src, dst, name, owner, result); err != nil {
				return err
			}

		default:
			// Sockets, fifos and device nodes. Nothing legitimate in a game
			// directory is one of these, and recreating a device node inside a
			// tree that gets mounted into a container is not something to do by
			// accident.
			return fmt.Errorf("%s is neither a file, a directory nor a symlink",
				filepath.Join(src.Name(), name))
		}
	}

	return nil
}

func cloneDir(src, dst *os.File, name, here string, owner Owner, carry map[string]bool, result *Result) error {
	in, err := openDirAt(src, name)
	if err != nil {
		return err
	}

	defer in.Close()

	// The stat of what was opened, not the one the caller made: the entry
	// may have been replaced by another directory in between, and this is
	// the one being copied.
	var st unix.Stat_t

	if err := unix.Fstat(fdOf(in), &st); err != nil {
		return err
	}

	// Created closed, like the staging directory, and given its real mode on
	// the way out. Setting the mode first and then writing into the directory
	// works as root, but not for a mode without write permission when the
	// tests run this as themselves.
	if err := unix.Mkdirat(fdOf(dst), name, 0o700); err != nil {
		return pathError("mkdir", dst, name, err)
	}

	out, err := openDirAt(dst, name)
	if err != nil {
		return err
	}

	defer out.Close()

	if err := cloneTree(in, out, here, owner, carry, result); err != nil {
		return err
	}

	if err := out.Chown(owner.UID, owner.GID); err != nil {
		return err
	}

	if err := out.Chmod(os.FileMode(st.Mode).Perm()); err != nil {
		return err
	}

	if err := setMtime(out, st.Mtim); err != nil {
		return err
	}

	result.Dirs++

	return nil
}

func cloneLink(src, dst *os.File, name string, st *unix.Stat_t, owner Owner, result *Result) error {
	target, err := readlinkAt(src, name)
	if err != nil {
		return err
	}

	if err := unix.Symlinkat(target, fdOf(dst), name); err != nil {
		return pathError("symlink", dst, name, err)
	}

	if err := unix.Fchownat(fdOf(dst), name, owner.UID, owner.GID, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return pathError("lchown", dst, name, err)
	}

	// The link's own timestamp, for the same reason files and directories get
	// theirs back: measure() stats every entry in the tree, symlinks included,
	// and takes the newest as the folder's version. A fresh link would make
	// every copy newer than the original it was made from, and the pool would
	// carry the two back and forth for as long as it ran.
	//
	// Stamped without following, which would stamp the target instead, and
	// for a link pointing outside the pool the target is not the pool's to
	// touch.
	if err := setMtimeAt(dst, name, st.Mtim); err != nil {
		return err
	}

	result.Symlinks++

	return nil
}

func cloneFile(src, dst *os.File, name string, owner Owner, result *Result) error {
	in, st, err := openFileAt(src, name)
	if err != nil {
		return err
	}

	defer in.Close()

	out, err := createFileAt(dst, name, os.FileMode(st.Mode).Perm())
	if err != nil {
		return err
	}

	defer out.Close()

	err = unix.IoctlFileClone(fdOf(out), fdOf(in))
	switch {
	case err == nil:
		// The whole point.

	case errors.Is(err, syscall.EOPNOTSUPP), errors.Is(err, syscall.EXDEV),
		errors.Is(err, syscall.EINVAL), errors.Is(err, syscall.ENOTTY):
		// EXDEV means the two paths are on different filesystems, EINVAL covers
		// a zero length file, and the other two mean the filesystem has no such
		// ioctl. All of them are recoverable by copying, and the count in the
		// result is what tells the operator the pool stopped saving anything.
		if _, err := io.Copy(out, in); err != nil {
			return err
		}

		result.Copied++

	default:
		return fmt.Errorf("clone %s: %w", in.Name(), err)
	}

	if err := out.Chown(owner.UID, owner.GID); err != nil {
		return err
	}

	// Kept, not left at the moment of copying. Two reasons, and the second is
	// what makes it load bearing rather than tidy: some games and some
	// launchers compare file times to decide whether their data is current, and
	// a folder shared without manifests is versioned by the newest time inside
	// it, so a clone that stamped itself with now would always look newer than
	// the original it came from and be copied back and forth forever.
	if err := setMtime(out, st.Mtim); err != nil {
		return err
	}

	result.Files++
	result.Bytes += st.Size

	return nil
}

// TreeSize returns the space a tree occupies, counting each file once.
func TreeSize(root string) (int64, error) {
	var total int64

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.Type().IsRegular() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			// A file that vanished while Steam was working in the directory is
			// not a reason to fail a size estimate.
			if os.IsNotExist(err) {
				return nil
			}

			return err
		}

		total += info.Size()

		return nil
	})

	if os.IsNotExist(err) {
		return 0, nil
	}

	return total, err
}

// safeName rejects anything that would let a name out of the directory it is
// supposed to stay in.
//
// The names this package handles come out of files Steam writes, which is to
// say out of a game's own metadata. An installdir of "../../etc" is not a
// realistic attack from the store, but the daemon joins these names onto paths
// as root, and a value from a file on disk is not something to hand to
// filepath.Join unchecked.
func safeName(name string) error {
	switch {
	case name == "", name == "." || name == "..":
		return fmt.Errorf("%q is not a usable name", name)

	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("%q contains a path separator", name)

	case strings.HasPrefix(name, "."):
		return fmt.Errorf("%q starts with a dot", name)
	}

	return nil
}
