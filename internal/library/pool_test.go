package library

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openPool returns a pool on a filesystem that can clone, with the settling
// delay switched off so tests reach the interesting code without waiting.
func openPool(t *testing.T) *Pool {
	t.Helper()

	pool, err := Open(reflinkDir(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	pool.Settle = 0

	return pool
}

// install writes a game into a steamapps directory the way Steam would: a
// manifest and a directory under common.
func install(t *testing.T, steamapps, appID, name, dir, buildID string, state int) {
	t.Helper()

	if err := os.MkdirAll(filepath.Join(steamapps, commonDir, dir), 0o755); err != nil {
		t.Fatal(err)
	}

	write(t, filepath.Join(steamapps, commonDir, dir, "game.bin"), "content of "+name, 0o644)
	write(t, filepath.Join(steamapps, commonDir, dir, "save-me.cfg"), "settings", 0o644)

	// Modelled on the real fixture, including the fields the daemon does not
	// parse, because those are the ones that have to survive being copied.
	manifest := fmt.Sprintf(`"AppState"
{
	"appid"		"%s"
	"Universe"		"1"
	"name"		"%s"
	"StateFlags"		"%d"
	"installdir"		"%s"
	"LastUpdated"		"1785198869"
	"LastPlayed"		"1785199000"
	"SizeOnDisk"		"1000"
	"buildid"		"%s"
	"LastOwner"		"76561197960287930"
	"InstalledDepots"
	{
		"%s1"
		{
			"manifest"		"2406473492863353429"
			"size"		"1449233897"
		}
	}
	"UserConfig"
	{
	}
}
`, appID, name, state, dir, buildID, appID)

	write(t, filepath.Join(steamapps, ManifestName(appID)), manifest, 0o644)
}

// members builds seats that may not be updated, which is the cautious default
// and matches a seat with somebody sitting at it.
func members(names ...string) []Member {
	out := make([]Member, 0, len(names))
	for _, name := range names {
		out = append(out, Member{Name: name, Owner: Keep})
	}

	return out
}

// updatable builds seats whose files may be replaced, which is what the manager
// reports for a seat that is stopped or has no Steam running in it.
func updatable(names ...string) []Member {
	out := members(names...)
	for i := range out {
		out[i].Updatable = true
	}

	return out
}

func TestSyncSharesAnInstallBetweenSeats(t *testing.T) {
	pool := openPool(t)
	seats := members("seat1", "seat2")

	if _, err := pool.Sync(seats, nil); err != nil {
		t.Fatalf("Sync on an empty library: %v", err)
	}

	// seat1 installs something.
	install(t, pool.SeatApps("seat1"), "440", "Team Fortress 2", "Team Fortress 2", "1000", StateInstalled)

	report, err := pool.Sync(seats, nil)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if len(report.Harvested) != 1 || report.Harvested[0].App != "440" {
		t.Fatalf("harvested %+v, want the one install from seat1", report.Harvested)
	}

	if len(report.Delivered) != 1 || report.Delivered[0].Seat != "seat2" {
		t.Fatalf("delivered %+v, want one delivery to seat2", report.Delivered)
	}

	if len(report.Problems) != 0 {
		t.Errorf("problems: %v", report.Problems)
	}

	// The game reached seat2 in the same pass, files and manifest both.
	game := filepath.Join(pool.SeatApps("seat2"), commonDir, "Team Fortress 2", "game.bin")
	if got := read(t, game); got != "content of Team Fortress 2" {
		t.Errorf("seat2's copy reads %q", got)
	}

	manifest := read(t, filepath.Join(pool.SeatApps("seat2"), ManifestName("440")))

	// The account specific fields are cleared, the depot list is not. Without
	// the depot list Steam would download the game it already has.
	if strings.Contains(manifest, "76561197960287930") {
		t.Error("seat2 received the installing account's SteamID")
	}

	if strings.Contains(manifest, "1785199000") {
		t.Error("seat2 received the other seat's LastPlayed")
	}

	if !strings.Contains(manifest, `"manifest"		"2406473492863353429"`) {
		t.Error("the depot list did not survive, so Steam would download the game again")
	}

	// A second pass has nothing to do. A sync loop that found work every time
	// would rewrite libraries under running clients forever.
	report, err = pool.Sync(seats, nil)
	if err != nil {
		t.Fatal(err)
	}

	if !report.Empty() {
		t.Errorf("the second pass was not idle: %+v", report)
	}
}

func TestSyncIgnoresUnfinishedInstalls(t *testing.T) {
	pool := openPool(t)
	seats := members("seat1", "seat2")

	// 1026 is what Steam writes while it is downloading and staging.
	install(t, pool.SeatApps("seat1"), "440", "Team Fortress 2", "Team Fortress 2", "1000", 1026)

	report, err := pool.Sync(seats, nil)
	if err != nil {
		t.Fatal(err)
	}

	if !report.Empty() {
		t.Fatalf("a half finished download was shared: %+v", report)
	}

	if _, err := os.Stat(filepath.Join(pool.PoolApps(), commonDir, "Team Fortress 2")); err == nil {
		t.Error("a half finished download reached the pool")
	}
}

func TestSyncWaitsForAnInstallToSettle(t *testing.T) {
	pool := openPool(t)
	pool.Settle = time.Hour

	seats := members("seat1", "seat2")
	install(t, pool.SeatApps("seat1"), "440", "Team Fortress 2", "Team Fortress 2", "1000", StateInstalled)

	report, err := pool.Sync(seats, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Steam sets StateFlags to 4 and then keeps working for a while, so a
	// manifest that was touched a moment ago is not yet a finished install.
	if !report.Empty() {
		t.Errorf("an install that had just changed was taken straight away: %+v", report)
	}

	pool.Settle = 0

	report, err = pool.Sync(seats, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Harvested) != 1 {
		t.Errorf("after settling, harvested %+v", report.Harvested)
	}
}

func TestSyncDoesNotRestoreAnUninstalledGame(t *testing.T) {
	pool := openPool(t)
	seats := members("seat1", "seat2")

	install(t, pool.SeatApps("seat1"), "440", "Team Fortress 2", "Team Fortress 2", "1000", StateInstalled)

	if _, err := pool.Sync(seats, nil); err != nil {
		t.Fatal(err)
	}

	// seat2 uninstalls it, which is what Steam does: manifest and files gone.
	if err := os.Remove(filepath.Join(pool.SeatApps("seat2"), ManifestName("440"))); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(filepath.Join(pool.SeatApps("seat2"), commonDir, "Team Fortress 2")); err != nil {
		t.Fatal(err)
	}

	report, err := pool.Sync(seats, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Declined) != 1 || report.Declined[0].Seat != "seat2" {
		t.Fatalf("the removal was not noticed: %+v", report)
	}

	if len(report.Delivered) != 0 {
		t.Fatalf("the game was pushed back after being uninstalled: %+v", report.Delivered)
	}

	// And it stays gone across further passes. Somebody clearing space in their
	// own seat and finding the game back a minute later would be right to call
	// that broken.
	for i := range 3 {
		report, err := pool.Sync(seats, nil)
		if err != nil {
			t.Fatal(err)
		}

		if !report.Empty() {
			t.Fatalf("pass %d put it back: %+v", i, report)
		}
	}

	if _, err := os.Stat(filepath.Join(pool.SeatApps("seat2"), commonDir, "Team Fortress 2")); err == nil {
		t.Error("the uninstalled game is on disk again")
	}

	// Offer is the way back: asking for it explicitly clears the refusal.
	if err := pool.Offer("seat2", "440"); err != nil {
		t.Fatal(err)
	}

	report, err = pool.Sync(seats, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Delivered) != 1 {
		t.Errorf("Offer did not bring it back: %+v", report)
	}
}

// TestSyncNeverDowngradesThePool guards the direction of the build comparison.
//
// The first version took a copy from a seat whenever the build differed at all,
// either way, so a seat one patch behind quietly overwrote the pool's newer
// copy and handed that older build to everybody else. Direction is the whole
// content of this test.
func TestSyncNeverDowngradesThePool(t *testing.T) {
	pool := openPool(t)
	seats := members("ahead", "behind")

	install(t, pool.SeatApps("ahead"), "440", "Team Fortress 2", "Team Fortress 2", "2000", StateInstalled)
	write(t, filepath.Join(pool.SeatApps("ahead"), commonDir, "Team Fortress 2", "game.bin"), "new build", 0o644)

	install(t, pool.SeatApps("behind"), "440", "Team Fortress 2", "Team Fortress 2", "1000", StateInstalled)
	write(t, filepath.Join(pool.SeatApps("behind"), commonDir, "Team Fortress 2", "game.bin"), "old build", 0o644)

	for range 3 {
		if _, err := pool.Sync(seats, nil); err != nil {
			t.Fatal(err)
		}
	}

	// Whichever seat is read first, the pool has to end up on the newer build.
	// Nine hundred is not newer than one thousand either, which is why this is
	// compared as a number and not as text.
	app, err := ReadApp(filepath.Join(pool.PoolApps(), ManifestName("440")))
	if err != nil {
		t.Fatal(err)
	}

	if app.BuildID != "2000" {
		t.Errorf("the pool ended up on build %s, want 2000", app.BuildID)
	}

	if got := read(t, filepath.Join(pool.PoolApps(), commonDir, "Team Fortress 2", "game.bin")); got != "new build" {
		t.Errorf("the pool holds %q", got)
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
		why  string
	}{
		{"2000", "1000", true, "plainly newer"},
		{"1000", "2000", false, "plainly older"},
		{"1000", "1000", false, "the same build is not newer than itself"},
		{"10", "9", true, "compared as numbers, which text comparison gets backwards"},
		{"9", "10", false, "compared as numbers, which text comparison gets backwards"},
		{"", "1000", false, "an unknown build is no reason to replace files"},
		{"1000", "", false, "an unknown build is no reason to replace files"},
		{"abc", "1000", false, "not a number"},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v (%s)", c.a, c.b, got, c.want, c.why)
		}
	}
}

// TestSyncWaitsForASafeMomentToUpdate covers the other direction: the pool has
// a newer build than a seat, and whether it may be applied depends on the seat.
func TestSyncWaitsForASafeMomentToUpdate(t *testing.T) {
	pool := openPool(t)

	install(t, pool.SeatApps("source"), "440", "Team Fortress 2", "Team Fortress 2", "2000", StateInstalled)
	write(t, filepath.Join(pool.SeatApps("source"), commonDir, "Team Fortress 2", "game.bin"), "new build", 0o644)

	install(t, pool.SeatApps("busy"), "440", "Team Fortress 2", "Team Fortress 2", "1000", StateInstalled)
	write(t, filepath.Join(pool.SeatApps("busy"), commonDir, "Team Fortress 2", "game.bin"), "old build", 0o644)

	// Nobody may be updated yet: somebody is playing.
	report, err := pool.Sync(members("source", "busy"), nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Pending) != 1 || report.Pending[0].Seat != "busy" {
		t.Fatalf("the waiting update was not reported: %+v", report)
	}

	// Overwriting a game under a running client corrupts an install rather than
	// improving one, so the old copy has to still be exactly as it was.
	if got := read(t, filepath.Join(pool.SeatApps("busy"), commonDir, "Team Fortress 2", "game.bin")); got != "old build" {
		t.Fatalf("a busy seat was updated anyway, it now reads %q", got)
	}

	// The inventory says so rather than reporting the seat as simply having it.
	inv, err := pool.Inventory(members("source", "busy"))
	if err != nil {
		t.Fatal(err)
	}

	if got := inv.Titles[0].Stale; len(got) != 1 || got[0] != "busy" {
		t.Errorf("stale reads %v, want [busy]", got)
	}

	// Now the seat is free.
	report, err = pool.Sync(updatable("source", "busy"), nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Pending) != 0 {
		t.Errorf("still reported as waiting: %+v", report.Pending)
	}

	if got := read(t, filepath.Join(pool.SeatApps("busy"), commonDir, "Team Fortress 2", "game.bin")); got != "new build" {
		t.Errorf("the update was not applied, the file reads %q", got)
	}

	app, err := ReadApp(filepath.Join(pool.SeatApps("busy"), ManifestName("440")))
	if err != nil {
		t.Fatal(err)
	}

	if app.BuildID != "2000" {
		t.Errorf("the seat's manifest still says build %s", app.BuildID)
	}

	// And it settles: a further pass has nothing left to do.
	report, err = pool.Sync(updatable("source", "busy"), nil)
	if err != nil {
		t.Fatal(err)
	}

	if !report.Empty() || len(report.Pending) != 0 {
		t.Errorf("the pass after the update was not idle: %+v", report)
	}
}

// TestSyncLeavesASeatThatIsAheadAlone: a seat carrying a build newer than the
// pool's is not dragged backwards while the pool catches up.
func TestSyncLeavesASeatThatIsAheadAlone(t *testing.T) {
	pool := openPool(t)

	install(t, pool.SeatApps("old"), "440", "Team Fortress 2", "Team Fortress 2", "1000", StateInstalled)
	install(t, pool.SeatApps("new"), "440", "Team Fortress 2", "Team Fortress 2", "3000", StateInstalled)
	write(t, filepath.Join(pool.SeatApps("new"), commonDir, "Team Fortress 2", "game.bin"), "newest", 0o644)

	if _, err := pool.Sync(updatable("old", "new"), nil); err != nil {
		t.Fatal(err)
	}

	if got := read(t, filepath.Join(pool.SeatApps("new"), commonDir, "Team Fortress 2", "game.bin")); got != "newest" {
		t.Errorf("the seat ahead of the pool was rolled back to %q", got)
	}
}

func TestSyncKeepsSeatPrivateDirectories(t *testing.T) {
	pool := openPool(t)
	seats := members("seat1", "seat2")

	install(t, pool.SeatApps("seat1"), "440", "Team Fortress 2", "Team Fortress 2", "1000", StateInstalled)

	// compatdata holds the Proton prefix, which is the seat's own saves and
	// registry. Nothing may carry it between seats.
	prefix := filepath.Join(pool.SeatApps("seat1"), "compatdata", "440")
	mkdirs(t, prefix)
	write(t, filepath.Join(prefix, "user.reg"), "seat1's prefix", 0o644)

	if _, err := pool.Sync(seats, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(pool.SeatApps("seat2"), "compatdata", "440")); err == nil {
		t.Error("one seat's Proton prefix was copied into another")
	}

	if _, err := os.Stat(filepath.Join(pool.PoolApps(), "compatdata")); err == nil {
		t.Error("a Proton prefix reached the pool")
	}
}

// TestSourceIsTrackedNotImportedOnce is the difference between "the games that
// were on this machine when I pressed the button" and "the games on this
// machine". A one time import leaves the host's copy to drift ahead after the
// next update, and every seat would then download that update for itself.
func TestSourceIsTrackedNotImportedOnce(t *testing.T) {
	pool := openPool(t)

	// A library that is not a seat, which is how the games already on the host
	// get in.
	host := filepath.Join(reflinkDir(t), "hostlib")
	mkdirs(t, host)
	install(t, host, "570", "Dota 2", "dota 2 beta", "1000", StateInstalled)
	install(t, host, "730", "Half Life", "Half Life", "1000", 1026)

	report, err := pool.AddSource(host, nil)
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}

	if len(report.Harvested) != 1 || report.Harvested[0].App != "570" {
		t.Fatalf("took %+v, want only the finished install", report.Harvested)
	}

	if len(report.Problems) != 0 {
		t.Errorf("taking from the same filesystem reported problems: %v", report.Problems)
	}

	if got := pool.Sources(); len(got) != 1 {
		t.Fatalf("sources are %v, want the one library", got)
	}

	// From there it reaches a seat like anything else.
	seats := updatable("seat1")

	if _, err := pool.Sync(seats, nil); err != nil {
		t.Fatal(err)
	}

	if got := read(t, filepath.Join(pool.SeatApps("seat1"), commonDir, "dota 2 beta", "game.bin")); got != "content of Dota 2" {
		t.Errorf("the game did not reach seat1, got %q", got)
	}

	// Adding it twice changes nothing and does not list it twice.
	report, err = pool.AddSource(host, nil)
	if err != nil {
		t.Fatal(err)
	}

	if !report.Empty() {
		t.Errorf("adding the same source again was not idle: %+v", report)
	}

	if got := pool.Sources(); len(got) != 1 {
		t.Errorf("the source was listed twice: %v", got)
	}

	// Now the host updates the game. Nobody presses anything.
	install(t, host, "570", "Dota 2", "dota 2 beta", "2000", StateInstalled)
	write(t, filepath.Join(host, commonDir, "dota 2 beta", "game.bin"), "patched", 0o644)

	report, err = pool.Sync(seats, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Harvested) != 1 || report.Harvested[0].Seat != "host" {
		t.Fatalf("the host's update was not picked up: %+v", report)
	}

	if got := read(t, filepath.Join(pool.SeatApps("seat1"), commonDir, "dota 2 beta", "game.bin")); got != "patched" {
		t.Errorf("the update did not reach seat1, it still reads %q", got)
	}

	// The source itself is never written to. A pool that wrote back into
	// somebody's own Steam library would be editing files Steam owns.
	if got := read(t, filepath.Join(host, ManifestName("570"))); !strings.Contains(got, "76561197960287930") {
		t.Error("the source library was rewritten")
	}

	// Dropping the source leaves what was already taken from it in place,
	// because the seats are using it.
	if err := pool.RemoveSource(pool.Sources()[0]); err != nil {
		t.Fatal(err)
	}

	if got := pool.Sources(); len(got) != 0 {
		t.Errorf("the source is still tracked: %v", got)
	}

	if _, err := os.Stat(filepath.Join(pool.PoolApps(), commonDir, "dota 2 beta")); err != nil {
		t.Error("dropping the source took the game out of the pool")
	}
}

func TestSourceRefusesTheLibraryItself(t *testing.T) {
	pool := openPool(t)

	// Adding the pool's own directories would make it harvest from itself, and
	// a seat's library added by hand would let that seat's copies be taken as
	// though they were an independent library.
	for _, path := range []string{pool.Root(), pool.PoolApps(), pool.SeatApps("seat1")} {
		mkdirs(t, path)

		if _, err := pool.AddSource(path, nil); err == nil {
			t.Errorf("AddSource accepted %s, which is inside the library", path)
		}
	}
}

func TestInventory(t *testing.T) {
	pool := openPool(t)
	seats := members("seat1", "seat2", "seat3")

	install(t, pool.SeatApps("seat1"), "440", "Team Fortress 2", "Team Fortress 2", "1000", StateInstalled)

	if _, err := pool.Sync(seats, nil); err != nil {
		t.Fatal(err)
	}

	inv, err := pool.Inventory(seats)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}

	if len(inv.Titles) != 1 {
		t.Fatalf("inventory holds %d titles, want 1", len(inv.Titles))
	}

	title := inv.Titles[0]
	if len(title.In) != 3 {
		t.Errorf("the title reports itself in %v, want all three seats", title.In)
	}

	if inv.Bytes != 1000 {
		t.Errorf("the pool holds %d bytes, want 1000", inv.Bytes)
	}

	// Three seats each holding a 1000 byte title would have cost 3000 bytes if
	// the copies were real. They are not, and that number is the entire point
	// of the milestone.
	if inv.Saved != 3000 {
		t.Errorf("saved %d bytes, want 3000", inv.Saved)
	}

	// A seat that turned a title down is reported separately from one that has
	// it, so the interface can offer it again rather than look broken.
	os.Remove(filepath.Join(pool.SeatApps("seat3"), ManifestName("440")))
	os.RemoveAll(filepath.Join(pool.SeatApps("seat3"), commonDir, "Team Fortress 2"))

	if _, err := pool.Sync(seats, nil); err != nil {
		t.Fatal(err)
	}

	inv, err = pool.Inventory(seats)
	if err != nil {
		t.Fatal(err)
	}

	if got := inv.Titles[0].Declined; len(got) != 1 || got[0] != "seat3" {
		t.Errorf("declined reads %v, want [seat3]", got)
	}
}

