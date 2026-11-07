package seat

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The checks a recipe goes through are the boundary between a seat's Faugus
// and somebody else's. Faugus takes any word of the form NAME=value in the
// command it builds as a variable for the game, so the arguments are checked
// for that as well as the paths for leaving the folder.
func TestFaugusRecipeRefusesWhatCouldLeaveTheFolder(t *testing.T) {
	good := faugusEntry{
		GameID: "madison", Title: "Madison", Folder: "Madison-AnkerGames",
		Exe: "MADiSON/MADiSON.exe", Runner: "Proton-CachyOS Latest",
		GameArgs: "-windowed -res=1920x1080", Protonfix: "umu-1234",
	}

	if _, err := good.recipe("host"); err != nil {
		t.Fatalf("an ordinary entry was refused: %v", err)
	}

	for name, change := range map[string]func(*faugusEntry){
		"absolute exe":        func(e *faugusEntry) { e.Exe = "/usr/bin/sh" },
		"exe climbing out":    func(e *faugusEntry) { e.Exe = "../other/game.exe" },
		"no exe":              func(e *faugusEntry) { e.Exe = "" },
		"prefix climbing":     func(e *faugusEntry) { e.Prefix = "../../.wine" },
		"steam runner":        func(e *faugusEntry) { e.Runner = "Steam" },
		"quote in runner":     func(e *faugusEntry) { e.Runner = "x' LD_PRELOAD='y" },
		"variable in args":    func(e *faugusEntry) { e.GameArgs = "-x LD_PRELOAD=/tmp/a.so" },
		"quote in args":       func(e *faugusEntry) { e.GameArgs = `-x "a b"` },
		"odd protonfix":       func(e *faugusEntry) { e.Protonfix = "a=b" },
		"odd runtime":         func(e *faugusEntry) { e.Runtime = "/bin/sh" },
		"space in id":         func(e *faugusEntry) { e.GameID = "mad ison" },
		"quote in id":         func(e *faugusEntry) { e.GameID = "mad'ison" },
		"the daemon's own id": func(e *faugusEntry) { e.GameID = "madison-polyseat" },
		"empty title":         func(e *faugusEntry) { e.Title = " " },
		"newline in title":    func(e *faugusEntry) { e.Title = "a\nb" },
	} {
		e := good
		change(&e)

		if _, err := e.recipe("host"); err == nil {
			t.Errorf("%s: accepted %+v", name, e)
		}
	}

	for _, runner := range []string{"Default", "Linux-Native", "Proton-CachyOS (System)",
		"GE-Proton10-25", "/usr/share/steam/compatibilitytools.d/proton-cachyos"} {
		e := good
		e.Runner = runner

		if _, err := e.recipe("host"); err != nil {
			t.Errorf("runner %q was refused: %v", runner, err)
		}
	}

	// Faugus keeps letters of any script in an id.
	e := good
	e.GameID = "pokémon-perle"

	if _, err := e.recipe("host"); err != nil {
		t.Errorf("an id with an accent was refused: %v", err)
	}
}

// ------------------------------------------------------------------ helper

// faugusHome builds a home with a Faugus in it, laid out the way Faugus 2.5
// writes one, and a PATH on which faugus-launcher exists.
func faugusHome(t *testing.T) (home, path string) {
	t.Helper()

	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("SKIPPED: no python3 to run the helper with")
	}

	home = t.TempDir()
	path = filepath.Join(home, "bin")

	writeFile(t, filepath.Join(path, "faugus-launcher"), "#!/bin/sh\n")

	if err := os.Chmod(filepath.Join(path, "faugus-launcher"), 0o755); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(home, ".config/faugus-launcher/config.json"),
		`{"default-prefix": "`+filepath.Join(home, "Faugus")+`", "default-runner": "Proton-CachyOS Latest"}`)

	if err := os.MkdirAll(filepath.Join(home, "Games/shared"), 0o755); err != nil {
		t.Fatal(err)
	}

	return home, path
}

func faugusGamesOf(t *testing.T, home string) []map[string]any {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(home, ".local/share/faugus-launcher/games.json"))
	if err != nil {
		t.Fatal(err)
	}

	var games []map[string]any
	if err := json.Unmarshal(data, &games); err != nil {
		t.Fatal(err)
	}

	return games
}

