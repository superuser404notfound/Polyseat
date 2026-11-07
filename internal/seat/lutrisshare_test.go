package seat

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The checks a recipe goes through are the boundary between a seat's Lutris
// and somebody else's, so each refusal is asserted rather than the happy case
// alone.
func TestLutrisRecipeRefusesWhatCouldLeaveTheFolder(t *testing.T) {
	good := lutrisEntry{
		Name: "Madison", Slug: "madison", Runner: "wine", Folder: "Madison",
		Exe: "MADiSON/MADiSON.exe", WineVersion: "Proton-CachyOS Latest",
	}

	if _, err := good.recipe("host"); err != nil {
		t.Fatalf("an ordinary entry was refused: %v", err)
	}

	for name, change := range map[string]func(*lutrisEntry){
		"absolute exe":       func(e *lutrisEntry) { e.Exe = "/usr/bin/sh" },
		"exe climbing out":   func(e *lutrisEntry) { e.Exe = "../other/game.exe" },
		"the folder as exe":  func(e *lutrisEntry) { e.Exe = "." },
		"no exe":             func(e *lutrisEntry) { e.Exe = "" },
		"prefix climbing":    func(e *lutrisEntry) { e.Prefix = "../../.wine" },
		"working dir abs":    func(e *lutrisEntry) { e.WorkingDir = "/tmp" },
		"steam runner":       func(e *lutrisEntry) { e.Runner = "steam" },
		"odd slug":           func(e *lutrisEntry) { e.Slug = "Mad Ison" },
		"the daemon's slug":  func(e *lutrisEntry) { e.Slug = "madison-polyseat" },
		"newline in args":    func(e *lutrisEntry) { e.Args = "-x\nsystem:" },
		"newline in version": func(e *lutrisEntry) { e.WineVersion = "a\nb" },
		"empty name":         func(e *lutrisEntry) { e.Name = " " },
		"odd arch":           func(e *lutrisEntry) { e.Arch = "arm64" },
	} {
		e := good
		change(&e)

		if _, err := e.recipe("host"); err == nil {
			t.Errorf("%s: accepted %+v", name, e)
		}
	}

	dot := good
	dot.Prefix, dot.WorkingDir = ".", "."

	if _, err := dot.recipe("host"); err != nil {
		t.Errorf("a prefix in the folder itself was refused: %v", err)
	}
}

// The first member to offer a recipe keeps it, an entry the daemon wrote is
// never read back as one, and a folder that is not in the pool has none.
func TestLutrisLearnKeepsTheFirstAndIgnoresItsOwn(t *testing.T) {
	s, _ := openLutrisShares("")
	pooled := func(name string) bool { return name != "Elsewhere" }

	entry := lutrisEntry{Name: "Madison", Slug: "madison", Runner: "wine", Folder: "Madison", Exe: "m.exe"}

	if err := s.learn("host", []lutrisEntry{entry}, pooled); err != nil {
		t.Fatal(err)
	}

	other := entry
	other.Exe = "other.exe"

	ours := entry
	ours.Folder, ours.Ours = "Copied", true

	away := entry
	away.Folder = "Elsewhere"

	_ = s.learn("vince", []lutrisEntry{other, ours, away}, pooled)

	recipes := s.recipes()

	if got := recipes["Madison"]; got.Exe != "m.exe" || got.From != "host" {
		t.Errorf("the second member replaced the first one's recipe: %+v", got)
	}

	if _, ok := recipes["Copied"]; ok {
		t.Error("an entry the daemon wrote was taken as a recipe")
	}

	if _, ok := recipes["Elsewhere"]; ok {
		t.Error("a folder that is not in the pool got a recipe")
	}

	// The member it came from may change it.
	_ = s.learn("host", []lutrisEntry{other}, pooled)

	if got := s.recipes()["Madison"]; got.Exe != "other.exe" {
		t.Errorf("the recipe's own member could not change it: %+v", got)
	}
}

// Everyone is owed a look once, and a member that was not done stays owed.
func TestLutrisOwedHandsOutEveryoneOnce(t *testing.T) {
	s, _ := openLutrisShares("")

	got := s.owed([]string{"host", "vince"})
	if !got["host"] || !got["vince"] {
		t.Fatalf("a fresh record did not owe everyone: %v", got)
	}

	s.done("host")

	got = s.owed([]string{"host", "vince"})
	if got["host"] || !got["vince"] {
		t.Errorf("after host was done: %v", got)
	}

	got = s.owed([]string{"host"})
	if len(got) != 0 {
		t.Errorf("a member that left is still owed: %v", got)
	}
}

