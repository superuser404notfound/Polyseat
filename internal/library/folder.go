package library

import (
	"os"
	"path/filepath"
	"sort"
	"time"
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
func ScanFolders(dir string) ([]Folder, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, err
	}

	var out []Folder

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		if err := safeName(entry.Name()); err != nil {
			// Skipped rather than refused. This directory is written by whoever
			// uses the seat, so an odd name in it is not a reason to stop
			// sharing everything else.
			continue
		}

		folder, err := measure(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}

		folder.Name = entry.Name()
		out = append(out, folder)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out, nil
}

// FolderAt measures one named folder under dir.
func FolderAt(dir, name string) (Folder, error) {
	if err := safeName(name); err != nil {
		return Folder{}, err
	}

	folder, err := measure(filepath.Join(dir, name))
	if err != nil {
		return Folder{}, err
	}

	folder.Name = name

	return folder, nil
}

// measure walks a tree for its size and its newest modification time.
//
// A full walk, which is the cost of having no manifest to read. It is stat only
// and the kernel keeps the directory entries cached, so on a warm filesystem a
// large game costs a fraction of a second; the pool still only does it for
// folders whose recorded version it needs to check.
func measure(root string) (Folder, error) {
	var folder Folder

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// A file that vanished under the walk is normal while a launcher is
			// still writing, and it does not make the rest of the tree
			// unreadable.
			if os.IsNotExist(err) {
				return nil
			}

			return err
		}

		info, err := d.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}

			return err
		}

		if info.ModTime().After(folder.Newest) {
			folder.Newest = info.ModTime()
		}

		if d.Type().IsRegular() {
			folder.Bytes += info.Size()
		}

		return nil
	})

	return folder, err
}