func writeFaugusGames(t *testing.T, home string, games ...map[string]any) {
	t.Helper()

	data, _ := json.Marshal(games)
	writeFile(t, filepath.Join(home, ".local/share/faugus-launcher/games.json"), string(data))
}

func runFaugusHelper(t *testing.T, home, path string, args ...string) map[string]any {
	t.Helper()

	cmd := exec.Command("python3", append([]string{"-I", "-c", faugusShareScript}, args...)...)
	cmd.Env = []string{"HOME=" + home, "PATH=" + path}

	out, _ := cmd.CombinedOutput()

	var doc map[string]any
	if err := json.Unmarshal([]byte(lastLine(string(out))), &doc); err != nil {
		t.Fatalf("the helper printed %q", out)
	}

	return doc
}

// The whole way a game added to Faugus on the host takes: listed with the
// directory it is in, moved into the shared folders with its entry and its
// shortcut following it, read as a recipe, and written into another member's
// Faugus with that member's own prefix and its own Proton CachyOS.
func TestFaugusHelperMovesScansAndRegisters(t *testing.T) {
	host, hostPath := faugusHome(t)
	root := filepath.Join(host, "Games")
	shared := filepath.Join(root, "shared")

	exe := filepath.Join(root, "Madison-AnkerGames/MADiSON/MADiSON.exe")
	writeFile(t, exe, "MZ")
	writeFaugusGames(t, host, map[string]any{
		"gameid": "madison", "title": "Madison", "path": exe,
		"prefix": filepath.Join(host, "Faugus/madison"), "runner": "Default",
		"launch_arguments": "LD_PRELOAD=/tmp/x.so", "pre_launch": "rm -rf ~",
		"addapp_bat": filepath.Join(root, "Madison-AnkerGames/MADiSON/faugus-madison.bat"),
	})

	shortcut := filepath.Join(host, ".local/share/applications/madison.desktop")
	writeFile(t, shortcut, "[Desktop Entry]\nName=Madison\nExec=/usr/bin/faugus-launcher --game madison\n"+
		"Type=Application\nPath="+filepath.Dir(exe)+"\n")

	scan := runFaugusHelper(t, host, hostPath, "scan", root, shared)
	if scan["present"] != true {
		t.Fatalf("scan answered %v", scan)
	}

	g := scan["games"].([]any)[0].(map[string]any)
	if g["folder"] != "" || g["home_folder"] != "Madison-AnkerGames" {
		t.Errorf("before the move: %v", g)
	}

	moved := runFaugusHelper(t, host, hostPath, "move", root, shared, "madison")
	if moved["status"] != "moved" || moved["new"] != filepath.Join(shared, "Madison-AnkerGames") {
		t.Fatalf("move answered %v", moved)
	}

	after := faugusGamesOf(t, host)[0]
	if after["path"] != filepath.Join(shared, "Madison-AnkerGames/MADiSON/MADiSON.exe") {
		t.Errorf("the entry still points at the old place: %v", after["path"])
	}

	if after["prefix"] != filepath.Join(host, "Faugus/madison") {
		t.Errorf("the prefix outside the folder was moved along: %v", after["prefix"])
	}

	if text, _ := os.ReadFile(shortcut); !strings.Contains(string(text), "Path="+filepath.Join(shared, "Madison-AnkerGames/MADiSON")) {
		t.Errorf("the shortcut still starts in the old place:\n%s", text)
	}

	scan = runFaugusHelper(t, host, hostPath, "scan", root, shared)

	var entry faugusEntry

	data, _ := json.Marshal(scan["games"].([]any)[0])
	_ = json.Unmarshal(data, &entry)

	if entry.Folder != "Madison-AnkerGames" || entry.Exe != "MADiSON/MADiSON.exe" ||
		entry.Prefix != "" || entry.Runner != "Proton-CachyOS Latest" {
		t.Fatalf("after the move: %+v", entry)
	}

	recipe, err := entry.recipe("host")
	if err != nil {
		t.Fatal(err)
	}

	// The other member: the same folder arrived, and it has a Proton CachyOS
	// of its own under another name, which is what the entry should use.
	seat, seatPath := faugusHome(t)
	seatShared := filepath.Join(seat, "Games/shared")
	writeFile(t, filepath.Join(seatShared, "Madison-AnkerGames/MADiSON/MADiSON.exe"), "MZ")
	writeFile(t, filepath.Join(seat, ".local/share/Steam/compatibilitytools.d/proton-cachyos/proton"), "")

	arg, _ := json.Marshal(struct {
		Recipe faugusRecipe `json:"recipe"`
		Folder string       `json:"folder"`
	}{recipe, "Madison-AnkerGames"})

	res := runFaugusHelper(t, seat, seatPath, "register", seatShared, string(arg))
	if res["status"] != "added" || res["written"] != "madison-polyseat" || res["runner"] != "proton-cachyos" {
		t.Fatalf("register answered %v", res)
	}

	written := faugusGamesOf(t, seat)
	if len(written) != 1 {
		t.Fatalf("games.json holds %v", written)
	}

	w := written[0]
	if w["path"] != filepath.Join(seatShared, "Madison-AnkerGames/MADiSON/MADiSON.exe") ||
		w["prefix"] != filepath.Join(seat, "Faugus/madison-polyseat") || w["title"] != "Madison" {
		t.Errorf("the entry does not start the member's own copy in its own prefix: %v", w)
	}

	if w["launch_arguments"] != "" || w["pre_launch"] != "" {
		t.Errorf("a launch command travelled with the entry: %v", w)
	}

	// Asked again, it sees its own entry and writes no second one.
	if res := runFaugusHelper(t, seat, seatPath, "register", seatShared, string(arg)); res["status"] != "present" {
		t.Errorf("the second register answered %v", res)
	}

	// The scan marks it as the daemon's own, which keeps it from coming back
	// round as a recipe.
	again := runFaugusHelper(t, seat, seatPath, "scan", filepath.Join(seat, "Games"), seatShared)
	if mine := again["games"].([]any)[0].(map[string]any); mine["ours"] != true {
		t.Errorf("the written entry was not recognised as the daemon's: %v", mine)
	}

	// Gone again: only the entry the daemon wrote is removed.
	games := faugusGamesOf(t, seat)
	games = append(games, map[string]any{"gameid": "mine", "title": "Mine",
		"path": filepath.Join(seatShared, "Madison-AnkerGames/MADiSON/MADiSON.exe")})
	writeFaugusGames(t, seat, games...)

	if res := runFaugusHelper(t, seat, seatPath, "unregister", seatShared, "Madison-AnkerGames", "mine"); res["status"] != "refused" {
		t.Errorf("unregister of an entry somebody made answered %v", res)
	}

	if res := runFaugusHelper(t, seat, seatPath, "unregister", seatShared, "Madison-AnkerGames", "madison-polyseat"); res["status"] != "removed" {
		t.Errorf("unregister answered %v", res)
	}

	if left := faugusGamesOf(t, seat); len(left) != 1 || left[0]["gameid"] != "mine" {
		t.Errorf("after unregister games.json holds %v", left)
	}
}

