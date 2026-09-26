package seat

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// stockSunshineApps is the file Sunshine ships, taken verbatim from a seat
// before Polyseat generated this list. Every seat built before M7 has exactly
// this, which is what makes it the case worth testing against.
const stockSunshineApps = `{
  "env": {
    "PATH": "$(PATH):$(HOME)/.local/bin"
  },
  "apps": [
    {
      "name": "Desktop",
      "image-path": "desktop.png"
    },
    {
      "name": "Low Res Desktop",
      "image-path": "desktop.png",
      "prep-cmd": [
        {
          "do": "xrandr --output HDMI-1 --mode 1920x1080",
          "undo": "xrandr --output HDMI-1 --mode 1920x1200"
        }
      ]
    },
    {
      "name": "Steam Big Picture",
      "detached": [
        "setsid steam steam://open/bigpicture"
      ],
      "image-path": "steam.png"
    }
  ]
}`

// names reads the app names out of a merged list, in order.
func names(t *testing.T, list appList) []string {
	t.Helper()

	var out []string

	for _, raw := range list.Apps {
		var head struct {
			Name string `json:"name"`
		}

		if err := json.Unmarshal(raw, &head); err != nil {
			t.Fatalf("an entry in the merged list is not an object: %v", err)
		}

		out = append(out, head.Name)
	}

	return out
}

func ours() []app {
	return []app{
		{Name: "Desktop", ImagePath: "desktop.png", Polyseat: true},
		{Name: "Steam Big Picture", Polyseat: true,
			Detached: []string{"setsid steam steam://open/bigpicture"}},
	}
}

// The stock list has to converge, because every seat that already exists has
// it. Both of the entries Polyseat owns appear once, and the one that cannot
// work in a headless container is gone.
func TestMergeConvergesSunshinesOwnList(t *testing.T) {
	list, kept, err := mergeApps(ours(), []byte(stockSunshineApps))
	if err != nil {
		t.Fatalf("mergeApps: %v", err)
	}

	got := names(t, list)

	want := []string{"Desktop", "Steam Big Picture"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("merged list is %v, want %v", got, want)
	}

	if kept != 0 {
		t.Errorf("kept %d entries from the stock list, want 0", kept)
	}
}

// The point of merging rather than overwriting. Somebody who added an app
// through Sunshine's own web interface should not find it gone after the seat
// restarts, and the entry has to come back unchanged rather than reconstructed:
// Polyseat does not model most of what Sunshine understands, so anything it
// round trips through its own struct it would silently drop.
func TestMergeKeepsAnAppAddedByHand(t *testing.T) {
	// A file Polyseat has written at least once, which is what makes the
	// unmarked entry below recognisable as somebody else's.
	existing := `{
  "env": {},
  "apps": [
    {
      "name": "Desktop",
      "image-path": "desktop.png",
      "polyseat": true
    },
    {
      "name": "Minecraft",
      "cmd": "/usr/bin/minecraft",
      "exclude-global-prep-cmd": true,
      "wait-all": false,
      "image-path": "mc.png"
    }
  ]
}`

	list, kept, err := mergeApps(ours(), []byte(existing))
	if err != nil {
		t.Fatalf("mergeApps: %v", err)
	}

	if kept != 1 {
		t.Fatalf("kept %d entries, want 1", kept)
	}

	got := names(t, list)
	if got[len(got)-1] != "Minecraft" {
		t.Fatalf("the hand made entry is %q, want it kept as Minecraft", got[len(got)-1])
	}

	// The fields Polyseat has no struct field for have to survive, or merging
	// would be a slower way of losing them.
	var kept0 map[string]any
	if err := json.Unmarshal(list.Apps[len(list.Apps)-1], &kept0); err != nil {
		t.Fatalf("the kept entry is not an object: %v", err)
	}

	if kept0["exclude-global-prep-cmd"] != true {
		t.Errorf("exclude-global-prep-cmd did not survive the merge: %v", kept0)
	}

	if kept0["cmd"] != "/usr/bin/minecraft" {
		t.Errorf("cmd did not survive the merge: %v", kept0)
	}
}

