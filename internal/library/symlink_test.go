package library

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The attacks in this file are the ones a seat can mount on the daemon from
// inside its container, and every one of them worked before the library stopped
// following links it did not make.
//
// The setting: the daemon runs as root, a seat's library directory belongs to
// the seat's mapped uid and is mounted into the container, and so the player can
// put a symlink anywhere in it, absolute ones included, which the daemon then
// resolves on the host. The host's own Steam library and ~/Games/shared are the
// same situation one step removed: they belong to the desktop user, who is not
// root either.
//
// "The host" here is a directory outside the pool root that no member has any
// business reaching. Each test checks that it came through untouched, and that
// nothing from it reached the pool or another seat.

// hostDir is a directory standing in for the rest of the machine.
func hostDir(t *testing.T) string {
	t.Helper()

	return reflinkDir(t)
}

func symlink(t *testing.T, target, link string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func problemMentions(report Report, what string) bool {
	for _, p := range report.Problems {
		if strings.Contains(p, what) {
			return true
		}
	}

	return false
}

// otherGroup is a supplementary group of the user running the tests, which is
// the one ownership change an unprivileged test can make and then see.
func otherGroup() (int, bool) {
	groups, err := os.Getgroups()
	if err != nil {
		return 0, false
	}

	for _, g := range groups {
		if g != os.Getgid() {
			return g, true
		}
	}

	return 0, false
}

// A seat replaces its steamapps with a link to a host directory. Ensure used to
// create common inside whatever that was and chown it to the seat, which with
// /etc as the target hands the seat the host's configuration.
func TestEnsureDoesNotFollowASeatsLink(t *testing.T) {
	pool := openPool(t)
	host := hostDir(t)

	owner := Keep

	gid, ok := otherGroup()
	if ok {
		owner = Owner{UID: -1, GID: gid}
	}

	mkdirs(t, pool.SeatRoot("seat1"))
	symlink(t, host, pool.SeatApps("seat1"))

	if err := pool.Ensure(Member{Name: "seat1", Owner: owner}); err == nil {
		t.Error("Ensure accepted a steamapps that is a link")
	}

	if _, err := os.Lstat(filepath.Join(host, commonDir)); err == nil {
		t.Error("Ensure created a directory on the host through the seat's link")
	}

	if ok {
		info, err := os.Stat(host)
		if err != nil {
			t.Fatal(err)
		}

		if int(info.Sys().(*syscall.Stat_t).Gid) == gid {
			t.Error("Ensure chowned a host directory through the seat's link")
		}
	}
}

// A seat puts a link where the daemon writes a manifest before renaming it into
// place. Writing through it overwrote any file on the host with manifest text,
// and the chown after it gave that file to the seat.
func TestAManifestIsNotWrittenThroughALink(t *testing.T) {
	pool := openPool(t)
	host := hostDir(t)
	victim := filepath.Join(host, "shadow")

	write(t, victim, "precious", 0o600)

	install(t, pool.SeatApps("seat1"), "1", "Game", "Game", "1000", StateInstalled)
	symlink(t, victim, filepath.Join(pool.SeatApps("seat2"), ManifestName("1")+".polyseat-tmp"))

	if _, err := pool.Sync(updatable("seat1", "seat2"), nil); err != nil {
		t.Fatal(err)
	}

	if got := read(t, victim); got != "precious" {
		t.Errorf("the host file was overwritten through the seat's link, it now reads %q", got)
	}
}

// A seat makes its steamapps a link to a library on the host that it cannot
// read. The pool used to harvest what it found there and hand it to every
// other seat.
func TestHarvestDoesNotReadThroughALinkedLibrary(t *testing.T) {
	pool := openPool(t)
	host := hostDir(t)

	install(t, host, "2", "Secret", "Secret", "1000", StateInstalled)

	mkdirs(t, pool.SeatRoot("seat1"))
	symlink(t, host, pool.SeatApps("seat1"))

	report, err := pool.Sync(members("seat1", "seat2"), nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(filepath.Join(pool.PoolApps(), commonDir, "Secret")); err == nil {
		t.Error("a host directory reached through a seat's link was taken into the pool")
	}

	if _, err := os.Lstat(filepath.Join(pool.SeatApps("seat2"), commonDir, "Secret")); err == nil {
		t.Error("a host directory reached through a seat's link was given to another seat")
	}

	if !problemMentions(report, "seat1") {
		t.Errorf("the refusal was not reported: %+v", report.Problems)
	}
}

// The same with the launcher agnostic side: shared/ made a link to the host.
func TestHarvestDoesNotReadThroughALinkedFolderDirectory(t *testing.T) {
	pool := openPool(t)
	host := hostDir(t)

	putFolder(t, host, "Secrets", "the host's")
	age(t, host, time.Hour)

	mkdirs(t, pool.SeatRoot("seat1"))
	symlink(t, host, pool.SeatFolders("seat1"))

	if _, err := pool.Sync(members("seat1", "seat2"), nil); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(filepath.Join(pool.PoolFolders(), "Secrets")); err == nil {
		t.Error("a host directory reached through a seat's link was taken into the pool")
	}
}

// A seat makes steamapps/common a link to the host and waits for a delivery.
// The clone used to move whatever the host had under the game's name aside and
// delete it, and put the game there in its place.
func TestDeliveryDoesNotReplaceThroughALink(t *testing.T) {
	pool := openPool(t)
	host := hostDir(t)

	mkdirs(t, filepath.Join(host, "Game"))
	write(t, filepath.Join(host, "Game", "precious"), "the host's", 0o644)

	install(t, pool.SeatApps("seat1"), "3", "Game", "Game", "1000", StateInstalled)

	mkdirs(t, pool.SeatApps("seat2"))
	symlink(t, host, filepath.Join(pool.SeatApps("seat2"), commonDir))

	if _, err := pool.Sync(updatable("seat1", "seat2"), nil); err != nil {
		t.Fatal(err)
	}

	if got := read(t, filepath.Join(host, "Game", "precious")); got != "the host's" {
		t.Errorf("the host directory was replaced through the seat's link, it reads %q", got)
	}

	if _, err := os.Lstat(filepath.Join(host, "Game", "game.bin")); err == nil {
		t.Error("the game was written into the host through the seat's link")
	}
}

// A manifest that is a link to a file on the host. Before, it was read, taken
// into the pool, and copied into every other seat.
func TestAManifestLinkIsNotRead(t *testing.T) {
	pool := openPool(t)
	host := hostDir(t)

	install(t, host, "4", "Secret", "Secret", "1000", StateInstalled)

	mkdirs(t, filepath.Join(pool.SeatApps("seat1"), commonDir, "Secret"))
	symlink(t, filepath.Join(host, ManifestName("4")), filepath.Join(pool.SeatApps("seat1"), ManifestName("4")))

	if _, err := pool.Sync(members("seat1", "seat2"), nil); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(filepath.Join(pool.PoolApps(), ManifestName("4"))); err == nil {
		t.Error("a manifest read through a seat's link reached the pool")
	}
}

// A manifest that is a fifo. Reading it used to block the whole daemon's sync
// loop forever, for every seat, until somebody wrote into the pipe.
func TestAManifestFifoDoesNotHang(t *testing.T) {
	pool := openPool(t)

	mkdirs(t, pool.SeatApps("seat1"))

	if err := syscall.Mkfifo(filepath.Join(pool.SeatApps("seat1"), ManifestName("5")), 0o644); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)

	go func() {
		_, err := pool.Sync(members("seat1"), nil)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}

	case <-time.After(5 * time.Second):
		// Unblock the reader so the test can end at all.
		if f, err := os.OpenFile(filepath.Join(pool.SeatApps("seat1"), ManifestName("5")), os.O_WRONLY, 0); err == nil {
			f.Close()
		}

		t.Fatal("a fifo named like a manifest hung the sync pass")
	}
}

