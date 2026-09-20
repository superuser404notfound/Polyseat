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
	info, err := os.Lstat(filepath.Join(path, "drive_c"))
	if err != nil || !info.IsDir() {
		return false
	}

	for _, name := range []string{"system.reg", "user.reg"} {
		if _, err := os.Lstat(filepath.Join(path, name)); err == nil {
			return true
		}
	}

	return false
}

// seatPrivate reports whether a directory inside a shared folder belongs to
// the seat holding it rather than to the pool.
//
// The check is cheap on the way past: only a directory actually called `users`
// inside something actually called `drive_c` costs a look at the filesystem.
func seatPrivate(path string) bool {
	if filepath.Base(path) != "users" {
		return false
	}

	drive := filepath.Dir(path)
	if filepath.Base(drive) != "drive_c" {
		return false
	}

	return isWinePrefix(filepath.Dir(drive))
}

// privateDirs lists what the copy at root already owns, relative to it.
//
// Used against the destination of a clone, to decide what must survive the
// swap. A tree with no prefix in it answers with nothing and costs one walk.
func privateDirs(root string) map[string]bool {
	found := map[string]bool{}

	// A destination that is not there yet owns nothing, which is the ordinary
	// case for the first delivery and not a failure.
	if _, err := os.Lstat(root); err != nil {
		return found
	}

	walk(root, "", found)

	return found
}

// walk descends looking for seat private directories, and does not descend
// into one once it has found it: what is inside belongs to the same seat.
func walk(dir, rel string, found map[string]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		here := filepath.Join(rel, entry.Name())

		if seatPrivate(path) {
			found[here] = true

			continue
		}

		walk(path, here, found)
	}
}
