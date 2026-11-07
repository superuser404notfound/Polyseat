package seat

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// heroicHome is a home with a Heroic on its PATH and none of its lists yet.
func heroicHome(t *testing.T) (home, path string) {
	t.Helper()

	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("SKIPPED: no python3 to run the helper with")
	}

	home = t.TempDir()
	path = filepath.Join(home, "bin")

	writeFile(t, filepath.Join(path, "heroic"), "#!/bin/sh\n")

	if err := os.Chmod(filepath.Join(path, "heroic"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(home, "Games/shared"), 0o755); err != nil {
		t.Fatal(err)
	}

	return home, path
}

func runHeroicHelper(t *testing.T, home, path string, args ...string) map[string]any {
	t.Helper()

	cmd := exec.Command("python3", append([]string{"-I", "-c", heroicShareScript}, args...)...)
	cmd.Env = []string{"HOME=" + home, "PATH=" + path}

	out, _ := cmd.CombinedOutput()

	var doc map[string]any
	if err := json.Unmarshal([]byte(lastLine(string(out))), &doc); err != nil {
		t.Fatalf("the helper printed %q", out)
	}

	return doc
}

func readJSONFile(t *testing.T, path string, into any) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal(data, into); err != nil {
		t.Fatal(err)
	}
}

const legendaryList = ".config/heroic/legendaryConfig/legendary/installed.json"

// The whole way an Epic game takes: installed by Heroic below ~/Games/Heroic,
// moved into the shared folders with its entry following, read as a recipe
// with nothing in it that names a path, and written into another member's
// Heroic under the same id, pointing at that member's copy.
func TestHeroicHelperMovesScansAndRegisters(t *testing.T) {
	host, hostPath := heroicHome(t)
	root := filepath.Join(host, "Games")
	shared := filepath.Join(root, "shared")
	game := filepath.Join(root, "Heroic/Hades")

	writeFile(t, filepath.Join(game, "x64/Hades.exe"), "MZ")
	writeFile(t, filepath.Join(host, legendaryList), `{
		"Min": {"app_name": "Min", "title": "Hades", "install_path": "`+game+`",
		        "executable": "x64/Hades.exe", "platform": "Windows", "is_dlc": false,
		        "save_path": "/home/x/saves", "prereq_info": {"path": "/x"},
		        "install_tags": [], "base_urls": ["https://example.com/"], "version": "1"},
		"MinDLC": {"app_name": "MinDLC", "title": "Hades DLC", "install_path": "`+game+`", "is_dlc": true}
	}`)

	scan := runHeroicHelper(t, host, hostPath, "scan", root, shared)
	games := scan["games"].([]any)

	if len(games) != 1 {
		t.Fatalf("scan found %v, DLC should not be among them", games)
	}

	if g := games[0].(map[string]any); g["key"] != "legendary:Min" || g["home_folder"] != "Heroic/Hades" || g["folder"] != "" {
		t.Errorf("before the move: %v", g)
	}

	moved := runHeroicHelper(t, host, hostPath, "move", root, shared, "legendary:Min")
	if moved["status"] != "moved" || moved["new"] != filepath.Join(shared, "Hades") {
		t.Fatalf("move answered %v", moved)
	}

	var list map[string]map[string]any

	readJSONFile(t, filepath.Join(host, legendaryList), &list)

	if list["Min"]["install_path"] != filepath.Join(shared, "Hades") {
		t.Errorf("the entry still points at the old place: %v", list["Min"]["install_path"])
	}

	scan = runHeroicHelper(t, host, hostPath, "scan", root, shared)

	var entry heroicEntry

	data, _ := json.Marshal(scan["games"].([]any)[0])
	_ = json.Unmarshal(data, &entry)

	if entry.Folder != "Hades" || entry.Ours {
		t.Fatalf("after the move: %+v", entry)
	}

	recipe, err := entry.recipe("host")
	if err != nil {
		t.Fatal(err)
	}

	for _, word := range []string{"install_path", "save_path", "prereq_info", "/home/"} {
		if strings.Contains(recipe.Entry, word) {
			t.Errorf("the recipe carries %q: %s", word, recipe.Entry)
		}
	}

	// The other member.
	seat, seatPath := heroicHome(t)
	seatShared := filepath.Join(seat, "Games/shared")
	writeFile(t, filepath.Join(seatShared, "Hades/x64/Hades.exe"), "MZ")

	arg, _ := json.Marshal(struct {
		Recipe heroicRecipe `json:"recipe"`
		Folder string       `json:"folder"`
	}{recipe, "Hades"})

	res := runHeroicHelper(t, seat, seatPath, "register", seatShared, string(arg))
	if res["status"] != "added" || res["written"] != "legendary:Min" {
		t.Fatalf("register answered %v", res)
	}

	readJSONFile(t, filepath.Join(seat, legendaryList), &list)

	if got := list["Min"]; got["install_path"] != filepath.Join(seatShared, "Hades") ||
		got["executable"] != "x64/Hades.exe" || got["title"] != "Hades" || got["save_path"] != nil {
		t.Errorf("the entry written is %v", got)
	}

	scan = runHeroicHelper(t, seat, seatPath, "scan", filepath.Join(seat, "Games"), seatShared)
	if g := scan["games"].([]any)[0].(map[string]any); g["ours"] != true {
		t.Errorf("the written entry is not known as the daemon's: %v", g)
	}

	if res := runHeroicHelper(t, seat, seatPath, "register", seatShared, string(arg)); res["status"] != "present" {
		t.Errorf("a second register answered %v", res)
	}

	if res := runHeroicHelper(t, seat, seatPath, "unregister", seatShared, "Hades", "legendary:Min"); res["status"] != "removed" {
		t.Errorf("unregister answered %v", res)
	}

	list = nil
	readJSONFile(t, filepath.Join(seat, legendaryList), &list)

	if _, ok := list["Min"]; ok {
		t.Error("the entry is still there after unregister")
	}
}