// The host's library is somebody's home, and the person whose home it is can
// put a link there as well. Following it would let them read or write as root.
func TestAnExternalLibraryIsNotReachedThroughTheOwnersLink(t *testing.T) {
	pool := openPool(t)
	host := hostDir(t)
	home := reflinkDir(t)

	putFolder(t, host, "Secrets", "the host's")
	age(t, host, time.Hour)

	mkdirs(t, filepath.Join(home, "Steam", steamApps, commonDir), filepath.Join(home, "Games"))
	symlink(t, host, filepath.Join(home, "Games", sharedDir))

	desktop := Member{
		Name:    "host",
		Apps:    filepath.Join(home, "Steam", steamApps),
		Folders: filepath.Join(home, "Games", sharedDir),
		Owner:   Keep,
	}

	report, err := pool.Sync([]Member{desktop}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(filepath.Join(pool.PoolFolders(), "Secrets")); err == nil {
		t.Error("a host directory reached through the desktop user's link was taken into the pool")
	}

	if !problemMentions(report, "host") {
		t.Errorf("the refusal was not reported: %+v", report.Problems)
	}
}

// The other half of the rule for an external member's path: a link that only
// root could have made is followed. Without it a machine whose /home is a link
// to /var/home would have no host library at all.
func TestAnchoredFollowsWhatOnlyRootCouldHaveMade(t *testing.T) {
	info, err := os.Lstat("/lib")
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Skip("this system has no /lib link to follow")
	}

	want, err := os.Stat("/lib")
	if err != nil {
		t.Fatal(err)
	}

	dir, err := openAnchored("/lib")
	if err != nil {
		t.Fatalf("a link in / was not followed: %v", err)
	}

	defer dir.Close()

	got, err := dir.Stat()
	if err != nil {
		t.Fatal(err)
	}

	if !os.SameFile(got, want) {
		t.Error("the link in / led somewhere other than where it points")
	}
}