// An entry whose name is one of ours is replaced, not kept alongside. Two
// entries called "Steam Big Picture" in Moonlight is the kind of thing that
// looks like it works until somebody picks the stale one.
func TestMergeNeverLeavesTwoEntriesWithTheSameName(t *testing.T) {
	existing := `{"env":{},"apps":[{"name":"Steam Big Picture","cmd":"something-old"}]}`

	list, _, err := mergeApps(ours(), []byte(existing))
	if err != nil {
		t.Fatalf("mergeApps: %v", err)
	}

	seen := map[string]int{}
	for _, name := range names(t, list) {
		seen[name]++
	}

	for name, n := range seen {
		if n > 1 {
			t.Errorf("%q appears %d times in the merged list", name, n)
		}
	}
}

// A seat with no file yet, and a seat whose file somebody broke, both have to
// end up with a usable list. Failing here would mean a seat that starts and
// then offers nothing to stream.
func TestMergeSurvivesAMissingOrBrokenFile(t *testing.T) {
	for label, existing := range map[string][]byte{
		"missing":   nil,
		"empty":     []byte(""),
		"garbage":   []byte("this is not json"),
		"truncated": []byte(`{"env":{},"apps":[{"name":"half`),
	} {
		list, kept, err := mergeApps(ours(), existing)
		if err != nil {
			t.Errorf("%s: mergeApps returned %v, want it to carry on", label, err)

			continue
		}

		if got := names(t, list); len(got) != 2 {
			t.Errorf("%s: merged list is %v, want both of Polyseat's entries", label, got)
		}

		if kept != 0 {
			t.Errorf("%s: kept %d entries out of an unusable file", label, kept)
		}
	}
}

// The list always carries the PATH that makes a flatpak launcher startable.
// Without it an entry for something the player installed is in the menu and
// fails when picked, which is worse than not offering it.
func TestMergeAlwaysSetsAPathThatReachesFlatpakApps(t *testing.T) {
	list, _, err := mergeApps(ours(), []byte(stockSunshineApps))
	if err != nil {
		t.Fatalf("mergeApps: %v", err)
	}

	path := list.Env["PATH"]
	if !strings.Contains(path, "flatpak/exports/bin") {
		t.Errorf("PATH is %q, want the flatpak export directory in it", path)
	}
}

func TestValidateAppID(t *testing.T) {
	valid := []string{
		"com.heroicgameslauncher.hgl",
		"net.lutris.Lutris",
		"org.prismlauncher.PrismLauncher",
		"io.itch.itch",
		"org.videolan.VLC",
		"com.valvesoftware.Steam.Utility.gamescope",
	}

	for _, id := range valid {
		if err := ValidateAppID(id); err != nil {
			t.Errorf("ValidateAppID(%q) = %v, want nil", id, err)
		}
	}

	invalid := map[string]string{
		"":                  "empty",
		"lutris":            "not reverse DNS, so not a flatpak id",
		"net.lutris":        "only two parts",
		"net.lutris.":       "trailing dot",
		".net.lutris.x":     "leading dot",
		"net..lutris":       "empty part",
		"1net.lutris.x":     "starts with a digit",
		"net.lutris.x y":    "space",
		"net.lutris.x;id":   "shell metacharacters",
		"../../etc/passwd":  "path traversal",
		"net.lutris.x/../y": "a separator, which would matter in the delete route",
	}

	for id, why := range invalid {
		if err := ValidateAppID(id); err == nil {
			t.Errorf("ValidateAppID(%q) = nil, want an error: %s", id, why)
		}
	}
}

// The bug that made the marker necessary, and the one somebody actually
// reported. A game is uninstalled inside the seat, so the generator stops
// producing its entry. Without a way to tell that entry from a hand made one it
// was preserved, and Moonlight went on offering something that started nothing.
func TestMergeDropsWhatPolyseatNoLongerGenerates(t *testing.T) {
	existing := `{
  "env": {},
  "apps": [
    {"name": "Desktop", "polyseat": true},
    {"name": "DREDGE", "detached": ["setsid steam steam://rungameid/1562430"], "polyseat": true},
    {"name": "Minecraft", "cmd": "/usr/bin/minecraft"}
  ]
}`

	list, kept, err := mergeApps(ours(), []byte(existing))
	if err != nil {
		t.Fatalf("mergeApps: %v", err)
	}

	for _, name := range names(t, list) {
		if name == "DREDGE" {
			t.Error("an uninstalled game stayed in the app list")
		}
	}

	if kept != 1 {
		t.Errorf("kept %d entries, want only the hand made one", kept)
	}
}