// ------------------------------------------------------------------ helper

// lutrisHome builds a home with a Lutris in it, laid out the way Lutris 0.5
// writes one.
func lutrisHome(t *testing.T) string {
	t.Helper()

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("SKIPPED: no python3 to run the helper with")
	}

	if exec.Command(python, "-c", "import yaml").Run() != nil {
		t.Skip("SKIPPED: python3 has no yaml module, which Lutris itself needs")
	}

	home := t.TempDir()

	for _, dir := range []string{
		".local/share/lutris", ".config/lutris/games", "Games/shared",
		".local/share/Steam/compatibilitytools.d/proton-cachyos",
	} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(filepath.Join(home,
		".local/share/Steam/compatibilitytools.d/proton-cachyos/proton"), nil, 0o755); err != nil {
		t.Fatal(err)
	}

	script := `
import sqlite3, sys
db = sqlite3.connect(sys.argv[1])
db.execute("CREATE TABLE games (id INTEGER PRIMARY KEY, name TEXT, sortname TEXT, slug TEXT, installer_slug TEXT, parent_slug TEXT, platform TEXT, runner TEXT, executable TEXT, directory TEXT, updated DATETIME, lastplayed INTEGER, installed INTEGER, installed_at INTEGER, year INTEGER, configpath TEXT, has_custom_banner INTEGER, has_custom_icon INTEGER, has_custom_coverart_big INTEGER, playtime REAL, service TEXT, service_id TEXT, discord_id TEXT)")
db.commit()
`
	if out, err := exec.Command(python, "-c", script, filepath.Join(home, ".local/share/lutris/pga.db")).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	return home
}

func lutrisSQL(t *testing.T, home, query string, args ...any) {
	t.Helper()

	encoded, _ := json.Marshal(args)
	script := `
import json, sqlite3, sys
db = sqlite3.connect(sys.argv[1])
db.execute(sys.argv[2], json.loads(sys.argv[3]))
db.commit()
`
	out, err := exec.Command("python3", "-c", script,
		filepath.Join(home, ".local/share/lutris/pga.db"), query, string(encoded)).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func lutrisRows(t *testing.T, home string) []map[string]any {
	t.Helper()

	script := `
import json, sqlite3, sys
db = sqlite3.connect(sys.argv[1])
db.row_factory = sqlite3.Row
print(json.dumps([dict(r) for r in db.execute("select slug, runner, directory, configpath, installed from games order by id")]))
`
	out, err := exec.Command("python3", "-c", script, filepath.Join(home, ".local/share/lutris/pga.db")).Output()
	if err != nil {
		t.Fatal(err)
	}

	var rows []map[string]any
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatal(err)
	}

	return rows
}