// A member without Faugus is told apart from one with an empty list, because
// the first is asked again once Faugus is there and the second is not owed
// anything.
func TestFaugusHelperWithoutFaugus(t *testing.T) {
	home, _ := faugusHome(t)
	shared := filepath.Join(home, "Games/shared")
	writeFile(t, filepath.Join(shared, "Game/game.exe"), "MZ")

	nowhere := t.TempDir()

	if res := runFaugusHelper(t, home, nowhere, "scan", filepath.Join(home, "Games"), shared); res["present"] != false {
		t.Errorf("scan without Faugus answered %v", res)
	}

	arg := `{"folder":"Game","recipe":{"gameid":"game","title":"Game","exe":"game.exe"}}`
	if res := runFaugusHelper(t, home, nowhere, "register", shared, arg); res["status"] != "absent" {
		t.Errorf("register without Faugus answered %v", res)
	}
}

// An id the member already uses is left alone, and a Proton nothing here
// resembles is left to the member's own default.
func TestFaugusHelperRegisterLeavesATakenIDAlone(t *testing.T) {
	home, path := faugusHome(t)
	shared := filepath.Join(home, "Games/shared")
	writeFile(t, filepath.Join(shared, "Game/game.exe"), "MZ")
	writeFaugusGames(t, home, map[string]any{"gameid": "game-polyseat", "title": "Game",
		"path": filepath.Join(home, "Games/elsewhere/game.exe")})

	arg := `{"folder":"Game","recipe":{"gameid":"game","title":"Game","exe":"game.exe"}}`
	if res := runFaugusHelper(t, home, path, "register", shared, arg); res["status"] != "taken" {
		t.Errorf("answered %v", res)
	}

	arg = `{"folder":"Game","recipe":{"gameid":"other","title":"Other","exe":"game.exe","runner":"wine-ge-8"}}`
	if res := runFaugusHelper(t, home, path, "register", shared, arg); res["status"] != "added" || res["runner"] != "Default" {
		t.Errorf("answered %v", res)
	}
}