// A file from before the marker existed says nothing about who wrote what, and
// the entries Polyseat had generated are indistinguishable from somebody's own.
// Keeping none of them converges it in one write; keeping them would strand
// every stale entry a seat had accumulated.
func TestMergeConvergesAFileFromBeforeTheMarker(t *testing.T) {
	existing := `{
  "env": {},
  "apps": [
    {"name": "Desktop"},
    {"name": "DREDGE", "detached": ["setsid steam steam://rungameid/1562430"]},
    {"name": "Minecraft", "cmd": "/usr/bin/minecraft"}
  ]
}`

	list, kept, err := mergeApps(ours(), []byte(existing))
	if err != nil {
		t.Fatalf("mergeApps: %v", err)
	}

	got := names(t, list)
	if strings.Join(got, "|") != "Desktop|Steam Big Picture" {
		t.Errorf("merged list is %v, want only what is generated now", got)
	}

	if kept != 0 {
		t.Errorf("kept %d entries out of a file that predates the marker", kept)
	}
}

// Everything written carries the marker, or the next merge cannot tell its own
// work from anybody else's and the whole problem comes back.
//
// Against the real generator rather than the fixture above, because the marker
// was on the struct and missing from every entry it built: the file went out
// with "polyseat": false throughout and nothing noticed.
func TestEveryGeneratedEntryIsMarked(t *testing.T) {
	built, names := polyseatApps(
		[]installed{{Name: "Heroic", command: "flatpak run com.heroicgameslauncher.hgl"}},
		[]Game{{Name: "DREDGE", Launch: "steam steam://rungameid/1562430"}},
	)

	if len(built) != len(names) {
		t.Fatalf("built %d entries but reported %d names", len(built), len(names))
	}

	for _, a := range built {
		if !a.Polyseat {
			t.Errorf("%q was built without the marker", a.Name)
		}
	}

	list, _, err := mergeApps(built, nil)
	if err != nil {
		t.Fatalf("mergeApps: %v", err)
	}

	for _, raw := range list.Apps {
		var h struct {
			Name     string `json:"name"`
			Polyseat bool   `json:"polyseat"`
		}

		if err := json.Unmarshal(raw, &h); err != nil {
			t.Fatalf("an entry is not an object: %v", err)
		}

		if !h.Polyseat {
			t.Errorf("%q was written without the marker", h.Name)
		}
	}
}

// Sunshine rewrites this file in an arrangement of its own whenever anything
// is changed through its API, and the daemon asks it to do exactly that after
// every write. Comparing the two byte for byte would therefore find a
// difference on every pass and rewrite the same list back at it for ever.
func TestSameAppListIgnoresArrangement(t *testing.T) {
	mine := []byte(`{
  "env": {"PATH": "x"},
  "apps": [
    {"name": "Desktop", "image-path": "desktop.png", "polyseat": true},
    {"name": "Lutris", "detached": ["setsid lutris"], "polyseat": true}
  ]
}`)

	// The same thing after Sunshine has been through it: entries reordered,
	// keys reordered, whitespace different.
	theirs := []byte(`{"apps":[{"polyseat":true,"detached":["setsid lutris"],` +
		`"name":"Lutris"},{"image-path":"desktop.png","polyseat":true,` +
		`"name":"Desktop"}],"env":{"PATH":"x"}}`)

	if !sameAppList(mine, theirs) {
		t.Error("the same list in another arrangement was read as a change")
	}
}