func runLutrisHelper(t *testing.T, home string, args ...string) map[string]any {
	t.Helper()

	cmd := exec.Command("python3", append([]string{"-I", "-c", lutrisShareScript}, args...)...)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}

	out, _ := cmd.CombinedOutput()

	var doc map[string]any
	if err := json.Unmarshal([]byte(lastLine(string(out))), &doc); err != nil {
		t.Fatalf("the helper printed %q", out)
	}

	return doc
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The whole way a game added by hand on the host takes: listed with the
// directory it is in, moved into the shared folders with its entry following
// it, read as a recipe, and written into another member's Lutris under that
// member's name for the same Proton.
func TestLutrisHelperMovesScansAndRegisters(t *testing.T) {
	host := lutrisHome(t)
	root := filepath.Join(host, "Games")
	shared := filepath.Join(root, "shared")

	exe := filepath.Join(root, "Madison-AnkerGames/MADiSON/MADiSON.exe")
	writeFile(t, exe, "MZ")
	writeFile(t, filepath.Join(host, ".config/lutris/games/madison-1.yml"),
		"game:\n  exe: "+exe+"\nwine:\n  version: Proton-CachyOS Latest\nsystem:\n  prelaunch_command: rm -rf ~\n")
	lutrisSQL(t, host, "insert into games (name, slug, runner, platform, directory, installed, configpath) values (?, ?, ?, ?, ?, 1, ?)",
		"Madison", "madison", "wine", "Windows", "", "madison-1")

	scan := runLutrisHelper(t, host, "scan", root, shared)
	games := scan["games"].([]any)

	if len(games) != 1 {
		t.Fatalf("scan found %v", games)
	}

	g := games[0].(map[string]any)
	if g["folder"] != "" || g["home_folder"] != "Madison-AnkerGames" {
		t.Errorf("before the move: %v", g)
	}

	moved := runLutrisHelper(t, host, "move", root, shared, "madison")
	if moved["status"] != "moved" {
		t.Fatalf("move answered %v", moved)
	}

	if _, err := os.Stat(filepath.Join(shared, "Madison-AnkerGames/MADiSON/MADiSON.exe")); err != nil {
		t.Errorf("the game is not where it was moved to: %v", err)
	}

	yml, _ := os.ReadFile(filepath.Join(host, ".config/lutris/games/madison-1.yml"))
	if !strings.Contains(string(yml), filepath.Join(shared, "Madison-AnkerGames")) {
		t.Errorf("the entry still points at the old place:\n%s", yml)
	}

	scan = runLutrisHelper(t, host, "scan", root, shared)

	var entry lutrisEntry

	data, _ := json.Marshal(scan["games"].([]any)[0])
	_ = json.Unmarshal(data, &entry)

	if entry.Folder != "Madison-AnkerGames" || entry.Exe != "MADiSON/MADiSON.exe" {
		t.Fatalf("after the move: %+v", entry)
	}

	recipe, err := entry.recipe("host")
	if err != nil {
		t.Fatal(err)
	}

	// The other member: the same folder arrived, and its Lutris names Proton
	// CachyOS differently.
	seat := lutrisHome(t)
	seatShared := filepath.Join(seat, "Games/shared")
	writeFile(t, filepath.Join(seatShared, "Madison-AnkerGames/MADiSON/MADiSON.exe"), "MZ")

	arg, _ := json.Marshal(struct {
		Recipe lutrisRecipe `json:"recipe"`
		Folder string       `json:"folder"`
	}{recipe, "Madison-AnkerGames"})

	res := runLutrisHelper(t, seat, "register", seatShared, string(arg))
	if res["status"] != "added" || res["wine_version"] != "proton-cachyos" {
		t.Fatalf("register answered %v", res)
	}

	written, _ := os.ReadFile(filepath.Join(seat, ".config/lutris/games/madison-polyseat.yml"))
	if !strings.Contains(string(written), filepath.Join(seatShared, "Madison-AnkerGames/MADiSON/MADiSON.exe")) {
		t.Errorf("the entry does not start the member's own copy:\n%s", written)
	}

	if strings.Contains(string(written), "prelaunch") {
		t.Errorf("a launch hook travelled with the entry:\n%s", written)
	}

	rows := lutrisRows(t, seat)
	if len(rows) != 1 || rows[0]["directory"] != filepath.Join(seatShared, "Madison-AnkerGames") {
		t.Fatalf("pga.db holds %v", rows)
	}

	// Asked again, it sees its own entry and writes no second one.
	if res := runLutrisHelper(t, seat, "register", seatShared, string(arg)); res["status"] != "present" {
		t.Errorf("the second register answered %v", res)
	}

	// The scan marks it as the daemon's own, which keeps it from coming back
	// round as a recipe.
	again := runLutrisHelper(t, seat, "scan", filepath.Join(seat, "Games"), seatShared)
	if mine := again["games"].([]any)[0].(map[string]any); mine["ours"] != true {
		t.Errorf("the written entry was not recognised as the daemon's: %v", mine)
	}

	// Gone again: only the entry the daemon wrote is removed.
	lutrisSQL(t, seat, "insert into games (name, slug, runner, directory, installed, configpath) values (?, ?, ?, ?, 1, ?)",
		"Mine", "mine", "wine", filepath.Join(seatShared, "Madison-AnkerGames"), "mine")

	if res := runLutrisHelper(t, seat, "unregister", seatShared, "Madison-AnkerGames", "mine"); res["status"] != "refused" {
		t.Errorf("unregister of an entry somebody made answered %v", res)
	}

	if res := runLutrisHelper(t, seat, "unregister", seatShared, "Madison-AnkerGames", "madison-polyseat"); res["status"] != "removed" {
		t.Errorf("unregister answered %v", res)
	}

	if rows := lutrisRows(t, seat); len(rows) != 1 || rows[0]["slug"] != "mine" {
		t.Errorf("after unregister pga.db holds %v", rows)
	}

	if _, err := os.Stat(filepath.Join(seat, ".config/lutris/games/madison-polyseat.yml")); !os.IsNotExist(err) {
		t.Errorf("the written YAML is still there: %v", err)
	}
}

