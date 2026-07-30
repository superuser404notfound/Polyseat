package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures under testdata are real files from a working Steam install, not
// handwritten ones, for the reason set out in testdata/README.md.
const (
	protonManifest = "testdata/appmanifest_1493710.acf"
	sharedManifest = "testdata/appmanifest_228980.acf"
)

func TestReadApp(t *testing.T) {
	app, err := ReadApp(protonManifest)
	if err != nil {
		t.Fatalf("ReadApp: %v", err)
	}

	for _, c := range []struct{ got, want, field string }{
		{app.AppID, "1493710", "appid"},
		{app.Name, "Proton Experimental", "name"},
		{app.InstallDir, "Proton - Experimental", "installdir"},
		{app.BuildID, "24407790", "buildid"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.field, c.got, c.want)
		}
	}

	if app.StateFlags != StateInstalled {
		t.Errorf("StateFlags = %d, want %d", app.StateFlags, StateInstalled)
	}

	if app.SizeOnDisk != 1514891505 {
		t.Errorf("SizeOnDisk = %d, want 1514891505", app.SizeOnDisk)
	}

	if !app.Installed() {
		t.Error("a fully installed app did not report itself as installed")
	}
}

// TestReadAppIgnoresNestedBlocks guards the trap this format sets.
//
// A manifest is nested, and InstalledDepots contains a key literally called
// "manifest" alongside per depot blocks. A parser that took the last value it
// saw for a key, or that did not track depth, would read fields out of those
// inner blocks. Here a decoy is planted inside InstalledDepots using the same
// names as the real fields, built by editing the real fixture rather than by
// writing a new one.
func TestReadAppIgnoresNestedBlocks(t *testing.T) {
	original, err := os.ReadFile(protonManifest)
	if err != nil {
		t.Fatal(err)
	}

	decoy := `	"InstalledDepots"
	{
		"9999999"
		{
			"name"		"WRONG"
			"installdir"		"../../../etc"
			"buildid"		"666"
			"appid"		"999"
			"StateFlags"		"1026"
		}`

	edited := strings.Replace(string(original), "\t\"InstalledDepots\"\n\t{", decoy, 1)
	if edited == string(original) {
		t.Fatal("the fixture no longer contains an InstalledDepots block, so this test tests nothing")
	}

	path := filepath.Join(t.TempDir(), "appmanifest_1493710.acf")
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	app, err := ReadApp(path)
	if err != nil {
		t.Fatalf("ReadApp: %v", err)
	}

	if app.Name != "Proton Experimental" {
		t.Errorf("name came out as %q, so a nested block was read as a top level field", app.Name)
	}

	if app.InstallDir != "Proton - Experimental" {
		t.Errorf("installdir came out as %q, which is a path traversal from a nested block", app.InstallDir)
	}

	if app.BuildID != "24407790" || app.AppID != "1493710" || app.StateFlags != StateInstalled {
		t.Errorf("nested values leaked into %+v", app)
	}
}

