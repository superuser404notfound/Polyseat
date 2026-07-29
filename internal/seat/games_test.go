package seat

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The names on the left were read off a real seat; the rest are the ones that
// arrive with a game and would otherwise fill the app list.
func TestSteamToolRecognisesWhatIsNotAGame(t *testing.T) {
	tools := map[string]string{
		"1493710": "Proton Experimental",
		"4183110": "Steam Linux Runtime 4.0",
		"228980":  "Steamworks Common Redistributables",
		"2805730": "Proton 9.0",
		"2348590": "Proton 8.0",
		"1826330": "Proton EasyAntiCheat Runtime",
		"1161040": "Proton BattlEye Runtime",
		"1493711": "Proton Hotfix",
		"1070560": "Steam Linux Runtime 1.0 (scout)",
	}

	for appid, name := range tools {
		if !steamTool(appid, name) {
			t.Errorf("steamTool(%s, %q) = false, want it kept out of the app list", appid, name)
		}
	}
}

// The failure that matters. A tool shown by mistake is a wasted line in a menu;
// a game hidden by mistake is somebody unable to start their game with no way
// to find out why. So anything not clearly a tool has to come through.
func TestSteamToolNeverHidesAGame(t *testing.T) {
	games := map[string]string{
		"1562430": "DREDGE",
		"3751950": "Assassin's Creed Black Flag Resynced",
		"400":     "Portal",
		"322500":  "Proton Pulse",
		"558100":  "Protonwar",
		"1091500": "Cyberpunk 2077",
		"620":     "Portal 2",
		"105600":  "Terraria",
		"39120":   "Steamworld Dig",
	}

	for appid, name := range games {
		if steamTool(appid, name) {
			t.Errorf("steamTool(%s, %q) = true, so this game would vanish from Moonlight", appid, name)
		}
	}
}

// The same game can arrive twice, through Steam and again through Lutris
// pointed at a Steam installation. Two entries with the same name in Moonlight
// make it a coin toss which one runs.
func TestDedupeGamesKeepsTheFirstOfEachName(t *testing.T) {
	got := dedupeGames([]Game{
		{Name: "DREDGE", Launch: "steam steam://rungameid/1562430"},
		{Name: "dredge", Launch: "lutris lutris:rungameid/7"},
		{Name: "  DREDGE  ", Launch: "something else"},
		{Name: "", Launch: "nameless"},
		{Name: "Portal", Launch: "steam steam://rungameid/400"},
	})

	if len(got) != 2 {
		t.Fatalf("kept %d games, want 2: %+v", len(got), got)
	}

	if got[0].Launch != "steam steam://rungameid/1562430" {
		t.Errorf("kept %q for DREDGE, want the first one seen", got[0].Launch)
	}

	if got[1].Name != "Portal" {
		t.Errorf("second entry is %q, want Portal", got[1].Name)
	}
}

// runScan runs the embedded scan against a home directory built for the test.
//
// The script is Python inside a Go string, so the only alternative is checking
// it for the words it contains, and that is not a test: removing the sign in
// check while leaving the function that performs it went straight through one.
// This runs the thing itself.
func runScan(t *testing.T, home string) []map[string]string {
	t.Helper()

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("SKIPPED: no python3 to run the scan with, so its behaviour is unverified here")
	}

	cmd := exec.Command(python, "-c", steamScan)
	cmd.Env = append(os.Environ(), "POLYSEAT_HOME="+home)

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the scan failed: %v", err)
	}

	var found []map[string]string
	if err := json.Unmarshal(out, &found); err != nil {
		t.Fatalf("the scan printed something that is not a list: %v\n%s", err, out)
	}

	return found
}

