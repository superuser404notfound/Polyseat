package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// prefix writes a wine prefix at path, with one save in it.
func prefix(t *testing.T, path, save string) {
	t.Helper()

	mkdirs(t, filepath.Join(path, "drive_c", "users", "steamuser", "Saved Games"))
	write(t, filepath.Join(path, "system.reg"), "registry", 0o644)
	write(t, filepath.Join(path, "user.reg"), "registry", 0o644)
	write(t, filepath.Join(path, "drive_c", "users", "steamuser", "Saved Games", "garden.sav"), save, 0o644)
}

// A prefix is recognised by shape because there is nothing to ask, and the
// shape has to be specific enough that an ordinary game directory is not one.
func TestIsWinePrefixWantsDriveCAndARegistry(t *testing.T) {
	dir := t.TempDir()

	full := filepath.Join(dir, "full")
	prefix(t, full, "save")

	if !isWinePrefix(full) {
		t.Error("a prefix was not recognised")
	}

	// A game that happens to ship a directory called drive_c is not a prefix.
	bare := filepath.Join(dir, "bare")
	mkdirs(t, filepath.Join(bare, "drive_c"))

	if isWinePrefix(bare) {
		t.Error("a drive_c with no registry beside it was taken for a prefix")
	}

	plain := filepath.Join(dir, "plain")
	mkdirs(t, filepath.Join(plain, "bin"))

	if isWinePrefix(plain) {
		t.Error("an ordinary game directory was taken for a prefix")
	}
}

// The line is at drive_c/users and not around the whole prefix, because with a
// Lutris installer the prefix root and the game root are the same directory
// and the game files are inside it.
func TestSeatPrivateIsTheUserDirectoryOnly(t *testing.T) {
	dir := t.TempDir()
	game := filepath.Join(dir, "game")

	prefix(t, game, "save")
	mkdirs(t, filepath.Join(game, "drive_c", "Program Files", "Game"))

	if !seatPrivate(filepath.Join(game, "drive_c", "users")) {
		t.Error("the user directory is not private")
	}

	if seatPrivate(filepath.Join(game, "drive_c", "Program Files")) {
		t.Error("the installed files were treated as private")
	}

	// A directory called users that is not inside a prefix is just a
	// directory, and a game is allowed to have one.
	other := filepath.Join(dir, "other", "drive_c", "users")
	mkdirs(t, other)

	if seatPrivate(other) {
		t.Error("a users directory outside a prefix was treated as private")
	}
}

// The version of a folder is the newest thing in it. If saving counted, every
// evening at a game would be a new version and the pool would copy the whole
// folder over the other seat because somebody played.
func TestMeasureIgnoresWhatTheSeatOwns(t *testing.T) {
	dir := t.TempDir()
	game := filepath.Join(dir, "game")

	prefix(t, game, "save")
	mkdirs(t, filepath.Join(game, "drive_c", "Program Files"))
	write(t, filepath.Join(game, "drive_c", "Program Files", "game.exe"), "the game", 0o644)

	old := time.Now().Add(-48 * time.Hour)

	if err := filepath.Walk(game, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		return os.Chtimes(path, old, old)
	}); err != nil {
		t.Fatal(err)
	}

	before, err := measure(game)
	if err != nil {
		t.Fatal(err)
	}

	// Somebody plays and saves.
	write(t, filepath.Join(game, "drive_c", "users", "steamuser", "Saved Games", "garden.sav"), "a longer save", 0o644)

	after, err := measure(game)
	if err != nil {
		t.Fatal(err)
	}

	if after.Newer(before) {
		t.Errorf("saving made the folder a new version, %v against %v", after.Newest, before.Newest)
	}

	if after.Bytes != before.Bytes {
		t.Errorf("the save counted towards the size, %d against %d", after.Bytes, before.Bytes)
	}
}

// A folder that is nothing but a prefix must not measure as empty, or it would
// lose to every copy of itself and be carried back and forth.
func TestMeasureStillSeesAFolderThatIsItselfAPrefix(t *testing.T) {
	dir := t.TempDir()
	game := filepath.Join(dir, "game")

	prefix(t, game, "save")

	got, err := measure(game)
	if err != nil {
		t.Fatal(err)
	}

	if got.Bytes == 0 {
		t.Error("a folder that is a prefix measured as empty")
	}
}