// A slug the member already uses for something else is left alone rather
// than given a second entry, and a version nothing here resembles is left to
// the member's own default.
func TestLutrisHelperRegisterLeavesATakenSlugAlone(t *testing.T) {
	seat := lutrisHome(t)
	shared := filepath.Join(seat, "Games/shared")
	writeFile(t, filepath.Join(shared, "Game/game.exe"), "MZ")

	lutrisSQL(t, seat, "insert into games (name, slug, runner, directory, installed, configpath) values (?, ?, ?, ?, 1, ?)",
		"Game", "game", "wine", filepath.Join(seat, "Games/game"), "game-1")

	arg := `{"folder":"Game","recipe":{"name":"Game","slug":"game","runner":"wine","exe":"game.exe","wine_version":"wine-ge-8"}}`
	if res := runLutrisHelper(t, seat, "register", shared, arg); res["status"] != "taken" {
		t.Errorf("answered %v", res)
	}

	arg = `{"folder":"Game","recipe":{"name":"Other","slug":"other","runner":"wine","exe":"game.exe","wine_version":"lutris-7.2"}}`

	res := runLutrisHelper(t, seat, "register", shared, arg)
	if res["status"] != "added" || res["wine_version"] != "" {
		t.Errorf("answered %v", res)
	}
}

// A deleted seat takes its record with it, and its recipes stay for the seats
// still to come, taken over by the next member to offer one.
func TestLutrisForgetMemberKeepsItsRecipes(t *testing.T) {
	s, _ := openLutrisShares("")
	pooled := func(string) bool { return true }

	entry := lutrisEntry{Name: "Game", Slug: "game", Runner: "wine", Folder: "Game", Exe: "a.exe"}
	_ = s.learn("louis", []lutrisEntry{entry}, pooled)
	_ = s.note("louis", "Other", "other-polyseat", false)

	if err := s.forgetMember("louis"); err != nil {
		t.Fatal(err)
	}

	if len(s.registered("louis")) != 0 {
		t.Error("the deleted seat's entries are still on record")
	}

	if got := s.recipes()["Game"]; got.Exe != "a.exe" || got.From != "" {
		t.Fatalf("the recipe was not kept without an author: %+v", got)
	}

	entry.Exe = "b.exe"
	_ = s.learn("vince", []lutrisEntry{entry}, pooled)

	if got := s.recipes()["Game"]; got.From != "vince" {
		t.Errorf("nobody took the recipe over: %+v", got)
	}
}

// A recipe goes when its own member stops offering it, which is what moving a
// game to another launcher looks like from here; another member's entry, or
// one the daemon wrote there, does not keep it.
func TestLutrisLearnWithdrawsARecipeItsMemberDropped(t *testing.T) {
	s, _ := openLutrisShares("")
	pooled := func(string) bool { return true }

	madison := lutrisEntry{Name: "Madison", Slug: "madison", Runner: "wine", Folder: "Madison", Exe: "m.exe"}
	pinata := lutrisEntry{Name: "Viva", Slug: "viva", Runner: "linux", Folder: "Viva Pinata", Exe: "v.sh"}
	_ = s.learn("host", []lutrisEntry{madison, pinata}, pooled)

	copied := madison
	copied.Ours = true

	if err := s.learn("host", []lutrisEntry{pinata, copied}, pooled); err != nil {
		t.Fatal(err)
	}

	recipes := s.recipes()

	if _, ok := recipes["Madison"]; ok {
		t.Error("a recipe stayed after its member took the game out")
	}

	if _, ok := recipes["Viva Pinata"]; !ok {
		t.Error("a recipe its member still offers was withdrawn")
	}

	// Somebody else's scan never withdraws it.
	_ = s.learn("vince", nil, pooled)

	if _, ok := s.recipes()["Viva Pinata"]; !ok {
		t.Error("another member's scan withdrew the recipe")
	}

	// And it can be offered again, by anyone.
	_ = s.learn("vince", []lutrisEntry{madison}, pooled)

	if got := s.recipes()["Madison"]; got.From != "vince" {
		t.Errorf("a withdrawn recipe could not be offered again: %+v", got)
	}
}
