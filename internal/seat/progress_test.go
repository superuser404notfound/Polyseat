package seat

import (
	"strings"
	"testing"
)

// value returns what the watcher last read, without going through a manager.
func (w *installWatch) value() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.seen
}

// The table flatpak prints before it starts, taken from a seat. The sizes are
// the whole reason for reading it: one small application pulled a 1.6 MB
// locale, a 384 MB locale and a 409 MB runtime, so counting the four steps
// equally would send the bar to a quarter and then leave it there.
const flatpakPlan = "        ID                           Branch  Op  Remote   Download\n" +
	" 1.     org.gnome.Calculator.Locale  stable  i   flathub    < 1.6 MB\n" +
	" 2.     org.gnome.Platform.Locale    50      i   flathub  < 384.8 MB\n" +
	" 3.     org.gnome.Platform           50      i   flathub  < 409.7 MB\n" +
	" 4.     org.gnome.Calculator         stable  i   flathub    < 1.8 MB\n"

func TestPlanSizesReadsTheTable(t *testing.T) {
	sizes := planSizes(flatpakPlan)

	if len(sizes) != 4 {
		t.Fatalf("read %d sizes, want 4: %v", len(sizes), sizes)
	}

	if sizes[0] != 1.6e6 || sizes[2] != 409.7e6 {
		t.Errorf("sizes are %v, want the megabytes from the table", sizes)
	}
}

// Anything it cannot read in full has to come back as nothing, so the caller
// falls back to counting steps. A bar weighted by half a table is wrong in a
// way nobody could explain.
func TestPlanSizesRefusesWhatItCannotRead(t *testing.T) {
	for label, text := range map[string]string{
		"no table":         "Looking for matches\nInstalling 1/2\n",
		"missing unit":     " 1.  org.example.App  stable  i  flathub  12.5\n",
		"unknown unit":     " 1.  org.example.App  stable  i  flathub  12.5 furlongs\n",
		"row out of order": " 1.  org.example.A  s  i  f  1.0 MB\n 3.  org.example.B  s  i  f  2.0 MB\n",
	} {
		if got := planSizes(text); got != nil {
			t.Errorf("%s: read %v, want nothing", label, got)
		}
	}
}

// Weighted by size: at the end of the second step, the two locales are done
// and they are 386 MB of the 797 MB total.
func TestOverallWeighsBySize(t *testing.T) {
	sizes := planSizes(flatpakPlan)

	got, ok := overall("Installing 2/4… 100%", sizes)
	if !ok {
		t.Fatal("no figure read")
	}

	if got < 46 || got > 51 {
		t.Errorf("overall is %d%%, want about 48%% after two of four steps by size", got)
	}
}

// Without a table it counts steps, which is coarse but never wrong about the
// direction.
func TestOverallCountsStepsWithoutATable(t *testing.T) {
	got, ok := overall("Installing 2/4… 50%", nil)
	if !ok {
		t.Fatal("no figure read")
	}

	if got != 37 {
		t.Errorf("overall is %d%%, want 37%% for half of the second of four", got)
	}
}

// The bug this replaced: the figure alone runs to a hundred and starts again
// for the next step, so a bar following it filled up once per step.
func TestOverallNeverReachesTheEndBeforeTheLastStep(t *testing.T) {
	sizes := planSizes(flatpakPlan)

	for _, line := range []string{
		"Installing 1/4… 100%",
		"Installing 2/4… 100%",
		"Installing 3/4… 100%",
	} {
		got, ok := overall(line, sizes)
		if !ok {
			t.Fatalf("%s: no figure read", line)
		}

		if got >= 100 {
			t.Errorf("%s: overall is %d%%, want it short of the end", line, got)
		}
	}

	if got, _ := overall("Installing 4/4… 100%", sizes); got != 100 {
		t.Errorf("the last step at 100%% gives %d%%, want 100", got)
	}
}

// What actually arrives: a table, then one line redrawn with carriage returns,
// wrapped in the escape sequences a terminal application writes.
func TestInstallWatchFollowsARealStream(t *testing.T) {
	w := &installWatch{}

	w.Write([]byte(flatpakPlan))
	w.Write([]byte("\x1b[?25l\x1b[6nInstalling 1/4… \x1b[2m \x1b[22m 0%\x1b]9;4;1;0\x1b\\"))
	w.Write([]byte("\rInstalling 3/4… \x1b[2m███\x1b[22m  50%\x1b]9;4;1;50\x1b\\"))

	// The two locales are done and the big runtime is half way, which by size
	// is 591 MB of 798.
	got := w.value()
	if got < 72 || got > 76 {
		t.Errorf("overall is %d%%, want about 74%% by size", got)
	}
}

// A line can be cut between two reads, which is why the watcher carries the end
// of one into the next.
func TestInstallWatchSurvivesALineSplitAcrossReads(t *testing.T) {
	w := &installWatch{}

	w.Write([]byte("\rInstalling 2/4"))
	w.Write([]byte("… 100%"))

	// No table was written, so this counts steps: two of four are done.
	if got := w.value(); got != 50 {
		t.Errorf("read %d, want 50 out of a line split across two writes", got)
	}
}

// Nothing that is not a step should move it. A version number or a byte count
// in the same output would otherwise send the bar somewhere random.
func TestInstallWatchIgnoresWhatIsNotAStep(t *testing.T) {
	w := &installWatch{}

	w.Write([]byte("\rInstalling 2/4… 50%"))
	before := w.value()

	w.Write([]byte("\nresolving org.gnome.Platform 48 stable 1234 kB 99%\n"))

	if got := w.value(); got != before {
		t.Errorf("read %d, want it left at %d", got, before)
	}
}

// The end of the output is what an error message is made of, and the carriage
// returns in it would otherwise put the whole progress bar on one line.
func TestInstallWatchKeepsAReadableTail(t *testing.T) {
	w := &installWatch{}

	w.Write([]byte("\rInstalling 1/2… 10%\rInstalling 1/2… 20%\r"))
	w.Write([]byte("error: Failed to install: no such ref\n"))

	tail := w.tail()
	if !strings.Contains(tail, "no such ref") {
		t.Errorf("the tail does not carry the error: %q", tail)
	}

	if strings.Contains(tail, "\r") {
		t.Errorf("the tail still has carriage returns in it: %q", tail)
	}
}
