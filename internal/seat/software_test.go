package seat

import "testing"

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