// A folder Lutris moved is followed by Faugus's entry for it too, and the
// other way round, so a game in both does not break in one of them.
func TestTheOtherLauncherFollowsAMove(t *testing.T) {
	home, path := faugusHome(t)
	old := filepath.Join(home, "Games/Game")
	moved := filepath.Join(home, "Games/shared/Game")

	writeFaugusGames(t, home, map[string]any{"gameid": "game", "title": "Game",
		"path": filepath.Join(old, "game.exe"), "prefix": filepath.Join(home, "Faugus/game")})

	if res := runFaugusHelper(t, home, path, "repoint", old, moved); res["status"] != "repointed" {
		t.Fatalf("repoint answered %v", res)
	}

	if got := faugusGamesOf(t, home)[0]; got["path"] != filepath.Join(moved, "game.exe") ||
		got["prefix"] != filepath.Join(home, "Faugus/game") {
		t.Errorf("after repoint: %v", got)
	}

	lutris := lutrisHome(t)
	lold := filepath.Join(lutris, "Games/Game")
	lnew := filepath.Join(lutris, "Games/shared/Game")

	writeFile(t, filepath.Join(lutris, ".config/lutris/games/game-1.yml"),
		"game:\n  exe: "+filepath.Join(lold, "game.exe")+"\n")
	lutrisSQL(t, lutris, "insert into games (name, slug, runner, directory, installed, configpath) values (?, ?, ?, ?, 1, ?)",
		"Game", "game", "wine", lold, "game-1")

	if res := runLutrisHelper(t, lutris, "repoint", lold, lnew); res["status"] != "repointed" {
		t.Fatalf("Lutris repoint answered %v", res)
	}

	if rows := lutrisRows(t, lutris); rows[0]["directory"] != lnew {
		t.Errorf("pga.db still names the old place: %v", rows)
	}

	yml, _ := os.ReadFile(filepath.Join(lutris, ".config/lutris/games/game-1.yml"))
	if !strings.Contains(string(yml), filepath.Join(lnew, "game.exe")) {
		t.Errorf("the YAML still names the old place:\n%s", yml)
	}
}

// ------------------------------------------------------------------ package

// Named in three places that nothing but this ties together, the same way the
// other packages are: the version and build here, the recipe, and the
// checksum of what the recipe produced.
func TestTheFaugusPackageIsTheOneTheRecipeBuilds(t *testing.T) {
	recipe, err := os.ReadFile("../../packaging/faugus/PKGBUILD")
	if err != nil {
		t.Fatalf("the recipe is not where the source says it is: %v", err)
	}

	field := func(name string) string {
		m := regexp.MustCompile(`(?m)^` + name + `=(\S+)`).FindSubmatch(recipe)
		if m == nil {
			t.Fatalf("the recipe sets no %s", name)
		}

		return string(m[1])
	}

	if got := field("pkgname"); got != "faugus-launcher" {
		t.Errorf("the recipe builds %q, and stepFaugus asks pacman for faugus-launcher", got)
	}

	if got := field("pkgver"); got != FaugusVersion {
		t.Errorf("the recipe builds %q and seats ask for %q", got, FaugusVersion)
	}

	if got := field("pkgrel"); got != FaugusBuild {
		t.Errorf("the recipe builds pkgrel %q and seats ask for %q", got, FaugusBuild)
	}

	if got := field("arch"); got != "('any')" {
		t.Errorf("the recipe is for %s, and the address names any", got)
	}

	// A recipe that downloads whatever the tag points at today is not the one
	// whose package was checked.
	if strings.Contains(string(recipe), "SKIP") {
		t.Error("the recipe does not pin its source")
	}

	v := FaugusVersion + "-" + FaugusBuild
	want := "https://github.com/superuser404notfound/Polyseat/releases/download/faugus-" +
		v + "/faugus-launcher-" + v + "-any.pkg.tar.zst"

	if got := faugusPackageURL(); got != want {
		t.Errorf("the package is looked for at %s, want %s", got, want)
	}

	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(faugusPackageSHA256) {
		t.Errorf("%q is not a sha256", faugusPackageSHA256)
	}
}

