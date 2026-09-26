package library

import (
	"os"
	"path/filepath"
)

// What belongs to the seat rather than to the pool.
//
// The Steam half of the library already answers this and gets the answer for
// free from Steam's layout: game files are in `common/<game>`, and the Proton
// prefix is in `compatdata/` next to it, so taking only the first leaves the
// second behind. architecture.md says as much, and a test asserts that no
// prefix ever reaches the pool.
//
// The folder half has no such layout to lean on. A folder is the whole unit,
// and the launchers put the wine prefix inside it: a Lutris installer sets the
// prefix to $GAMEDIR, which is the folder itself, so the game files sit in
// `drive_c/Program Files` and the prefix root and the game root are the same
// directory. Skipping the prefix would skip the game.
//
// So the line is drawn one level further in, at `drive_c/users`. That is where
// wine puts Documents, AppData and Saved Games, which is where a Windows game
// puts what the person playing it made. Everything else in the prefix, the
// installed files and the registry that knows about them, is the install and
// is worth sharing.
//
// The cost of drawing it there rather than around the whole prefix: a game
// that keeps data files rather than saves under `drive_c/users` gets them on
// the first delivery and never again. Hence "copied when the seat has none,
// never replaced" rather than "never copied": a seat that has never seen the
// game gets a working starting point, and a seat that has played it keeps what
// it played.

// isWinePrefix reports whether a directory is the root of a wine prefix.
//
// By shape, because there is nothing to ask. A prefix is a `drive_c` with the
// registry beside it, and nothing else in a game folder looks like that.
func isWinePrefix(path string) bool {
	dir, err := openOwn(path)
	if err != nil {
		return false
	}

	defer dir.Close()

	return isWinePrefixAt(dir)
}

// isWinePrefixAt is isWinePrefix for a directory already open, which is how
// the walks below meet one.
func isWinePrefixAt(dir *os.File) bool {
	if !isDirAt(dir, "drive_c") {
		return false
	}

	for _, name := range []string{"system.reg", "user.reg"} {
		if _, err := lstatAt(dir, name); err == nil {
			return true
		}
	}

	return false
}

// seatPrivate reports whether a directory inside a shared folder belongs to
// the seat holding it rather than to the pool.
func seatPrivate(path string) bool {
	drive := filepath.Dir(path)

	prefix, err := openOwn(filepath.Dir(drive))
	if err != nil {
		return false
	}

	defer prefix.Close()

	return seatPrivateAt(prefix, filepath.Base(drive), filepath.Base(path))
}

// seatPrivateAt is seatPrivate for the entry name inside a directory called
// dirName, whose own parent is prefix.
//
// The check is cheap on the way past: only a directory actually called `users`
// inside something actually called `drive_c` costs a look at the filesystem.
func seatPrivateAt(prefix *os.File, dirName, name string) bool {
	if name != "users" || dirName != "drive_c" || prefix == nil {
		return false
	}

	return isWinePrefixAt(prefix)
}

// privateDirsAt lists what the copy at name in parent already owns, relative
// to it.
//
// Used against the destination of a clone, to decide what must survive the
// swap. A tree with no prefix in it answers with nothing and costs one walk.
//
// A destination that is not there yet owns nothing, which is the ordinary case
// for the first delivery and not a failure. Nor does one that is a link: it is
// not followed, so there is nothing behind it to keep.
func privateDirsAt(parent *os.File, name string) map[string]bool {
	found := map[string]bool{}

	root, err := openDirAt(parent, name)
	if err != nil {
		return found
	}

	defer root.Close()

	walk(parent, root, name, "", found)

	return found
}

// walk descends looking for seat private directories, and does not descend
// into one once it has found it: what is inside belongs to the same seat.
//
// up is the directory holding dir, because whether an entry is private depends
// on the directory two levels above it.
func walk(up, dir *os.File, dirName, rel string, found map[string]bool) {
	names, err := readNames(dir)
	if err != nil {
		return
	}

	for _, name := range names {
		if !isDirAt(dir, name) {
			continue
		}

		here := filepath.Join(rel, name)

		if seatPrivateAt(up, dirName, name) {
			found[here] = true

			continue
		}

		sub, err := openDirAt(dir, name)
		if err != nil {
			continue
		}

		walk(dir, sub, name, here, found)
		sub.Close()
	}
}
