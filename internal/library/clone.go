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
	"strings"
	"syscall"
	"time"

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
func Clone(src, dst string, owner Owner) (Result, error) {
	var result Result

	info, err := os.Lstat(src)
	if err != nil {
		return result, err
	}

	if !info.IsDir() {
		return result, fmt.Errorf("%s is not a directory", src)
	}

	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return result, err
	}

	staging, err := os.MkdirTemp(parent, ".polyseat-clone-")
	if err != nil {
		return result, err
	}

	// The staging directory is removed on every path out of here. On success
	// it has already been renamed away and this is a cheap no-op; on failure it
	// is what keeps a failed clone from occupying space forever.
	defer os.RemoveAll(staging)

	if err := cloneTree(src, staging, owner, &result); err != nil {
		return result, err
	}

	// The root of the tree needs the same treatment as everything inside it,
	// and this is easy to forget because the staging directory already exists
	// by the time the contents are written.
	//
	// Forgotten once, and it broke the feature completely while looking like it
	// worked: MkdirTemp creates 0700 owned by the daemon, so every game
	// directory arrived in a seat as root with no permissions for anybody else.
	// The files were there, the sizes were right, the blocks were shared, and
	// the player inside the container could not open a single one of them.
	if err := os.Lchown(staging, owner.UID, owner.GID); err != nil {
		return result, err
	}

	if err := os.Chmod(staging, info.Mode().Perm()); err != nil {
		return result, err
	}

	if err := os.Chtimes(staging, time.Time{}, info.ModTime()); err != nil {
		return result, err
	}

	// Whatever was there before is moved aside rather than deleted first, so a
	// failure between the two renames leaves the old copy recoverable instead
	// of leaving the seat with nothing.
	previous := ""

	if _, err := os.Lstat(dst); err == nil {
		previous = dst + ".polyseat-old"

		if err := os.RemoveAll(previous); err != nil {
			return result, err
		}

		if err := os.Rename(dst, previous); err != nil {
			return result, err
		}
	}

	if err := os.Rename(staging, dst); err != nil {
		if previous != "" {
			os.Rename(previous, dst)
		}

		return result, err
	}

	if previous != "" {
		os.RemoveAll(previous)
	}

	return result, nil
}

func cloneTree(src, dst string, owner Owner, result *Result) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		info, err := entry.Info()
		if err != nil {
			return err
		}

		switch {
		case info.IsDir():
			if err := os.Mkdir(dstPath, info.Mode().Perm()); err != nil {
				return err
			}

			if err := cloneTree(srcPath, dstPath, owner, result); err != nil {
				return err
			}

			// Ownership and permissions are applied on the way out, after the
			// contents are in. Setting them first and then writing into the
			// directory as root would work, but a mode without write permission
			// would not.
			if err := os.Lchown(dstPath, owner.UID, owner.GID); err != nil {
				return err
			}

			if err := os.Chmod(dstPath, info.Mode().Perm()); err != nil {
				return err
			}

			if err := os.Chtimes(dstPath, time.Time{}, info.ModTime()); err != nil {
				return err
			}

			result.Dirs++

		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(srcPath)
			if err != nil {
				return err
			}

			if err := os.Symlink(target, dstPath); err != nil {
				return err
			}

			if err := os.Lchown(dstPath, owner.UID, owner.GID); err != nil {
				return err
			}

			result.Symlinks++

		case info.Mode().IsRegular():
			if err := cloneFile(srcPath, dstPath, info, owner, result); err != nil {
				return err
			}

		default:
			// Sockets, fifos and device nodes. Nothing legitimate in a game
			// directory is one of these, and recreating a device node inside a
			// tree that gets mounted into a container is not something to do by
			// accident.
			return fmt.Errorf("%s is neither a file, a directory nor a symlink", srcPath)
		}
	}

	return nil
}

func cloneFile(srcPath, dstPath string, info os.FileInfo, owner Owner, result *Result) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}

	defer src.Close()

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}

	defer dst.Close()

	err = unix.IoctlFileClone(int(dst.Fd()), int(src.Fd()))
	switch {
	case err == nil:
		// The whole point.

	case errors.Is(err, syscall.EOPNOTSUPP), errors.Is(err, syscall.EXDEV),
		errors.Is(err, syscall.EINVAL), errors.Is(err, syscall.ENOTTY):
		// EXDEV means the two paths are on different filesystems, EINVAL covers
		// a zero length file, and the other two mean the filesystem has no such
		// ioctl. All of them are recoverable by copying, and the count in the
		// result is what tells the operator the pool stopped saving anything.
		if _, err := io.Copy(dst, src); err != nil {
			return err
		}

		result.Copied++

	default:
		return fmt.Errorf("clone %s: %w", srcPath, err)
	}

	if err := dst.Chown(owner.UID, owner.GID); err != nil {
		return err
	}

	// Kept, not left at the moment of copying. Two reasons, and the second is
	// what makes it load bearing rather than tidy: some games and some
	// launchers compare file times to decide whether their data is current, and
	// a folder shared without manifests is versioned by the newest time inside
	// it, so a clone that stamped itself with now would always look newer than
	// the original it came from and be copied back and forth forever.
	if err := os.Chtimes(dstPath, time.Time{}, info.ModTime()); err != nil {
		return err
	}

	result.Files++
	result.Bytes += info.Size()

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