// Everything that is not arrangement has to count, or the list would stop
// being kept up to date at all.
func TestSameAppListNoticesWhatMatters(t *testing.T) {
	base := `{"env":{"PATH":"x"},"apps":[` +
		`{"name":"Desktop","polyseat":true},` +
		`{"name":"DREDGE","detached":["setsid steam steam://rungameid/1"],"polyseat":true}]}`

	differs := map[string]string{
		"an entry gone":  `{"env":{"PATH":"x"},"apps":[{"name":"Desktop","polyseat":true}]}`,
		"an entry added": base[:len(base)-2] + `,{"name":"Extra"}]}`,
		"a command changed": `{"env":{"PATH":"x"},"apps":[` +
			`{"name":"Desktop","polyseat":true},` +
			`{"name":"DREDGE","detached":["setsid steam steam://rungameid/2"],"polyseat":true}]}`,
		"a card changed": `{"env":{"PATH":"x"},"apps":[` +
			`{"name":"Desktop","polyseat":true,"image-path":"a.png"},` +
			`{"name":"DREDGE","detached":["setsid steam steam://rungameid/1"],"polyseat":true}]}`,
		"the marker gone": `{"env":{"PATH":"x"},"apps":[` +
			`{"name":"Desktop"},` +
			`{"name":"DREDGE","detached":["setsid steam steam://rungameid/1"],"polyseat":true}]}`,
		"the path changed": `{"env":{"PATH":"y"},"apps":[` +
			`{"name":"Desktop","polyseat":true},` +
			`{"name":"DREDGE","detached":["setsid steam steam://rungameid/1"],"polyseat":true}]}`,
	}

	for what, other := range differs {
		if sameAppList([]byte(base), []byte(other)) {
			t.Errorf("%s was not read as a change", what)
		}
	}
}

// A file that cannot be read is a file worth replacing, so it must never
// compare equal to anything.
func TestSameAppListRefusesWhatItCannotRead(t *testing.T) {
	good := []byte(`{"env":{},"apps":[{"name":"Desktop"}]}`)

	for _, bad := range [][]byte{nil, []byte(""), []byte("not json"), []byte(`{"apps":`)} {
		if sameAppList(good, bad) {
			t.Errorf("%q compared equal to a real list", bad)
		}
	}
}