func TestRemoveLeavesSeatsAlone(t *testing.T) {
	pool := openPool(t)
	seats := members("seat1", "seat2")

	install(t, pool.SeatApps("seat1"), "440", "Team Fortress 2", "Team Fortress 2", "1000", StateInstalled)

	if _, err := pool.Sync(seats, nil); err != nil {
		t.Fatal(err)
	}

	if err := pool.Remove("440"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := os.Stat(filepath.Join(pool.PoolApps(), commonDir, "Team Fortress 2")); err == nil {
		t.Error("the title is still in the pool")
	}

	// Both seats keep what they have. Reclaiming pool space must never uninstall
	// somebody's game, and because the blocks are shared this frees nothing
	// until the last seat lets go of it either.
	for _, seat := range []string{"seat1", "seat2"} {
		if _, err := os.Stat(filepath.Join(pool.SeatApps(seat), commonDir, "Team Fortress 2")); err != nil {
			t.Errorf("%s lost its copy when the pool entry was removed", seat)
		}
	}
}

func TestForget(t *testing.T) {
	pool := openPool(t)
	seats := members("seat1", "seat2")

	install(t, pool.SeatApps("seat1"), "440", "Team Fortress 2", "Team Fortress 2", "1000", StateInstalled)

	if _, err := pool.Sync(seats, nil); err != nil {
		t.Fatal(err)
	}

	os.Remove(filepath.Join(pool.SeatApps("seat2"), ManifestName("440")))
	os.RemoveAll(filepath.Join(pool.SeatApps("seat2"), commonDir, "Team Fortress 2"))

	if _, err := pool.Sync(seats, nil); err != nil {
		t.Fatal(err)
	}

	// A seat rebuilt under the same name starts clean rather than inheriting
	// the previous occupant's refusals.
	if err := pool.Forget("seat2"); err != nil {
		t.Fatal(err)
	}

	report, err := pool.Sync(seats, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(report.Delivered) != 1 {
		t.Errorf("after Forget the title was not offered again: %+v", report)
	}
}

func TestStateSurvivesAReopen(t *testing.T) {
	dir := reflinkDir(t)

	pool, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	pool.Settle = 0
	seats := members("seat1", "seat2")

	install(t, pool.SeatApps("seat1"), "440", "Team Fortress 2", "Team Fortress 2", "1000", StateInstalled)

	if _, err := pool.Sync(seats, nil); err != nil {
		t.Fatal(err)
	}

	os.Remove(filepath.Join(pool.SeatApps("seat2"), ManifestName("440")))
	os.RemoveAll(filepath.Join(pool.SeatApps("seat2"), commonDir, "Team Fortress 2"))

	if _, err := pool.Sync(seats, nil); err != nil {
		t.Fatal(err)
	}

	// A daemon restart must not undo somebody's decision to uninstall.
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	again.Settle = 0

	report, err := again.Sync(seats, nil)
	if err != nil {
		t.Fatal(err)
	}

	if !report.Empty() {
		t.Errorf("restarting put the uninstalled game back: %+v", report)
	}
}

func TestOpenRefusesAFilesystemWithoutReflinks(t *testing.T) {
	if _, err := os.Stat("/dev/shm"); err != nil {
		t.Skip("no /dev/shm to use as a filesystem without reflinks")
	}

	dir, err := os.MkdirTemp("/dev/shm", "polyseat-noreflink-")
	if err != nil {
		t.Skipf("cannot write to /dev/shm: %v", err)
	}

	defer os.RemoveAll(dir)

	// Refusing is the whole design. A pool that quietly made full copies would
	// fill the disk and only announce itself when there was no space left to
	// fix it in.
	if _, err := Open(dir); err == nil {
		t.Error("a pool was opened on a filesystem that cannot share blocks")
	}
}
