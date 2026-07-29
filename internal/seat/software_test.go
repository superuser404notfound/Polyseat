package seat

import (
	"fmt"
	"strings"
	"testing"
)

// The catalog is a list of strings that are typed once and then shipped, and a
// typo in one of them produces a failure at install time in somebody else's
// seat rather than here. Every id was checked against Flathub by hand when it
// was added; this checks the part that can be checked without a network.
func TestCatalogIsUsable(t *testing.T) {
	if len(Catalog) == 0 {
		t.Fatal("the catalog is empty, so the software panel would offer nothing")
	}

	seen := map[string]bool{}

	for _, entry := range Catalog {
		if err := ValidateAppID(entry.ID); err != nil {
			t.Errorf("catalog entry %q: %v", entry.Name, err)
		}

		if seen[entry.ID] {
			t.Errorf("%s appears in the catalog twice", entry.ID)
		}

		seen[entry.ID] = true

		if entry.Name == "" {
			t.Errorf("%s has no name, so the row would be blank", entry.ID)
		}

		if entry.Summary == "" {
			t.Errorf("%s has no summary, so nobody can tell what it is", entry.ID)
		}
	}
}

// Steam, Lutris and Firefox are installed into every seat as packages. Offering
// them again as flatpaks would put a second copy of the same program in the
// list, several hundred megabytes per seat, and the sandboxed one is the worse
// of the two here because it has to be told separately that it may see the
// shared library.
func TestCatalogDoesNotOfferWhatIsAlreadyInstalled(t *testing.T) {
	native := map[string]string{
		"com.valvesoftware.Steam": "Steam",
		"net.lutris.Lutris":       "Lutris",
		"org.mozilla.firefox":     "Firefox",
	}

	for _, entry := range Catalog {
		if name, ok := native[entry.ID]; ok {
			t.Errorf("the catalog offers %s as a flatpak, but every seat has it as a package", name)
		}
	}
}

// Every launcher Polyseat can put in the Moonlight app list should be
// installable from the interface, or somebody has to find out its application
// id from somewhere else. The reverse does not hold: the catalog may carry
// things that are not launchers.
func TestEveryFlatpakLauncherCanBeInstalledFromTheInterface(t *testing.T) {
	offered := map[string]bool{}
	for _, entry := range Catalog {
		offered[entry.ID] = true
	}

	for _, l := range launchers {
		if l.Flatpak == "" {
			continue
		}

		// A launcher that ships as a package in every seat needs no flatpak
		// entry, because the app list will find the binary.
		if l.Binary == "lutris" {
			continue
		}

		if !offered[l.Flatpak] {
			t.Errorf("%s is recognised in the app list as %s but cannot be installed from the interface",
				l.Name, l.Flatpak)
		}
	}
}

// Real output, taken from a seat, for a search that returns a mixture of the
// well behaved and the not.
const flatpakSearchOutput = "X Minecraft Launcher\tPlay and manage Minecraft\tapp.xmcl.voxelum\n" +
	"Quadrant for Minecraft\tManage your mods and modpacks with ease\tdev.mrquantumoff.mcmodpackmanager\n" +
	"Minecraft\tCreate your own world in one of the most popular video games\tcom.mojang.Minecraft\n"

func TestParseSearchReadsWhatFlatpakPrints(t *testing.T) {
	found := parseSearch(flatpakSearchOutput)

	if len(found) != 3 {
		t.Fatalf("got %d results, want 3: %+v", len(found), found)
	}

	if found[2].ID != "com.mojang.Minecraft" {
		t.Errorf("third id is %q, want com.mojang.Minecraft", found[2].ID)
	}

	if found[2].Name != "Minecraft" {
		t.Errorf("third name is %q, want Minecraft", found[2].Name)
	}

	if found[0].Summary != "Play and manage Minecraft" {
		t.Errorf("first summary is %q", found[0].Summary)
	}
}

// A description is text somebody else wrote, and flatpak prints it into a tab
// separated line. Anything that does not come back as three columns with a
// usable id in the third has to be dropped: an entry whose id is really a
// fragment of prose would put an Install button on something that cannot be
// installed.
func TestParseSearchDropsWhatItCannotUse(t *testing.T) {
	messy := "Good App\tA description\tcom.example.Good\n" +
		"\n" +
		"Truncated line with no columns\n" +
		"Two\tcolumns only\n" +
		"Bad id\tdescription\tnot-reverse-dns\n" +
		"Nameless\tdescription\tcom.example.Nameless\n" +
		"   \t   \tcom.example.Blank\n"

	found := parseSearch(messy)

	ids := []string{}
	for _, entry := range found {
		ids = append(ids, entry.ID)
	}

	want := "com.example.Good|com.example.Nameless|com.example.Blank"
	if strings.Join(ids, "|") != want {
		t.Errorf("kept %v, want %s", ids, want)
	}

	for _, entry := range found {
		if entry.Name == "" {
			t.Errorf("%s came back with no name, so its row would be blank", entry.ID)
		}
	}
}

// A search for something common returns dozens, and a panel is not the place
// for all of them.
func TestParseSearchStopsAtALength(t *testing.T) {
	var b strings.Builder
	for i := 0; i < searchResults*3; i++ {
		fmt.Fprintf(&b, "App %d\tdescription\tcom.example.App%d\n", i, i)
	}

	if got := len(parseSearch(b.String())); got != searchResults {
		t.Errorf("kept %d results, want it capped at %d", got, searchResults)
	}
}