// Sunshine applies this block to the applications it starts, so it is where the
// framerate cap reaches a game. Losing it would cost nothing visible: the seat
// streams, the games run, and every one of them runs uncapped.
func TestAppListCarriesTheFramerateCapIntoWhatItStarts(t *testing.T) {
	list, _, err := mergeApps(nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if list.Env["MANGOHUD"] != "1" {
		t.Errorf("MANGOHUD is %q, so the Vulkan limiter never loads", list.Env["MANGOHUD"])
	}

	// $LIB is the dynamic linker's, and a shell that expanded it would leave a
	// path that exists for neither word length.
	preload := list.Env["LD_PRELOAD"]
	if !strings.Contains(preload, "libMangoHud_shim.so") || !strings.Contains(preload, "$LIB") {
		t.Errorf("LD_PRELOAD is %q, so OpenGL games run uncapped", preload)
	}

	// The reason the launchers are on PATH at all.
	if !strings.Contains(list.Env["PATH"], "flatpak/exports/bin") {
		t.Errorf("PATH is %q, so an installed launcher cannot be started", list.Env["PATH"])
	}
}

// The seat's own launcher shows the games too, and the entry has to survive a
// name that is not well behaved. A key in a desktop entry runs to the end of
// the line, so a newline in a title would not truncate the title, it would
// invent a key.
func TestDesktopEntryIsOneKeyPerLine(t *testing.T) {
	got := string(desktopEntry(Game{
		Name:   "Assassin's Creed\nExec=rm -rf /",
		Launch: "steam steam://rungameid/3751950",
		Image:  "/home/player/.local/share/polyseat/art/26df6f40.png",
	}))

	lines := strings.Split(strings.TrimSpace(got), "\n")

	seen := map[string]string{}

	for _, line := range lines[1:] {
		key, value, found := strings.Cut(line, "=")
		if !found {
			t.Fatalf("line %q is neither a key nor a value:\n%s", line, got)
		}

		if _, twice := seen[key]; twice {
			t.Errorf("%s appears twice, so a name smuggled in a key:\n%s", key, got)
		}

		seen[key] = value
	}

	// The command, behind the framerate cap. The cap is in the entry rather
	// than in the environment the launcher was started with, because the
	// desktop deliberately does not carry MangoHud's OpenGL shim: it kills
	// applications that are not games, Firefox among them.
	want := "/usr/local/bin/polyseat-capped steam steam://rungameid/3751950"

	if seen["Exec"] != want {
		t.Errorf("Exec is %q, want %q", seen["Exec"], want)
	}

	if strings.Contains(seen["Name"], "rm -rf") == false {
		t.Errorf("the name lost its text along with its newline: %q", seen["Name"])
	}

	if seen["Icon"] == "" {
		t.Error("no icon, so the launcher shows a blank square next to the game")
	}
}

// The grid inside the seat draws square icons, so a game entry wears the game's
// icon and falls back to its card only when there is no icon to be had.
//
// Getting this the wrong way round is what it looked like before: every game
// Polyseat wrote an entry for wore its portrait cover, drawn as a tall sliver
// between the square icons beside it, while a game somebody had asked Steam for
// a shortcut for wore a proper icon, because that entry is Steam's and not ours.
func TestDesktopEntryPrefersTheIconOverTheCard(t *testing.T) {
	game := Game{
		Name:   "DREDGE",
		Launch: "steam steam://rungameid/1562430",
		Image:  "/home/player/.local/share/polyseat/art/26df6f40.png",
		Icon:   "/home/player/.local/share/polyseat/icons/steam-1562430-581c0734.png",
	}

	got := string(desktopEntry(game))

	if !strings.Contains(got, "Icon="+game.Icon+"\n") {
		t.Errorf("the entry does not wear the game's icon:\n%s", got)
	}

	if strings.Contains(got, game.Image) {
		t.Errorf("the card reached the entry as well:\n%s", got)
	}

	// And with no icon found, the card is still better than the blank square a
	// launcher draws for an entry that names nothing.
	game.Icon = ""

	if !strings.Contains(string(desktopEntry(game)), "Icon="+game.Image+"\n") {
		t.Errorf("a game with no icon lost its picture entirely:\n%s", desktopEntry(game))
	}
}

// A game entry has to start from the launcher the seat actually has, with the
// cap intact, and that is tested by starting it the way the launcher does.
//
// nwg-drawer strips every quote from Exec, cuts it at the first %, and hands
// what is left to `/usr/bin/env -S`. GNU env refuses a bare $NAME in that
// string with exit 125, and the entries used to carry $LIB for the linker, so
// every game in the grid failed to start while Steam and Firefox beside it were
// fine. Reading the Exec line for a dollar sign would catch that one cause; this
// catches anything else about the line env will not take, and checks that the
// variables arrive as well, since an entry that starts the game uncapped would
// also pass a test that only looked at the exit code.
func TestDesktopEntryStartsTheWayTheLauncherRunsIt(t *testing.T) {
	envPath := "/usr/bin/env"
	if err := exec.Command(envPath, "-S", "true").Run(); err != nil {
		t.Skip("SKIPPED: this env has no -S, so the launcher's way of starting an entry cannot be reproduced here")
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")

	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}

	capped := filepath.Join(bin, "polyseat-capped")
	if err := os.WriteFile(capped, asset("assets/capped.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Stands in for Steam and records what it was started with.
	dump := filepath.Join(dir, "environment")
	steam := "#!/bin/sh\nenv > " + dump + "\necho \"$@\" >> " + dump + "\n"

	if err := os.WriteFile(filepath.Join(bin, "steam"), []byte(steam), 0o755); err != nil {
		t.Fatal(err)
	}

	var line string

	for _, l := range strings.Split(string(desktopEntry(Game{
		Name:   "DREDGE",
		Launch: "steam steam://rungameid/1562430",
		Image:  "/art/dredge.png",
	})), "\n") {
		if v, ok := strings.CutPrefix(l, "Exec="); ok {
			line = v
		}
	}

	if !strings.HasPrefix(line, cappedPath+" ") {
		t.Fatalf("Exec is %q, which does not start through %s", line, cappedPath)
	}

	// The seat's path for the script is swapped for the test's copy, and the
	// rest is done to the line exactly as nwg-drawer 0.7.5 does it in
	// parseDesktopEntry and launch.
	command := strings.NewReplacer(`"`, "", "'", "").Replace(capped + strings.TrimPrefix(line, cappedPath))

	if cut := strings.Index(command, "%"); cut > 0 {
		command = command[:cut-1]
	}

	cmd := exec.Command(envPath, "-S", command)
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + dir}

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the launcher could not start the entry: %v\n%s", err, out)
	}

	body, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal("the game was never started, so this checked nothing")
	}

	got := string(body)

	if !strings.Contains(got, "LD_PRELOAD="+mangoHudPreload+"\n") {
		t.Errorf("the game did not get LD_PRELOAD=%s, so an OpenGL game started from "+
			"the desktop runs uncapped:\n%s", mangoHudPreload, got)
	}

	if !strings.Contains(got, "MANGOHUD=1\n") {
		t.Errorf("the game did not get MANGOHUD=1, so a Vulkan game started from the desktop runs uncapped:\n%s", got)
	}

	if !strings.Contains(got, "steam://rungameid/1562430\n") {
		t.Errorf("the game was started without its own arguments:\n%s", got)
	}
}