// manifest writes an appmanifest the way Steam does.
func manifest(t *testing.T, dir, appid, name, state string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`"AppState"
{
	"appid"		"%s"
	"name"		"%s"
	"StateFlags"		"%s"
	"installdir"		"%s"
}
`, appid, name, state, name)

	if err := os.WriteFile(filepath.Join(dir, "appmanifest_"+appid+".acf"),
		[]byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func signIn(t *testing.T, home string) {
	t.Helper()

	dir := filepath.Join(home, ".local/share/Steam/config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	body := "\"users\"\n{\n\t\"76561198979087621\"\n\t{\n\t}\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "loginusers.vdf"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The reported case. The shared library puts a game's files into every seat
// that takes part, and files are not an account: a seat where nobody has ever
// signed in to Steam was offering its neighbour's games in Moonlight, where
// picking one did nothing at all.
func TestScanOffersNothingWhereNobodyHasSignedIn(t *testing.T) {
	home := t.TempDir()
	manifest(t, filepath.Join(home, "games/steamapps"), "3751950", "Some Shared Game", "4")

	if found := runScan(t, home); len(found) != 0 {
		t.Errorf("offered %d games in a seat with no Steam account: %v", len(found), found)
	}
}

func TestScanOffersGamesOnceSomebodyHas(t *testing.T) {
	home := t.TempDir()
	manifest(t, filepath.Join(home, "games/steamapps"), "3751950", "Some Shared Game", "4")
	signIn(t, home)

	found := runScan(t, home)
	if len(found) != 1 {
		t.Fatalf("offered %d games, want 1: %v", len(found), found)
	}

	if found[0]["name"] != "Some Shared Game" || found[0]["appid"] != "3751950" {
		t.Errorf("read %v, want the manifest that was written", found[0])
	}
}

// A title still downloading cannot start, so offering it is offering a dead
// entry. 4 is the state that means fully installed.
func TestScanSkipsWhatIsNotFullyInstalled(t *testing.T) {
	home := t.TempDir()
	signIn(t, home)
	manifest(t, filepath.Join(home, ".local/share/Steam/steamapps"), "1", "Half Downloaded", "1026")
	manifest(t, filepath.Join(home, ".local/share/Steam/steamapps"), "2", "Ready To Play", "4")

	found := runScan(t, home)
	if len(found) != 1 || found[0]["name"] != "Ready To Play" {
		t.Errorf("offered %v, want only the installed one", found)
	}
}

// Both libraries are read, and a title in both is one entry rather than two.
func TestScanReadsBothLibrariesWithoutDuplicating(t *testing.T) {
	home := t.TempDir()
	signIn(t, home)
	manifest(t, filepath.Join(home, ".local/share/Steam/steamapps"), "7", "In Both", "4")
	manifest(t, filepath.Join(home, "games/steamapps"), "7", "In Both", "4")
	manifest(t, filepath.Join(home, "games/steamapps"), "8", "Only Shared", "4")

	found := runScan(t, home)
	if len(found) != 2 {
		t.Errorf("offered %d entries, want 2 without a duplicate: %v", len(found), found)
	}
}

// cover writes a cached cover the way Steam does, in whichever layout.
func cover(t *testing.T, home, appid, rel string) string {
	t.Helper()

	path := filepath.Join(home, ".local/share/Steam/appcache/librarycache", appid, rel)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("not really a jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}

// Steam keeps a title's cover in two different places depending on when it was
// cached, and looking in only one of them is how a game with perfectly good
// artwork came out as a card with nothing but its name on it.
func TestScanFindsACoverInEitherLayout(t *testing.T) {
	for name, rel := range map[string]string{
		"the older layout": "library_600x900.jpg",
		"the newer one":    "36a1644b03afce1a648ab90b232196609e827539/library_capsule.jpg",
	} {
		home := t.TempDir()
		signIn(t, home)
		manifest(t, filepath.Join(home, ".local/share/Steam/steamapps"), "42", "A Game", "4")
		want := cover(t, home, "42", rel)

		found := runScan(t, home)
		if len(found) != 1 {
			t.Fatalf("%s: found %d games, want 1", name, len(found))
		}

		if found[0]["cover"] != want {
			t.Errorf("%s: cover is %q, want %q", name, found[0]["cover"], want)
		}
	}
}

// Anything that is not a picture must not be handed on as one. Steam keeps
// other things beside the artwork, and one of them being taken for a cover
// would put a broken card where a name would have done.
func TestScanIgnoresWhatIsNotAPicture(t *testing.T) {
	home := t.TempDir()
	signIn(t, home)
	manifest(t, filepath.Join(home, ".local/share/Steam/steamapps"), "42", "A Game", "4")
	cover(t, home, "42", "library_capsule.txt")
	cover(t, home, "42", "library_600x900.json")

	found := runScan(t, home)
	if len(found) != 1 {
		t.Fatalf("found %d games, want 1", len(found))
	}

	if found[0]["cover"] != "" {
		t.Errorf("cover is %q, want nothing", found[0]["cover"])
	}
}