// The whole point: an update to the game must not take the saves with it.
func TestCloneFolderKeepsTheSeatsSaves(t *testing.T) {
	dir := t.TempDir()
	pool := filepath.Join(dir, "pool")
	seat := filepath.Join(dir, "seat")

	// The pool holds the game with the saves of whoever installed it.
	prefix(t, pool, "the first seat's garden")
	mkdirs(t, filepath.Join(pool, "drive_c", "Program Files"))
	write(t, filepath.Join(pool, "drive_c", "Program Files", "game.exe"), "version one", 0o644)

	// The seat has been playing and has its own.
	prefix(t, seat, "this seat's garden")
	mkdirs(t, filepath.Join(seat, "drive_c", "Program Files"))
	write(t, filepath.Join(seat, "drive_c", "Program Files", "game.exe"), "version one", 0o644)

	// The game is patched in the pool.
	write(t, filepath.Join(pool, "drive_c", "Program Files", "game.exe"), "version two", 0o644)

	if _, err := CloneFolder(pool, seat, Keep); err != nil {
		t.Fatalf("CloneFolder: %v", err)
	}

	save := filepath.Join(seat, "drive_c", "users", "steamuser", "Saved Games", "garden.sav")

	if got := read(t, save); got != "this seat's garden" {
		t.Errorf("the save reads %q, want this seat's own back", got)
	}

	if got := read(t, filepath.Join(seat, "drive_c", "Program Files", "game.exe")); got != "version two" {
		t.Errorf("the update did not arrive, the executable reads %q", got)
	}
}

// A seat that has never seen the game gets the saves that came with it, so
// that a game which keeps data files rather than saves under drive_c/users is
// playable there at all.
func TestCloneFolderGivesANewSeatTheStartingPoint(t *testing.T) {
	dir := t.TempDir()
	pool := filepath.Join(dir, "pool")
	seat := filepath.Join(dir, "seat")

	prefix(t, pool, "what the installer put there")

	if _, err := CloneFolder(pool, seat, Keep); err != nil {
		t.Fatalf("CloneFolder: %v", err)
	}

	save := filepath.Join(seat, "drive_c", "users", "steamuser", "Saved Games", "garden.sav")

	if got := read(t, save); got != "what the installer put there" {
		t.Errorf("a seat with no copy got %q", got)
	}
}

// Clone itself is the Steam path and must not start treating parts of a game
// as private: there the prefix is in compatdata, next to the tree, and what is
// inside the tree is all game.
func TestCloneWithoutFoldersCopiesEverything(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	prefix(t, src, "in the tree")

	if _, err := Clone(src, dst, Keep); err != nil {
		t.Fatalf("Clone: %v", err)
	}

	save := filepath.Join(dst, "drive_c", "users", "steamuser", "Saved Games", "garden.sav")

	if got := read(t, save); got != "in the tree" {
		t.Errorf("Clone left something out, the save reads %q", got)
	}
}

// Two prefixes in one folder, and the second cannot be carried. The first had
// already been moved into the staging tree by then, which the failed clone
// deletes on its way out, and with it the saves.
func TestAFailedCarryPutsBackWhatItMoved(t *testing.T) {
	dir := reflinkDir(t)
	pool := filepath.Join(dir, "pool", "Game")
	seat := filepath.Join(dir, "seat", "Game")

	prefix(t, filepath.Join(pool, "a"), "the pool's a")
	mkdirs(t, filepath.Join(pool, "b"))
	write(t, filepath.Join(pool, "b", "drive_c"), "a file where the seat has a directory", 0o644)

	prefix(t, filepath.Join(seat, "a"), "this seat's a")
	prefix(t, filepath.Join(seat, "b"), "this seat's b")

	if _, err := CloneFolder(pool, seat, Keep); err == nil {
		t.Error("the clone succeeded although b's saves had nowhere to go")
	}

	for _, which := range []string{"a", "b"} {
		save := filepath.Join(seat, which, "drive_c", "users", "steamuser", "Saved Games", "garden.sav")

		data, err := os.ReadFile(save)
		if err != nil {
			t.Errorf("%s's save is gone after the failed update: %v", which, err)

			continue
		}

		if want := "this seat's " + which; string(data) != want {
			t.Errorf("%s's save reads %q, want %q", which, data, want)
		}
	}
}