// And the file name has to follow the contents, because that is what makes the
// minute timer cheap and what replaces an entry when its artwork improves.
func TestDesktopEntryChangesWithWhatItSays(t *testing.T) {
	game := Game{Name: "DREDGE", Launch: "steam steam://rungameid/1562430", Image: "/art/old.png"}

	before := desktopEntry(game)

	game.Image = "/art/new.png"

	after := desktopEntry(game)

	if string(before) == string(after) {
		t.Fatal("the entry did not change when the artwork did, so the file name would not either")
	}
}

// Somebody who asked Steam or Lutris for a shortcut has an entry of their own,
// and the seat's launcher lists every desktop entry it finds. Two files means
// two rows, so the generated one stands down.
func TestAGeneratedEntryStandsDownForOneSomebodyMade(t *testing.T) {
	// What Steam writes for a shortcut: its own name for the title, its own
	// path to the binary, and the same game underneath.
	theirs := "Exec=/usr/bin/steam steam://rungameid/1562430\n" +
		"Exec=lutris\n" +
		"Exec=firefox %u\n"

	if !alreadyListed(theirs, "steam steam://rungameid/1562430") {
		t.Error("the game would be listed twice, once by Steam and once by Polyseat")
	}

	// A different game is a different row and has to stay.
	if alreadyListed(theirs, "steam steam://rungameid/3751950") {
		t.Error("a game nothing else lists was skipped, so it would be in no menu at all")
	}

	// And a launcher's own entry must not swallow a game: they are different
	// things and the plain command names no game.
	if alreadyListed(theirs, "lutris lutris:rungameid/7") {
		t.Error("a Lutris game was mistaken for the Lutris entry itself")
	}
}

// The comparison is on what identifies the game. Anything short or wordlike
// would match by accident, and a game that matches by accident is a game that
// appears in no menu.
func TestLaunchTargetIsSomethingSpecificOrNothing(t *testing.T) {
	for launch, want := range map[string]string{
		"steam steam://rungameid/1562430": "steam://rungameid/1562430",
		"lutris lutris:rungameid/7":       "lutris:rungameid/7",
		"/opt/games/some-game/run.sh":     "/opt/games/some-game/run.sh",
		"lutris":                          "",
		"steam":                           "",
		"":                                "",
		"run":                             "",
	} {
		if got := launchTarget(launch); got != want {
			t.Errorf("launchTarget(%q) = %q, want %q", launch, got, want)
		}
	}
}

