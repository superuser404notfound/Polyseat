package seat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	eaLauncher = "drive_c/Program Files/Electronic Arts/EA Desktop/EA Desktop/EALauncher.exe"
	eaGames    = "drive_c/Program Files/EA Games"
)

// ageTree puts every file and directory below dir an hour in the past, as a
// download that finished long ago would be.
func ageTree(t *testing.T, dir string) {
	t.Helper()

	old := time.Now().Add(-time.Hour)

	err := filepath.Walk(dir, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		return os.Chtimes(path, old, old)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The whole way a game the EA App installed takes: moved out of the store's
// place into the shared folders with the place becoming a link, read as a
// recipe that names the store and not the prefix, and written into another
// member that has the EA App, in that member's prefix and with its runner.
func TestAStoreGameIsSharedFromTheStoresPlace(t *testing.T) {
	host, hostPath := faugusHome(t)
	shared := filepath.Join(host, "Games/shared")
	ea := filepath.Join(host, "Faugus/ea-app")

	writeFile(t, filepath.Join(ea, eaLauncher), "MZ")
	writeFile(t, filepath.Join(ea, eaGames, "The Sims 4/Game/Bin/TS4_x64.exe"), "MZ")
	ageTree(t, filepath.Join(ea, eaGames))

	exe := filepath.Join(ea, eaGames, "The Sims 4/Game/Bin/TS4_x64.exe")
	writeFaugusGames(t, host,
		map[string]any{"gameid": "ea-app", "title": "EA App", "path": filepath.Join(ea, eaLauncher), "prefix": ea, "runner": "Default"},
		map[string]any{"gameid": "the-sims-4", "title": "The Sims 4", "path": exe, "prefix": ea, "runner": "Default"})

	res := runFaugusHelper(t, host, hostPath, "stores", shared)
	if res["status"] != "done" {
		t.Fatalf("stores answered %v", res)
	}

	st := res["stores"].([]any)[0].(map[string]any)
	if st["linked"] != true || len(st["moved"].([]any)) != 1 || st["problem"] != "" {
		t.Errorf("the store was not linked: %v", st)
	}

	if target, err := os.Readlink(filepath.Join(ea, eaGames)); err != nil || target != shared {
		t.Fatalf("the store's place is not a link to the shared folders: %q %v", target, err)
	}

	if _, err := os.Stat(filepath.Join(shared, "The Sims 4/Game/Bin/TS4_x64.exe")); err != nil {
		t.Fatalf("the game is not in the shared folders: %v", err)
	}

	// Run again, nothing to do.
	if res := runFaugusHelper(t, host, hostPath, "stores", shared); res["stores"].([]any)[0].(map[string]any)["linked"] != nil {
		t.Errorf("a second run linked again: %v", res)
	}

	scan := runFaugusHelper(t, host, hostPath, "scan", filepath.Join(host, "Games"), shared)

	var entry faugusEntry

	for _, g := range scan["games"].([]any) {
		if g.(map[string]any)["gameid"] == "the-sims-4" {
			data, _ := json.Marshal(g)
			_ = json.Unmarshal(data, &entry)
		}
	}

	if entry.Folder != "The Sims 4" || entry.Store != "ea" || entry.Exe != "Game/Bin/TS4_x64.exe" || entry.Prefix != "" {
		t.Fatalf("the store's game was read as %+v", entry)
	}

	recipe, err := entry.recipe("host")
	if err != nil {
		t.Fatal(err)
	}

	// The receiver: the folder arrived, and its EA App is in a prefix of a
	// different name with a runner of its own.
	seat, seatPath := faugusHome(t)
	seatShared := filepath.Join(seat, "Games/shared")
	seatEA := filepath.Join(seat, "Faugus/ea")
	writeFile(t, filepath.Join(seatShared, "The Sims 4/Game/Bin/TS4_x64.exe"), "MZ")
	writeFile(t, filepath.Join(seatEA, eaLauncher), "MZ")
	writeFaugusGames(t, seat,
		map[string]any{"gameid": "ea", "title": "EA App", "path": filepath.Join(seatEA, eaLauncher), "prefix": seatEA, "runner": "GE-Proton10-1"})

	arg, _ := json.Marshal(struct {
		Recipe faugusRecipe `json:"recipe"`
		Folder string       `json:"folder"`
	}{recipe, "The Sims 4"})

	// Before its place is a link the game cannot be started from it.
	if res := runFaugusHelper(t, seat, seatPath, "register", seatShared, string(arg)); res["status"] != "absent" {
		t.Errorf("register before the link answered %v", res)
	}

	runFaugusHelper(t, seat, seatPath, "stores", seatShared)

	if res := runFaugusHelper(t, seat, seatPath, "register", seatShared, string(arg)); res["status"] != "added" {
		t.Fatalf("register answered %v", res)
	}

	var written map[string]any

	for _, g := range faugusGamesOf(t, seat) {
		if g["gameid"] == "the-sims-4-polyseat" {
			written = g
		}
	}

	if written["path"] != filepath.Join(seatEA, eaGames, "The Sims 4/Game/Bin/TS4_x64.exe") ||
		written["prefix"] != seatEA || written["runner"] != "GE-Proton10-1" {
		t.Errorf("the entry written is %v", written)
	}

	// And again: the entry covers the folder through the link.
	if res := runFaugusHelper(t, seat, seatPath, "register", seatShared, string(arg)); res["status"] != "present" {
		t.Errorf("a second register answered %v", res)
	}
}

// A game still being written stays in the store's place, and so does one
// whose name is taken in the shared folders; the place is not linked then.
func TestAStoreGameInTheMiddleOfADownloadStays(t *testing.T) {
	home, path := faugusHome(t)
	shared := filepath.Join(home, "Games/shared")
	ea := filepath.Join(home, "Faugus/ea-app")

	writeFile(t, filepath.Join(ea, eaLauncher), "MZ")
	writeFile(t, filepath.Join(ea, eaGames, "Fresh/game.exe"), "MZ")
	writeFaugusGames(t, home,
		map[string]any{"gameid": "ea-app", "title": "EA App", "path": filepath.Join(ea, eaLauncher), "prefix": ea})

	res := runFaugusHelper(t, home, path, "stores", shared)
	st := res["stores"].([]any)[0].(map[string]any)

	if st["problem"] == "" || st["linked"] != nil {
		t.Errorf("a download in progress was moved: %v", st)
	}

	if info, err := os.Lstat(filepath.Join(ea, eaGames)); err != nil || !info.IsDir() {
		t.Fatalf("the store's place is no longer the directory it was: %v", err)
	}

	ageTree(t, filepath.Join(ea, eaGames))
	writeFile(t, filepath.Join(shared, "Fresh/other.exe"), "MZ")

	res = runFaugusHelper(t, home, path, "stores", shared)
	if st := res["stores"].([]any)[0].(map[string]any); st["problem"] == "" {
		t.Errorf("a game was moved onto a folder of the same name: %v", st)
	}

	if _, err := os.Stat(filepath.Join(ea, eaGames, "Fresh/game.exe")); err != nil {
		t.Errorf("the game left its place: %v", err)
	}
}

// A recipe for a store's game never carries a prefix of its own, and names a
// store the daemon knows.
func TestAStoreRecipeIsChecked(t *testing.T) {
	base := faugusEntry{GameID: "game", Title: "Game", Folder: "Game", Exe: "game.exe", Store: "ea"}

	if _, err := base.recipe("host"); err != nil {
		t.Fatalf("a plain store game was refused: %v", err)
	}

	withPrefix := base
	withPrefix.Prefix = "pfx"

	if _, err := withPrefix.recipe("host"); err == nil {
		t.Error("a store game with a prefix in its folder was taken")
	}

	unknown := base
	unknown.Store = "steam"

	if _, err := unknown.recipe("host"); err == nil {
		t.Error("an unknown store was taken")
	}
}