func TestReadApps(t *testing.T) {
	dir := t.TempDir()

	for _, fixture := range []string{protonManifest, sharedManifest} {
		data, err := os.ReadFile(fixture)
		if err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, filepath.Base(fixture)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Something that is not a manifest at all, because Steam leaves plenty of
	// other files in this directory and a scan that chokes on one of them would
	// stop the sync loop for good.
	if err := os.WriteFile(filepath.Join(dir, "appmanifest_broken.acf"), []byte("not a manifest"), 0o644); err != nil {
		t.Fatal(err)
	}

	apps, err := ReadApps(dir)
	if err != nil {
		t.Fatalf("ReadApps: %v", err)
	}

	if len(apps) != 2 {
		t.Fatalf("read %d apps, want 2: %+v", len(apps), apps)
	}

	// Sorted by name, so the interface does not reshuffle between refreshes.
	if apps[0].Name > apps[1].Name {
		t.Errorf("not sorted: %q before %q", apps[0].Name, apps[1].Name)
	}

	if apps, err := ReadApps(filepath.Join(dir, "does-not-exist")); err != nil || len(apps) != 0 {
		t.Errorf("scanning a missing directory gave %v, %v", apps, err)
	}
}

func TestRewrite(t *testing.T) {
	original, err := os.ReadFile(protonManifest)
	if err != nil {
		t.Fatal(err)
	}

	// The fixture has LastPlayed at zero, which would make the check below pass
	// without proving anything, so it is set to a real looking timestamp first.
	source := strings.Replace(string(original), `"LastPlayed"		"0"`, `"LastPlayed"		"1785198900"`, 1)
	if source == string(original) {
		t.Fatal("the fixture no longer has a LastPlayed field")
	}

	if !strings.Contains(source, `"LastOwner"		"76561197960287930"`) {
		t.Fatal("the fixture no longer has the LastOwner field this test is about")
	}

	got := string(Rewrite([]byte(source)))

	// LastOwner is the field that matters. Left as it was, every seat would
	// claim the account that first installed the game, and Steam uses it when
	// deciding whose license covers an install.
	if strings.Contains(got, "76561197960287930") {
		t.Error("LastOwner survived the rewrite")
	}

	if !strings.Contains(got, `"LastOwner"`) || !strings.Contains(got, `"LastOwner"		"0"`) {
		t.Error("LastOwner was removed rather than set to zero")
	}

	if strings.Contains(got, "1785198900") {
		t.Error("LastPlayed survived the rewrite")
	}

	// Everything else has to come through untouched. InstalledDepots is the
	// reason this rewrites line by line instead of regenerating the file from
	// the parsed fields: it carries the manifest id of every depot on disk, and
	// it is what lets the receiving client conclude the files are current
	// rather than download the game again.
	for _, want := range []string{
		`"InstalledDepots"`,
		`"1493711"`,
		`"manifest"		"2406473492863353429"`,
		`"size"		"1449233897"`,
		`"4862111"`,
		`"buildid"		"24407790"`,
		`"installdir"		"Proton - Experimental"`,
		`"StateFlags"		"4"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the rewrite lost %s", want)
		}
	}

	// The result still has to parse, and to parse the same way.
	before, err := parseManifest([]byte(source))
	if err != nil {
		t.Fatal(err)
	}

	after, err := parseManifest([]byte(got))
	if err != nil {
		t.Fatalf("the rewritten manifest does not parse: %v", err)
	}

	if before != after {
		t.Errorf("the rewrite changed the parsed fields:\n before %+v\n after  %+v", before, after)
	}
}

// TestRewriteLeavesNestedFieldsAlone makes sure the neutralising is anchored to
// the top level. A depot block that happened to contain a key called LastOwner
// must not be edited, because anything inside InstalledDepots is content Steam
// relies on byte for byte.
func TestRewriteLeavesNestedFieldsAlone(t *testing.T) {
	original, err := os.ReadFile(protonManifest)
	if err != nil {
		t.Fatal(err)
	}

	decoy := `	"InstalledDepots"
	{
		"9999999"
		{
			"LastOwner"		"12345"
		}`

	source := strings.Replace(string(original), "\t\"InstalledDepots\"\n\t{", decoy, 1)
	if source == string(original) {
		t.Fatal("the fixture no longer contains an InstalledDepots block")
	}

	got := string(Rewrite([]byte(source)))

	if !strings.Contains(got, `"LastOwner"		"12345"`) {
		t.Error("a LastOwner inside a nested block was rewritten")
	}

	if strings.Contains(got, "76561197960287930") {
		t.Error("the top level LastOwner was not rewritten")
	}
}

func TestParseManifestRejectsNonsense(t *testing.T) {
	for name, data := range map[string]string{
		"empty":                 "",
		"not a manifest":        "hello world\n",
		"no appid":              "\"AppState\"\n{\n\t\"name\"\t\t\"Thing\"\n}\n",
		"traversing installdir": "\"AppState\"\n{\n\t\"appid\"\t\t\"1\"\n\t\"installdir\"\t\t\"../escape\"\n}\n",
		"absolute installdir":   "\"AppState\"\n{\n\t\"appid\"\t\t\"1\"\n\t\"installdir\"\t\t\"/etc\"\n}\n",
		"appid with a slash":    "\"AppState\"\n{\n\t\"appid\"\t\t\"1/../..\"\n\t\"installdir\"\t\t\"Game\"\n}\n",
	} {
		if _, err := parseManifest([]byte(data)); err == nil {
			t.Errorf("parseManifest accepted %s", name)
		}
	}
}

// Dropping is what upgrades a seat that was built when the shared library was a
// second folder. The entry has to go, and everything else in the file has to
// survive it.
func TestDropLibraryFolder(t *testing.T) {
	original, err := os.ReadFile("testdata/libraryfolders-two.vdf")
	if err != nil {
		t.Fatal(err)
	}

	dropped, changed := DropLibraryFolder(original, "/home/player/games")
	if !changed {
		t.Fatal("dropping a folder that is in the file reported no change")
	}

	got := string(dropped)

	if strings.Contains(got, "/home/player/games") {
		t.Errorf("the folder is still there:\n%s", got)
	}

	for _, want := range []string{
		`"path"		"/home/player/.local/share/Steam"`,
		`"contentid"		"4713946985178202549"`,
		`"1493710"		"0"`,
		`"1562430"		"0"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dropping lost %s", want)
		}
	}

	if open, close := strings.Count(got, "{"), strings.Count(got, "}"); open != close {
		t.Errorf("%d opening braces against %d closing ones:\n%s", open, close, got)
	}

	// Nothing to do the second time.
	again, changed := DropLibraryFolder(dropped, "/home/player/games")
	if changed {
		t.Error("dropping a folder that is not there reported a change")
	}

	if string(again) != got {
		t.Error("the second drop altered the file")
	}
}

// The survivors have to stay contiguous. Steam stops reading at the first
// number it does not find, so a gap unregisters everything after it, and the
// entry after the shared library is by definition one somebody added by hand.
func TestDropLibraryFolderRenumbersWhatIsLeft(t *testing.T) {
	with, err := os.ReadFile("testdata/libraryfolders-three.vdf")
	if err != nil {
		t.Fatal(err)
	}

	dropped, changed := DropLibraryFolder(with, "/home/player/games")
	if !changed {
		t.Fatal("the middle entry was not dropped")
	}

	got := string(dropped)

	if !strings.Contains(got, "/mnt/somebodys-own-disk") {
		t.Fatalf("the hand added folder was lost:\n%s", got)
	}

	// It was entry 2 and has to have become entry 1, or Steam never reaches it.
	if strings.Contains(got, `"2"`) {
		t.Errorf("entry 2 survived, so there is a gap where entry 1 should be:\n%s", got)
	}

	if !strings.Contains(got, `"1"`) {
		t.Errorf("the folder after the dropped one was not renumbered to 1:\n%s", got)
	}
}

func TestDropLibraryFolderLeavesNonsenseAlone(t *testing.T) {
	// This file belongs to Steam. Where it is not the shape this understands,
	// writing a guess into it costs somebody their library list, so the only
	// safe answer is to leave it exactly as it was.
	for _, junk := range []string{"", "not a vdf at all\n", `"libraryfolders"`} {
		got, changed := DropLibraryFolder([]byte(junk), "/home/player/games")

		if changed {
			t.Errorf("DropLibraryFolder claimed to change %q", junk)
		}

		if string(got) != junk {
			t.Errorf("DropLibraryFolder altered %q into %q", junk, got)
		}
	}
}