// A Big Picture that cannot be left is worse than one that has to be rebuilt.
// gamescope makes Steam offer "switch to desktop", Steam has no implementation
// of it outside SteamOS, and the interface then waits on that screen for ever.
// The way back is the pair of entries below: Desktop closes it, Steam Big
// Picture puts the whole arrangement back.
//
// The stream ending deliberately does not close it. Closing Big Picture takes
// Steam and gamescope with it, so the next connection found neither and started
// a cold Steam outside gamescope - which is the one place the overlay does not
// work. That was worse than the screen it was meant to guard against.
func TestBigPictureCanBeLeftAndComeBack(t *testing.T) {
	apps, _ := polyseatApps(nil, nil)

	var desktop, steam app

	for _, a := range apps {
		switch a.Name {
		case "Desktop":
			desktop = a
		case "Steam Big Picture":
			steam = a
		}
	}

	refreshes := false

	for _, d := range desktop.Detached {
		if strings.Contains(d, "polyseat-steam refresh") {
			refreshes = true
		}
	}

	if !refreshes {
		t.Error("picking Desktop does not refresh Big Picture, so a stuck screen stays stuck")
	}

	// And it may not simply close it: Big Picture is gamescope's only window,
	// so a closed one leaves the workspace empty and the next player switches
	// to a black screen and then waits for it to be built.
	for _, c := range desktop.PrepCmd {
		if strings.Contains(c.Do, "close/bigpicture") {
			t.Errorf("Desktop closes Big Picture without opening it again: %q", c.Do)
		}
	}

	for _, c := range steam.PrepCmd {
		if strings.Contains(c.Undo, "close/bigpicture") {
			t.Error("the stream ending closes Big Picture, which takes Steam and gamescope with it")
		}
	}

	// And what puts it back may not be the bare steam command: with no Steam
	// running that starts one outside gamescope.
	opens := false

	for _, d := range steam.Detached {
		// With the argument, because the bare script only starts Steam now. The
		// session leaves it silent so that an idle seat does not hold Big
		// Picture's 218 MB of video memory, which makes this entry the one place
		// that asks for the window.
		if strings.Contains(d, "polyseat-steam bigpicture") {
			opens = true
		}

		if strings.Contains(d, "steam://open/bigpicture") {
			t.Errorf("Big Picture is opened directly, which can start a Steam outside gamescope: %q", d)
		}
	}

	if !opens {
		t.Error("nothing asks for Big Picture, so picking it does nothing")
	}
}

// The switch between the two workspaces has to be the first thing each entry
// does. Sunshine runs these in order while the stream is already running, so
// anything before it is time the player spends looking at the wrong screen -
// three to five seconds of the desktop when Steam was what they picked.
func TestTheWorkspaceSwitchComesFirst(t *testing.T) {
	apps, _ := polyseatApps(nil, nil)

	for _, a := range apps {
		if a.Name != "Desktop" && a.Name != "Steam Big Picture" {
			continue
		}

		if len(a.PrepCmd) == 0 {
			t.Errorf("%s has no prep commands at all", a.Name)

			continue
		}

		if !strings.Contains(a.PrepCmd[0].Do, "polyseat-workspace") {
			t.Errorf("%s does something before it switches workspace: %q",
				a.Name, a.PrepCmd[0].Do)
		}
	}
}

// inSeat is what a scan's argv runs once sudo has become the player: the part
// from timeout onward, which is what these tests run on this machine as
// whoever runs them. The prefix is checked rather than skipped blindly, so a
// change to it is noticed here and not only in a seat.
func inSeat(t *testing.T, argv []string) []string {
	t.Helper()

	want := []string{"sudo", "-u", Player, "env", "HOME=/home/" + Player}
	if len(argv) < len(want) || strings.Join(argv[:len(want)], " ") != strings.Join(want, " ") {
		t.Fatalf("a scan does not start by becoming the player: %q", argv)
	}

	return argv[len(want):]
}

