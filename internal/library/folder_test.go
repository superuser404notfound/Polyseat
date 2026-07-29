package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// putFolder writes a launcher agnostic game folder: just a directory with
// files in it, which is all any launcher other than Steam leaves behind.
func putFolder(t *testing.T, dir, name, content string) {
	t.Helper()

	mkdirs(t, filepath.Join(dir, name, "bin"))
	write(t, filepath.Join(dir, name, "bin", "game"), content, 0o755)
	write(t, filepath.Join(dir, name, "data.pak"), content+" data", 0o644)
}

// age moves a tree's modification times into the past, which is how a test
// says "this finished a while ago" without waiting.
func age(t *testing.T, root string, d time.Duration) {
	t.Helper()

	when := time.Now().Add(-d)

	err := filepath.WalkDir(root, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		return os.Chtimes(path, when, when)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestScanFolders(t *testing.T) {
	dir := reflinkDir(t)

	putFolder(t, dir, "Hollow Knight", "hk")
	putFolder(t, dir, "Cyberpunk 2077", "cp")

	// Not a game: a loose file, and a name that would escape the directory if
	// it were ever joined onto a path.
	write(t, filepath.Join(dir, "readme.txt"), "hello", 0o644)
	mkdirs(t, filepath.Join(dir, ".hidden"))

	folders, err := ScanFolders(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(folders) != 2 {
		t.Fatalf("found %d folders, want 2: %+v", len(folders), folders)
	}

	// Sorted, so the interface does not reshuffle between refreshes.
	if folders[0].Name != "Cyberpunk 2077" || folders[1].Name != "Hollow Knight" {
		t.Errorf("not sorted: %+v", folders)
	}

	if folders[0].Bytes == 0 {
		t.Error("the folder measured as empty")
	}

	if folders[0].Newest.IsZero() {
		t.Error("the folder has no modification time")
	}

	// A missing directory is nothing rather than an error, because a seat that
	// has never had a shared folder has no such directory.
	if got, err := ScanFolders(filepath.Join(dir, "nope")); err != nil || len(got) != 0 {
		t.Errorf("scanning a missing directory gave %v, %v", got, err)
	}
}

func TestFolderNewer(t *testing.T) {
	now := time.Now()

	older := Folder{Newest: now.Add(-time.Hour), Bytes: 100}
	newer := Folder{Newest: now, Bytes: 100}

	if !newer.Newer(older) {
		t.Error("a later tree did not compare as newer")
	}

	if older.Newer(newer) {
		t.Error("an earlier tree compared as newer")
	}

	if newer.Newer(newer) {
		t.Error("a tree compared as newer than itself")
	}

	// Same second, different size: a game unpacks fast enough for this to
	// happen, and then the larger one is the one that finished.
	small := Folder{Newest: now, Bytes: 100}
	big := Folder{Newest: now, Bytes: 200}

	if !big.Newer(small) {
		t.Error("with equal times the larger tree did not win")
	}

	if small.Newer(big) {
		t.Error("with equal times the smaller tree won")
	}
}

func TestSyncSharesAFolderBetweenSeats(t *testing.T) {
	pool := openPool(t)
	pool.Settle = time.Minute

	seats := updatable("seat1", "seat2")

	if _, err := pool.Sync(seats, nil); err != nil {
		t.Fatal(err)
	}

	putFolder(t, pool.SeatFolders("seat1"), "Hollow Knight", "hk")

	// Still being written, as far as the pool can tell.
	report, err := pool.Sync(seats, nil)
	if err != nil {
		t.Fatal(err)
	}

	if !report.Empty() {
		t.Fatalf("a folder that had just changed was taken straight away: %+v", report)
	}

	// A launcher has no equivalent of Steam's StateFlags, so quiet for a while
	// is the only completion signal there is.
	age(t, filepath.Join(pool.SeatFolders("seat1"), "Hollow Knight"), 2*time.Minute)

	report, err = pool.Sync(seats, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Harvested) != 1 || report.Harvested[0].Name != "Hollow Knight" {
		t.Fatalf("harvested %+v", report.Harvested)
	}

	if len(report.Delivered) != 1 || report.Delivered[0].Seat != "seat2" {
		t.Fatalf("delivered %+v", report.Delivered)
	}

	if got := read(t, filepath.Join(pool.SeatFolders("seat2"), "Hollow Knight", "bin", "game")); got != "hk" {
		t.Errorf("seat2's copy reads %q", got)
	}

	// The executable bit has to survive, or the game is present and unplayable.
	info, err := os.Stat(filepath.Join(pool.SeatFolders("seat2"), "Hollow Knight", "bin", "game"))
	if err != nil {
		t.Fatal(err)
	}

	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("the executable arrived as %o, want 755", perm)
	}

	// Idle afterwards. Because a folder is versioned by its newest file time,
	// a clone that stamped itself with the moment of copying would look newer
	// than its own source and be carried back and forth forever.
	for i := range 3 {
		report, err := pool.Sync(seats, nil)
		if err != nil {
			t.Fatal(err)
		}

		if !report.Empty() {
			t.Fatalf("pass %d was not idle, so the copies are chasing each other: %+v", i, report)
		}
	}
}

func TestSyncUpdatesAFolder(t *testing.T) {
	pool := openPool(t)
	pool.Settle = time.Minute

	putFolder(t, pool.SeatFolders("seat1"), "Hollow Knight", "old")
	age(t, filepath.Join(pool.SeatFolders("seat1"), "Hollow Knight"), 2*time.Minute)

	if _, err := pool.Sync(updatable("seat1", "seat2"), nil); err != nil {
		t.Fatal(err)
	}

	// seat1's launcher patches the game.
	putFolder(t, pool.SeatFolders("seat1"), "Hollow Knight", "patched")
	age(t, filepath.Join(pool.SeatFolders("seat1"), "Hollow Knight"), 90*time.Second)

	// seat2 is busy, so the update waits rather than being written under
	// whoever is playing.
	report, err := pool.Sync(members("seat1", "seat2"), nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Pending) != 1 || report.Pending[0].Seat != "seat2" {
		t.Fatalf("the waiting folder update was not reported: %+v", report)
	}

	if got := read(t, filepath.Join(pool.SeatFolders("seat2"), "Hollow Knight", "bin", "game")); got != "old" {
		t.Errorf("a busy seat was updated anyway, it reads %q", got)
	}

	// Free now.
	if _, err := pool.Sync(updatable("seat1", "seat2"), nil); err != nil {
		t.Fatal(err)
	}

	if got := read(t, filepath.Join(pool.SeatFolders("seat2"), "Hollow Knight", "bin", "game")); got != "patched" {
		t.Errorf("the update did not arrive, it reads %q", got)
	}
}

func TestSyncDoesNotRestoreARemovedFolder(t *testing.T) {
	pool := openPool(t)
	pool.Settle = time.Minute

	seats := updatable("seat1", "seat2")

	putFolder(t, pool.SeatFolders("seat1"), "Hollow Knight", "hk")
	age(t, filepath.Join(pool.SeatFolders("seat1"), "Hollow Knight"), 2*time.Minute)

	if _, err := pool.Sync(seats, nil); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(filepath.Join(pool.SeatFolders("seat2"), "Hollow Knight")); err != nil {
		t.Fatal(err)
	}

	report, err := pool.Sync(seats, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Declined) != 1 || report.Declined[0].Seat != "seat2" {
		t.Fatalf("the removal was not noticed: %+v", report)
	}

	for i := range 3 {
		report, err := pool.Sync(seats, nil)
		if err != nil {
			t.Fatal(err)
		}

		if !report.Empty() {
			t.Fatalf("pass %d put the folder back: %+v", i, report)
		}
	}

	// Offering it clears the refusal, the same way it does for a Steam title.
	if err := pool.Offer("seat2", folderKey("Hollow Knight")); err != nil {
		t.Fatal(err)
	}

	report, err = pool.Sync(seats, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Delivered) != 1 {
		t.Errorf("Offer did not bring the folder back: %+v", report)
	}
}

func TestFoldersAndSteamTitlesShareOneInventory(t *testing.T) {
	pool := openPool(t)
	pool.Settle = 0

	seats := updatable("seat1", "seat2")

	install(t, pool.SeatApps("seat1"), "440", "Team Fortress 2", "Team Fortress 2", "1000", StateInstalled)
	putFolder(t, pool.SeatFolders("seat1"), "Hollow Knight", "hk")

	if _, err := pool.Sync(seats, nil); err != nil {
		t.Fatal(err)
	}

	inv, err := pool.Inventory(seats)
	if err != nil {
		t.Fatal(err)
	}

	if len(inv.Titles) != 2 {
		t.Fatalf("the inventory holds %d entries, want 2: %+v", len(inv.Titles), inv.Titles)
	}

	kinds := map[string]Title{}
	for _, title := range inv.Titles {
		kinds[title.Kind] = title
	}

	if _, ok := kinds["steam"]; !ok {
		t.Error("no Steam title in the inventory")
	}

	folder, ok := kinds["folder"]
	if !ok {
		t.Fatal("no shared folder in the inventory")
	}

	if folder.Name != "Hollow Knight" {
		t.Errorf("the folder is called %q", folder.Name)
	}

	if len(folder.In) != 2 {
		t.Errorf("the folder reports itself in %v, want both seats", folder.In)
	}

	// Removing a folder from the pool uses the same call as a Steam title and
	// leaves the seats that have it alone.
	if err := pool.Remove(folder.AppID); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := os.Stat(filepath.Join(pool.PoolFolders(), "Hollow Knight")); err == nil {
		t.Error("the folder is still in the pool")
	}

	for _, seat := range []string{"seat1", "seat2"} {
		if _, err := os.Stat(filepath.Join(pool.SeatFolders(seat), "Hollow Knight")); err != nil {
			t.Errorf("%s lost its copy when the pool entry was removed", seat)
		}
	}
}
