package seat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// lutrisBin builds a PATH holding exactly what the probe needs, so that a real
// Lutris on the machine running the tests cannot decide the answer.
func lutrisBin(t *testing.T, withLutris bool) string {
	t.Helper()

	dir := t.TempDir()

	for _, tool := range []string{"stat"} {
		found, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("SKIPPED: no %s, so the probe cannot be run here", tool)
		}

		if err := os.Symlink(found, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}

	if withLutris {
		// Never run by the probe. It only has to be findable.
		if err := os.WriteFile(filepath.Join(dir, "lutris"),
			[]byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

// probe runs the embedded shell of lutrisProbe the way the seat runs it.
func probe(t *testing.T, home, bin string) string {
	t.Helper()

	cmd := exec.Command("sh", "-c", lutrisProbe)
	cmd.Env = []string{"HOME=" + home, "PATH=" + bin}

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the probe failed: %v", err)
	}

	return strings.TrimSpace(string(out))
}

func database(t *testing.T, path string, size int, when time.Time) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

// A seat with no Lutris is the common case, and it has to cost nothing: this
// answer is what stops the scan from starting anything at all.
func TestTheProbeSaysSoWhenTheSeatHasNoLutris(t *testing.T) {
	if got := probe(t, t.TempDir(), lutrisBin(t, false)); got != lutrisAbsent {
		t.Errorf("the probe answered %q, want %q, so a seat without Lutris would be asked anyway",
			got, lutrisAbsent)
	}
}

// The whole point. Two reads with nothing happening in between have to agree,
// or the listing is taken again every minute and the stutter is back.
func TestTheProbeAnswersTheSameWhileNothingChanges(t *testing.T) {
	home, bin := t.TempDir(), lutrisBin(t, true)
	database(t, filepath.Join(home, ".local/share/lutris/pga.db"), 4096,
		time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC))

	first := probe(t, home, bin)
	second := probe(t, home, bin)

	if first != second {
		t.Errorf("two reads answered %q and %q, so nothing would ever be reused", first, second)
	}

	if first == lutrisAbsent || first == "" {
		t.Errorf("a seat with Lutris and a database answered %q", first)
	}
}

// And a Lutris that has never been run answers stably too, rather than falling
// through to "cannot tell" and being asked forever.
func TestTheProbeIsSteadyBeforeLutrisHasADatabase(t *testing.T) {
	home, bin := t.TempDir(), lutrisBin(t, true)

	first := probe(t, home, bin)

	if first == lutrisAbsent {
		t.Fatalf("Lutris is installed here, so %q is the wrong answer", first)
	}

	// Not empty, and this is the whole test rather than a detail of it. An
	// empty answer is how the caller says the seat could not be asked, and a
	// seat that cannot be asked is asked again a minute later.
	if first == "" {
		t.Error("a Lutris with no database answered with nothing, which reads as a failed read")
	}

	if second := probe(t, home, bin); first != second {
		t.Errorf("two reads answered %q and %q with no database in either", first, second)
	}
}

// A game added or removed changes the database, and that has to reach the
// answer, or the app list would freeze at whatever it said first.
func TestTheProbeNoticesTheDatabaseChanging(t *testing.T) {
	home, bin := t.TempDir(), lutrisBin(t, true)
	db := filepath.Join(home, ".local/share/lutris/pga.db")
	noon := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	database(t, db, 4096, noon)
	before := probe(t, home, bin)

	database(t, db, 4096, noon.Add(time.Minute))

	if after := probe(t, home, bin); after == before {
		t.Errorf("the database was written a minute later and the answer stayed %q", before)
	}

	database(t, db, 8192, noon.Add(time.Minute))

	if bigger := probe(t, home, bin); bigger == before {
		t.Errorf("the database doubled in size and the answer stayed %q", before)
	}
}

// Lutris is installed as a flatpak as often as not, and that keeps its data
// somewhere else entirely. Watching only the native path would mean never
// noticing a change in the installation the seat actually has.
func TestTheProbeSeesAFlatpakLutrisToo(t *testing.T) {
	home, bin := t.TempDir(), lutrisBin(t, true)
	empty := probe(t, home, bin)

	database(t, filepath.Join(home, ".var/app/net.lutris.Lutris/data/lutris/pga.db"),
		4096, time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC))

	if got := probe(t, home, bin); got == empty {
		t.Errorf("a flatpak Lutris database appeared and the answer stayed %q", empty)
	}
}

func TestTheListingIsKeptUntilLutrisChanges(t *testing.T) {
	memory := &lutrisMemory{}
	games := []Game{{Name: "Hades", Source: "lutris"}}

	if _, ok := memory.recall("a"); ok {
		t.Error("a memory that was never told anything answered as though it had been")
	}

	memory.remember("a", games)

	got, ok := memory.recall("a")
	if !ok || len(got) != 1 || got[0].Name != "Hades" {
		t.Errorf("the listing came back as %v, %v, want the one that was kept", got, ok)
	}

	if _, ok := memory.recall("b"); ok {
		t.Error("Lutris changed and the old listing was handed out anyway")
	}
}

// Nil is what a build gets, and a build has nothing to reuse. It must ask
// rather than panic.
func TestAScanWithNoMemoryBehindItAsksEveryTime(t *testing.T) {
	var memory *lutrisMemory

	if _, ok := memory.recall("a"); ok {
		t.Error("a scan with no memory behind it was told to reuse something")
	}

	memory.remember("a", []Game{{Name: "Hades"}})

	if _, ok := memory.recall("a"); ok {
		t.Error("something was kept in a memory that does not exist")
	}
}

// An empty listing is an answer: a Lutris with no games installed has none,
// and remembering that is what stops it being asked again a minute later.
func TestNoGamesIsRememberedAsAnAnswer(t *testing.T) {
	memory := &lutrisMemory{}
	memory.remember("a", nil)

	if _, ok := memory.recall("a"); !ok {
		t.Error("a Lutris with nothing installed would be started again every minute")
	}
}