// A scan runs as the player with the player's HOME, and Python reads that
// HOME for code to run before the scan's first line. -I is what keeps it out,
// and this asks the real interpreter with a real usercustomize.py in place.
func TestPythonScanLeavesTheUsersSiteAlone(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}

	home := t.TempDir()
	env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}

	where := exec.Command(python, "-c", "import site; print(site.getusersitepackages())")
	where.Env = env

	out, err := where.Output()
	if err != nil {
		t.Fatal(err)
	}

	site := strings.TrimSpace(string(out))
	if err := os.MkdirAll(site, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(site, "usercustomize.py"),
		[]byte("print('the player was here')\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Without -I the file is read, or this test would prove nothing about the
	// flag: a Python that ignores the user's site anyway passes either way.
	plain := exec.Command(python, "-c", "print('scan')")
	plain.Env = env

	if out, _ := plain.CombinedOutput(); !strings.Contains(string(out), "the player was here") {
		t.Skipf("this Python does not read the user's site even without -I: %q", out)
	}

	argv := inSeat(t, pythonScan(10*time.Second, "print('scan')"))

	scan := exec.Command(argv[0], argv[1:]...)
	scan.Env = env

	got, err := scan.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, got)
	}

	if strings.TrimSpace(string(got)) != "scan" {
		t.Errorf("the player's usercustomize.py ran before the scan: %q", got)
	}
}

// A scan that never returns has to be ended by the seat, because Incus does not
// end a command whose caller stopped waiting. A FIFO nobody writes to is the
// way a player makes a read never return.
func TestScanIsEndedInsideTheSeat(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "appmanifest_1.acf")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}

	argv := inSeat(t, asPlayerFor(time.Second, "sh", "-c", `cat "$1"`, "sh", fifo))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	start := time.Now()

	scan := exec.CommandContext(ctx, argv[0], argv[1:]...)
	scan.WaitDelay = time.Second

	err := scan.Run()

	if ctx.Err() != nil {
		t.Fatal("the scan was still waiting on the FIFO when the test gave up")
	}

	if err == nil {
		t.Error("a scan that was ended reported success")
	}

	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("the scan took %s to be ended", took)
	}
}

// The desktop entry scan reads directories the player writes to, so a FIFO in
// one of them must not hold it up. The symlinks are there because the flatpak
// exports are symlinks, and those have to go on being read.
func TestForeignScanSkipsWhatIsNotAFile(t *testing.T) {
	dir := t.TempDir()

	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("steam.desktop", "[Desktop Entry]\nExec=steam steam://rungameid/440\n")
	write("real.desktop", "[Desktop Entry]\nExec=lutris lutris:rungame/quake\n")
	write(entryPrefix+"abc.desktop", "[Desktop Entry]\nExec=ours\n")

	if err := os.Symlink("real.desktop", filepath.Join(dir, "exported.desktop")); err != nil {
		t.Fatal(err)
	}

	if err := syscall.Mkfifo(filepath.Join(dir, "trap.desktop"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink("trap.desktop", filepath.Join(dir, "trap-link.desktop")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// WaitDelay, so that a scan which does wait makes this fail rather than
	// hang: grep blocked on the FIFO keeps the pipe open after sh is killed.
	scan := exec.CommandContext(ctx, "sh", "-c", foreignScan([]string{dir}))
	scan.WaitDelay = time.Second

	out, _ := scan.Output()

	if ctx.Err() != nil {
		t.Fatal("the scan waited on a FIFO")
	}

	got := string(out)

	for _, want := range []string{"steam://rungameid/440", "lutris:rungame/quake"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s is missing from %q", want, got)
		}
	}

	if strings.Count(got, "lutris:rungame/quake") != 2 {
		t.Errorf("the entry behind a symlink was not read: %q", got)
	}

	if strings.Contains(got, "Exec=ours") {
		t.Errorf("an entry of ours was taken for somebody else's: %q", got)
	}
}

// A seat decides how much it prints. Past the limit the answer is refused, and
// the writes still succeed, because a failed write would stop the Incus client
// reading and leave the command in the seat blocked on a full pipe.
func TestCappedBuffer(t *testing.T) {
	b := &cappedBuffer{limit: 8}

	if n, err := b.Write([]byte("12345")); n != 5 || err != nil {
		t.Fatalf("write under the limit: %d, %v", n, err)
	}

	if b.over() {
		t.Fatal("under the limit was reported over it")
	}

	if n, err := b.Write([]byte("67890")); n != 5 || err != nil {
		t.Fatalf("a write past the limit failed: %d, %v", n, err)
	}

	if !b.over() {
		t.Error("going past the limit was not noticed")
	}

	if got := b.String(); got != "12345678" {
		t.Errorf("kept %q", got)
	}
}