// ------------------------------------------------------------------ listing

// What a seat's Faugus has becomes a card that starts the game, and an id
// that could be more than one word in Sunshine's command never does.
func TestFaugusScanAndListing(t *testing.T) {
	home, path := faugusHome(t)
	icon := filepath.Join(home, ".local/share/faugus-launcher/icons/madison.png")
	writeFile(t, icon, "png")
	writeFaugusGames(t, home,
		map[string]any{"gameid": "madison", "title": "Madison", "icon": icon},
		map[string]any{"gameid": "fnaf", "title": "FNAF", "hidden": true},
		map[string]any{"gameid": "evil id", "title": "Evil"},
		map[string]any{"gameid": "x$(reboot)", "title": "Evil too"},
	)

	cmd := exec.Command("python3", "-I", "-c", faugusScan)
	cmd.Env = []string{"POLYSEAT_HOME=" + home, "PATH=" + path}

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	games, err := faugusListing(string(out))
	if err != nil {
		t.Fatal(err)
	}

	if len(games) != 1 {
		t.Fatalf("listed %+v", games)
	}

	if g := games[0]; g.Name != "Madison" || g.Launch != "faugus-launcher --game madison" ||
		g.Image != icon || g.Source != faugusSource {
		t.Errorf("listed %+v", g)
	}

	// Without Faugus there is nothing to start them with.
	cmd = exec.Command("python3", "-I", "-c", faugusScan)
	cmd.Env = []string{"POLYSEAT_HOME=" + home, "PATH=" + t.TempDir()}

	if out, _ := cmd.CombinedOutput(); strings.TrimSpace(string(out)) != "[]" {
		t.Errorf("without Faugus the scan printed %s", out)
	}
}

// The shortcut Faugus writes for a game and the entry Polyseat writes for it
// start the same thing, so the grid gets one of them.
func TestAFaugusShortcutIsTheSameGame(t *testing.T) {
	execs := "Exec=/usr/bin/faugus-launcher --game madison\nExec=steam steam://rungameid/1\n"

	if !alreadyListed(execs, "faugus-launcher --game madison") {
		t.Error("Faugus's own shortcut was not recognised")
	}

	if alreadyListed(execs, "faugus-launcher --game madison-2") {
		t.Error("another game of Faugus's was taken for this one")
	}

	if alreadyListed("Exec=/usr/bin/faugus-launcher %f\n", "faugus-launcher --game madison") {
		t.Error("Faugus itself was taken for one of its games")
	}
}

// A runner kept alive by a launcher in the tray blocks a move like an open
// Faugus does, and the refusal names the game it runs rather than a window
// nobody can find.
func TestFaugusHelperNamesTheRunningGame(t *testing.T) {
	home, path := faugusHome(t)
	writeFile(t, filepath.Join(home, "Games/Elden/elden.exe"), "MZ")
	writeFaugusGames(t, home,
		map[string]any{"gameid": "ea-app", "title": "EA App", "path": filepath.Join(home, "Faugus/ea/EALauncher.exe")},
		map[string]any{"gameid": "elden", "title": "Elden", "path": filepath.Join(home, "Games/Elden/elden.exe")})

	modules := t.TempDir()
	writeFile(t, filepath.Join(modules, "faugus/__init__.py"), "")
	writeFile(t, filepath.Join(modules, "faugus/runner.py"), "import time\ntime.sleep(60)\n")

	runner := exec.Command("python3", "-m", "faugus.runner", "--game", "ea-app")
	runner.Env = []string{"PYTHONPATH=" + modules}

	if err := runner.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = runner.Process.Kill()
		_ = runner.Wait()
	})

	res := runFaugusHelper(t, home, path, "move", filepath.Join(home, "Games"), filepath.Join(home, "Games/shared"), "elden")

	want := "Faugus is running EA App here; close it first, it would write the old place back"
	if res["error"] != want {
		t.Errorf("answered %v, want the error %q", res, want)
	}

	if _, err := os.Stat(filepath.Join(home, "Games/Elden/elden.exe")); err != nil {
		t.Errorf("the game was moved while the runner held the list: %v", err)
	}
}
