package seat

import (
	"encoding/json"
	"strings"
	"testing"
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