// An entry somebody made is never taken back, and one for a game the member
// has elsewhere is left alone.
func TestHeroicHelperLeavesOtherEntriesAlone(t *testing.T) {
	home, path := heroicHome(t)
	shared := filepath.Join(home, "Games/shared")
	writeFile(t, filepath.Join(shared, "Game/game.exe"), "MZ")
	writeFile(t, filepath.Join(home, ".config/heroic/gog_store/installed.json"),
		`{"installed": [{"appName": "1207658924", "install_path": "/elsewhere/Game", "platform": "windows"}]}`)

	arg := `{"folder": "Game", "recipe": {"runner": "gog", "app": "1207658924", "title": "Game", "entry": "{}"}}`
	if res := runHeroicHelper(t, home, path, "register", shared, arg); res["status"] != "taken" {
		t.Errorf("register over the member's own entry answered %v", res)
	}

	if res := runHeroicHelper(t, home, path, "unregister", shared, "Game", "gog:1207658924"); res["status"] != "refused" {
		t.Errorf("unregister of an entry nobody wrote answered %v", res)
	}
}

// Heroic is pointed at the shared folders when it installs into its own
// default, before it was ever started too, and left alone when somebody chose
// another directory.
func TestHeroicInstallPath(t *testing.T) {
	home, path := heroicHome(t)
	shared := filepath.Join(home, "Games/shared")

	res := runHeroicHelper(t, home, path, "install-path", shared)
	if st := res["stores"].([]any)[0].(map[string]any); st["linked"] != true {
		t.Fatalf("install-path answered %v", res)
	}

	var config struct {
		DefaultSettings map[string]any `json:"defaultSettings"`
		Version         string         `json:"version"`
	}

	readJSONFile(t, filepath.Join(home, ".config/heroic/config.json"), &config)

	if config.DefaultSettings["defaultInstallPath"] != shared || config.Version != "v0" {
		t.Errorf("config.json is %+v", config)
	}

	if res := runHeroicHelper(t, home, path, "install-path", shared); res["stores"].([]any)[0].(map[string]any)["linked"] != nil {
		t.Errorf("a second run changed something: %v", res)
	}

	other, otherPath := heroicHome(t)
	writeFile(t, filepath.Join(other, ".config/heroic/config.json"),
		`{"defaultSettings": {"defaultInstallPath": "/mnt/games", "language": "de"}, "version": "v0"}`)

	res = runHeroicHelper(t, other, otherPath, "install-path", filepath.Join(other, "Games/shared"))
	if st := res["stores"].([]any)[0].(map[string]any); st["problem"] == "" || st["linked"] != nil {
		t.Errorf("a chosen directory was not left alone: %v", st)
	}
}

// What a recipe may carry: the keys of its store, plain values, and a program
// inside the game's directory.
func TestHeroicRecipeIsChecked(t *testing.T) {
	base := heroicEntry{Key: "legendary:Min", Runner: "legendary", App: "Min", Title: "Hades",
		Folder: "Hades", Entry: `{"title": "Hades", "executable": "x64\\Hades.exe", "base_urls": ["a"]}`}

	if _, err := base.recipe("host"); err != nil {
		t.Fatalf("a plain entry was refused: %v", err)
	}

	for name, entry := range map[string]string{
		"a path":                  `{"install_path": "/x"}`,
		"a program outside":       `{"executable": "..\\..\\bin\\sh"}`,
		"an absolute program":     `{"executable": "/bin/sh"}`,
		"a nested value":          `{"title": {"x": 1}}`,
		"another store's key":     `{"appName": "1"}`,
		"a control character":     `{"title": "a\u0007"}`,
		"something that is not {": `[]`,
	} {
		e := base
		e.Entry = entry

		if _, err := e.recipe("host"); err == nil {
			t.Errorf("an entry with %s was taken", name)
		}
	}

	wrong := base
	wrong.Key = "gog:Min"

	if _, err := wrong.recipe("host"); err == nil {
		t.Error("a key that does not match its store and id was taken")
	}
}

// Each game becomes a card that starts it the way Heroic's own shortcuts do.
func TestHeroicListing(t *testing.T) {
	games, err := heroicListing(`[{"runner": "legendary", "app": "Min", "title": "Hades"},
		{"runner": "nile", "app": "amzn1.adg.product.1234", "title": "Amazon Game"},
		{"runner": "steam", "app": "1", "title": "Nope"},
		{"runner": "gog", "app": "1 2", "title": "Nope"}]`)
	if err != nil {
		t.Fatal(err)
	}

	if len(games) != 2 {
		t.Fatalf("listed %+v", games)
	}

	if games[0].Launch != `heroic --no-gui heroic://launch?appName=Min&runner=legendary` || games[0].Source != heroicSource {
		t.Errorf("the card starts %q", games[0].Launch)
	}
}
