package seat

import (
	"strings"
	"testing"
)

// seen returns what the watcher last read, without going through a manager.
func (w *installWatch) value() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.seen
}

// flatpak redraws one line with carriage returns rather than printing new ones,
// so the figures arrive in a stream of overwrites. This is the shape of it.
func TestInstallWatchReadsTheLastFigure(t *testing.T) {
	w := &installWatch{}

	for _, chunk := range []string{
		"Installing app/org.gnome.Calculator/x86_64/stable\r",
		"\rInstalling… 0%",
		"\rInstalling… ▍   4%",
		"\rInstalling… ███ 47%",
		"\rInstalling… ████ 100%\n",
	} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	if got := w.value(); got != 100 {
		t.Errorf("last figure is %d, want 100", got)
	}
}

// A figure can be cut in half between two reads, which is the whole reason the
// watcher carries the end of one chunk into the next.
func TestInstallWatchSurvivesAFigureSplitAcrossReads(t *testing.T) {
	w := &installWatch{}

	w.Write([]byte("\rInstalling… 6"))
	w.Write([]byte("3% of something"))

	if got := w.value(); got != 63 {
		t.Errorf("read %d, want 63 out of a figure split across two writes", got)
	}
}

// Nothing that is not a percentage should move it, or a version number or a
// byte count in the output would send the bar somewhere random.
func TestInstallWatchIgnoresWhatIsNotAPercentage(t *testing.T) {
	w := &installWatch{}

	w.Write([]byte("\rInstalling… 40%"))
	w.Write([]byte("\nresolving org.gnome.Platform 48 stable 1234 kB\n"))

	if got := w.value(); got != 40 {
		t.Errorf("read %d, want it left at 40", got)
	}
}

// A figure outside the range is somebody else's number, not progress.
func TestInstallWatchRefusesAnImpossibleFigure(t *testing.T) {
	w := &installWatch{}

	w.Write([]byte("\rInstalling… 30%"))
	w.Write([]byte(" 999% "))

	if got := w.value(); got != 30 {
		t.Errorf("read %d, want it left at 30", got)
	}
}

// The end of the output is what an error message is made of, and the carriage
// returns in it would otherwise put the whole progress bar on one line.
func TestInstallWatchKeepsAReadableTail(t *testing.T) {
	w := &installWatch{}

	w.Write([]byte("\rInstalling… 10%\rInstalling… 20%\r"))
	w.Write([]byte("error: Failed to install: no such ref\n"))

	tail := w.tail()
	if !strings.Contains(tail, "no such ref") {
		t.Errorf("the tail does not carry the error: %q", tail)
	}

	if strings.Contains(tail, "\r") {
		t.Errorf("the tail still has carriage returns in it: %q", tail)
	}
}