// A shared folder whose drive_c is a link, arriving from the pool into a seat
// that has its own saves under drive_c/users. The saves used to be moved through
// the link, into whatever directory the seat that shared the folder chose.
func TestSavesAreNotCarriedThroughALink(t *testing.T) {
	dir := reflinkDir(t)
	thief := hostDir(t)
	pool := filepath.Join(dir, "pool", "Game")
	seat := filepath.Join(dir, "seat", "Game")

	mkdirs(t, pool)
	write(t, filepath.Join(pool, "system.reg"), "registry", 0o644)
	symlink(t, thief, filepath.Join(pool, "drive_c"))

	prefix(t, seat, "this seat's garden")

	if _, err := CloneFolder(pool, seat, Keep); err == nil {
		t.Error("the clone went ahead with a linked drive_c")
	}

	if _, err := os.Lstat(filepath.Join(thief, "users")); err == nil {
		t.Error("the seat's saves were moved through the link")
	}

	save := filepath.Join(seat, "drive_c", "users", "steamuser", "Saved Games", "garden.sav")

	if got := read(t, save); got != "this seat's garden" {
		t.Errorf("the save reads %q after the refused update", got)
	}
}

// A directory swapped for a link between the moment the clone looks at it and
// the moment it goes in. The old walk asked what an entry was and then opened it
// by path, so the answer could be out of date by the time it was used.
func TestCloneDoesNotFollowADirectorySwappedForALink(t *testing.T) {
	dir := reflinkDir(t)
	host := hostDir(t)
	src := filepath.Join(dir, "src")

	write(t, filepath.Join(host, "secret"), "the host's", 0o600)
	mkdirs(t, filepath.Join(src, "sub"))

	beforeOpen = func(path string) {
		if filepath.Base(path) != "sub" {
			return
		}

		if err := os.Rename(path, path+".gone"); err != nil {
			t.Error(err)
		}

		if err := os.Symlink(host, path); err != nil {
			t.Error(err)
		}
	}

	defer func() { beforeOpen = nil }()

	dst := filepath.Join(dir, "dst")

	if _, err := Clone(src, dst, Keep); err == nil {
		t.Error("the clone went ahead through a directory swapped for a link")
	}

	if _, err := os.Lstat(filepath.Join(dst, "sub", "secret")); err == nil {
		t.Error("a host file was copied through a link swapped in during the clone")
	}
}
