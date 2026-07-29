package seat

import (
	"strings"
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

// The scan runs in the seat, so the script itself is the part that cannot be
// covered by a Go test. What can be checked is that it still looks for both
// library directories and still insists on the fully installed state, because
// dropping either would be silent: the app list would simply be shorter, or it
// would offer titles that are still downloading.
func TestSteamScanLooksWhereGamesActuallyAre(t *testing.T) {
	for _, want := range []string{
		"/home/player/.local/share/Steam/steamapps",
		"/home/player/games/steamapps",
		"appmanifest_*.acf",
		"librarycache",
		`state != "4"`,
	} {
		if !strings.Contains(steamScan, want) {
			t.Errorf("the scan no longer mentions %s", want)
		}
	}
}
