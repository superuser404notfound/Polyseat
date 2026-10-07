package seat

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runFps runs the embedded script the way Sunshine runs it, against a home
// directory built for the test, and returns what MangoHud would read.
//
// The script itself rather than a Go transcription of it, because the thing
// that has to hold is what ends up in that file and a second implementation
// would only prove the two agree.
func runFps(t *testing.T, fps string, args ...string) (string, int) {
	t.Helper()

	conf, _, code := runFpsIn(t, t.TempDir(), fps, args...)

	return conf, code
}

// runFpsIn is the same against a home that already has a history, which is the
// only way to see a cap being taken off: run against an empty one, taking the
// cap off writes nothing, finds nothing, and passes while reporting failure.
// That is not a hypothetical, it is what the first version of both did.
func runFpsIn(t *testing.T, home, fps string, args ...string) (string, string, int) {
	t.Helper()

	script := filepath.Join(home, "polyseat-fps")
	if err := os.WriteFile(script, asset("assets/fps.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/sh", append([]string{script}, args...)...)
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}

	// What it says on the way out counts as much as what it writes. The script
	// reports every failure and returns zero regardless, so a run that quietly
	// gave up looks exactly like one that worked.
	var complaints strings.Builder
	cmd.Stderr = &complaints

	if fps != "" {
		cmd.Env = append(cmd.Env, "SUNSHINE_CLIENT_FPS="+fps)
	}

	code := 0

	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running the script failed: %v", err)
		}

		code = exit.ExitCode()
	}

	written, err := os.ReadFile(filepath.Join(home, ".config/MangoHud/MangoHud.conf"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", complaints.String(), code
		}

		t.Fatal(err)
	}

	return string(written), complaints.String(), code
}

// limit reads the cap back out of a MangoHud configuration, or "" for none.
func limit(conf string) string {
	for _, line := range strings.Split(conf, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "fps_limit="); ok {
			return rest
		}
	}

	return ""
}

func TestFpsCapsAtWhatTheClientAskedFor(t *testing.T) {
	for _, fps := range []string{"30", "60", "90", "120", "144"} {
		conf, code := runFps(t, fps)

		if code != 0 {
			t.Errorf("exit %d for %s fps, and a prep command that fails stops the stream", code, fps)
		}

		if got := limit(conf); got != fps {
			t.Errorf("client asked for %s fps, seat capped at %q", fps, got)
		}
	}
}

// The overlay is MangoHud's default and would otherwise be burned into the
// video, which is a surprise for somebody who only turned on a framerate cap.
func TestFpsLeavesNoOverlayOnTheStream(t *testing.T) {
	conf, _ := runFps(t, "60")

	if !strings.Contains(conf, "no_display=1") {
		t.Errorf("no_display is not set, so every game shows an overlay:\n%s", conf)
	}
}

// The cap decides how many frames are drawn, the present mode decides how old
// the one that goes out is: a FIFO swapchain queues frames and hands the stream
// the oldest one still in line, mailbox keeps only the newest.
func TestFpsAsksForTheFreshestFrameItCan(t *testing.T) {
	conf, _ := runFps(t, "60")

	if !strings.Contains(conf, "vulkan_present_mode=mailbox") {
		t.Errorf("the present mode is missing, so the cap costs a frame it does not have to:\n%s", conf)
	}
}

// And the limiter waits before the work rather than after it, which is fresher
// by up to one interval.
//
// This was "late" for one release, on the theory that "early" was what made the
// in-game Steam overlay stutter. The overlay stutters just as badly with no cap
// loaded at all, so the cap is not the cause and the latency is not worth
// paying. fps.sh has the measurement.
func TestFpsWaitsBeforeTheFrameRatherThanAfterIt(t *testing.T) {
	conf, _ := runFps(t, "60")

	if !strings.Contains(conf, "fps_limit_method=early") {
		t.Errorf("the limiter does not wait early, so every frame is older than it has to be:\n%s", conf)
	}
}

// Once the stream has stayed gone the daemon hands over the seat's own mode,
// the same string it hands polyseat-resize.
func TestFpsTakesTheRateOfAMode(t *testing.T) {
	for arg, want := range map[string]string{
		"1920x1080@60Hz":  "60",
		"3840x2160@120Hz": "120",
		"30":              "30",
	} {
		// A client's rate in the environment as well, which an argument has to
		// win over: this is what a hand run inside a prep command would see.
		conf, code := runFps(t, "144", arg)

		if code != 0 {
			t.Errorf("%s: exit %d", arg, code)
		}

		if got := limit(conf); got != want {
			t.Errorf("%s: capped at %q, want %s", arg, got, want)
		}
	}
}

// The cap is never taken off. MangoHud rereads this file in a running game, so
// a game somebody left open would lose its limit and render flat out with
// nobody watching, and mailbox, which a game keeps from the moment it created
// its swapchain, would not hold it back either.
//
// "off" is what this script used to be called with when a client left, and a
// Sunshine started before an upgrade goes on passing it until it is restarted.
func TestFpsKeepsTheCapWhenToldToTakeItOff(t *testing.T) {
	home := t.TempDir()

	before, _, _ := runFpsIn(t, home, "30")
	if limit(before) != "30" {
		t.Fatalf("the seat was not capped to begin with:\n%s", before)
	}

	for _, arg := range []string{"off", "0", "1920x1080", "@Hz"} {
		after, said, code := runFpsIn(t, home, "", arg)

		if code != 0 {
			t.Errorf("%s: exit %d", arg, code)
		}

		if after != before {
			t.Errorf("%s: the file changed:\n%s", arg, after)
		}

		if !strings.Contains(said, "leaving the cap as it is") {
			t.Errorf("%s: it did not say that it left the cap alone: %s", arg, strings.TrimSpace(said))
		}
	}
}

// Sunshine has been seen to leave the framerate out. Anything invented here
// would be wrong in the direction that shows: a cap below what the client can
// display looks like a seat that cannot keep up. So the cap that is there
// stays, and a seat that never had one gets none.
func TestFpsInventsNoCapWhenSunshineSaysNothing(t *testing.T) {
	for name, fps := range map[string]string{
		"nothing at all":     "",
		"zero":               "0",
		"not a number":       "sixty",
		"a rate with a unit": "60Hz",
	} {
		if conf, code := runFps(t, fps); code != 0 || conf != "" {
			t.Errorf("%s: exit %d in a seat with no cap, and wrote:\n%s", name, code, conf)
		}

		home := t.TempDir()
		before, _, _ := runFpsIn(t, home, "90")

		after, _, code := runFpsIn(t, home, fps)

		if code != 0 {
			t.Errorf("%s: exit %d, and a prep command that fails stops the stream", name, code)
		}

		if after != before || limit(after) != "90" {
			t.Errorf("%s: the cap of 90 became %q", name, limit(after))
		}
	}
}
