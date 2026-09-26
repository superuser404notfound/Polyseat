package library

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sys/unix"
)

// Folders are how everything that is not Steam is shared.
//
// Steam hands the pool a completion signal and a version number: StateFlags
// says an install is finished and buildid says which one it is. No other
// launcher offers anything comparable, and there is no format they agree on, so
// inventing a manifest for them would be inventing a standard nobody writes to.
//
// What every launcher does produce is a directory. So a seat has a second
// place, shared/, where a plain folder is the whole unit: put a game there and
// it reaches the other seats, without the daemon needing to know which launcher
// made it or what is inside.
//
// The two signals Steam gives are replaced by the same two facts read off the
// tree. Finished is "nothing in it has changed for a while", which is honest
// but weaker: a download that stalls for longer than that window can be taken
// half complete, and there is no way to tell from the outside. Version is the
// newest modification time inside the tree, which is why cloning preserves file
// times; without that a copy would always look newer than its original.

// Folder is one shared game directory.
type Folder struct {
	Name string `json:"name"`

	// Bytes is the apparent size, and Newest the latest modification time
	// anywhere inside. Together they stand in for a version.
	Bytes  int64     `json:"bytes"`
	Newest time.Time `json:"newest"`
}

// Newer reports whether f is a later version than other.
//
// Time first, size as the tie breaker. Two trees written in the same second
// happen when a game is unpacked quickly, and then the larger one is the one
// that finished.
func (f Folder) Newer(other Folder) bool {
	if f.Newest.After(other.Newest) {
		return true
	}

	if f.Newest.Equal(other.Newest) {
		return f.Bytes > other.Bytes
	}

	return false
}

// Settled reports whether the folder has been quiet long enough to copy.
func (f Folder) Settled(quiet time.Duration) bool {
	return time.Since(f.Newest) >= quiet
}

// ScanFolders lists the game folders directly under dir.
//
// One level only. A folder is a game; what is inside it is the launcher's
// business and none of the pool's.
//
// The path form, trusting every directory on the way; the pool opens a
// member's directory itself and calls scanFoldersAt.
func ScanFolders(dir string) ([]Folder, error) {
	d, err := openOwn(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, err
	}

	defer d.Close()

	return scanFoldersAt(d)
}

func scanFoldersAt(dir *os.File) ([]Folder, error) {
	names, err := readNames(dir)
	if err != nil {
		return nil, err
	}

	var out []Folder

	for _, name := range names {
		if !isDirAt(dir, name) {
			continue
		}

		if err := safeName(name); err != nil {
			// Skipped rather than refused. This directory is written by whoever
			// uses the seat, so an odd name in it is not a reason to stop
			// sharing everything else.
			continue
		}

		folder, err := measureAt(dir, name)
		if err != nil {
			continue
		}

		folder.Name = name
		out = append(out, folder)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out, nil
}

// FolderAt measures one named folder under dir.
func FolderAt(dir, name string) (Folder, error) {
	d, err := openOwn(dir)
	if err != nil {
		return Folder{}, err
	}

	defer d.Close()

	return folderAt(d, name)
}

func folderAt(dir *os.File, name string) (Folder, error) {
	if err := safeName(name); err != nil {
		return Folder{}, err
	}

	folder, err := measureAt(dir, name)
	if err != nil {
		return Folder{}, err
	}

	folder.Name = name

	return folder, nil
}

// measure is measureAt for a path, trusting the directories leading to it.
func measure(root string) (Folder, error) {
	parent, err := openOwn(filepath.Dir(root))
	if err != nil {
		return Folder{}, err
	}

	defer parent.Close()

	return measureAt(parent, filepath.Base(root))
}

// measureAt walks a tree for its size and its newest modification time.
//
// A full walk, which is the cost of having no manifest to read. It is stat only
// and the kernel keeps the directory entries cached, so on a warm filesystem a
// large game costs a fraction of a second; the pool still only does it for
// folders whose recorded version it needs to check.
//
// What the seat owns is left out of both numbers, and the timestamp is the
// reason. A version here is "the newest thing inside", so counting a wine
// prefix's user directory would make every evening at the game a new version
// of it: the folder would be taken into the pool again and copied over the
// other seat, several gigabytes at a time, because somebody saved.
//
// Never the root itself, which is only ever looked at as the tree it is: a
// folder that is nothing but a prefix would otherwise measure as empty and lose
// to every copy of itself.
func measureAt(parent *os.File, name string) (Folder, error) {
	var folder Folder

	root, err := openDirAt(parent, name)
	if err != nil {
		return folder, err
	}

	defer root.Close()

	var st unix.Stat_t

	if err := unix.Fstat(fdOf(root), &st); err != nil {
		return folder, err
	}

	folder.Newest = mtimeOf(&st)

	err = measureTree(parent, root, name, &folder)

	return folder, err
}

func measureTree(up, dir *os.File, dirName string, folder *Folder) error {
	names, err := readNames(dir)
	if err != nil {
		return err
	}

	for _, name := range names {
		st, err := lstatAt(dir, name)
		if err != nil {
			// A file that vanished under the walk is normal while a launcher is
			// still writing, and it does not make the rest of the tree
			// unreadable.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}

			return err
		}

		isDir := st.Mode&unix.S_IFMT == unix.S_IFDIR

		if isDir && seatPrivateAt(up, dirName, name) {
			continue
		}

		if when := mtimeOf(&st); when.After(folder.Newest) {
			folder.Newest = when
		}

		if st.Mode&unix.S_IFMT == unix.S_IFREG {
			folder.Bytes += st.Size
		}

		if !isDir {
			continue
		}

		sub, err := openDirAt(dir, name)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}

			return err
		}

		err = measureTree(dir, sub, name, folder)
		sub.Close()

		if err != nil {
			return err
		}
	}

	return nil
}
