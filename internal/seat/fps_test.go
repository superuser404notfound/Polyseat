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

// A stream that has ended must leave the seat uncapped, otherwise the last
// client to connect decides the framerate of everything after it, including
// somebody playing on the seat directly.
func TestFpsOffRemovesTheCap(t *testing.T) {
	home := t.TempDir()

	if conf, _, _ := runFpsIn(t, home, "30"); limit(conf) != "30" {
		t.Fatalf("the seat was not capped to begin with:\n%s", conf)
	}

	conf, said, code := runFpsIn(t, home, "30", "off")

	if code != 0 {
		t.Errorf("exit %d", code)
	}

	if strings.Contains(said, "cannot") {
		t.Errorf("it gave up instead of taking the cap off: %s", strings.TrimSpace(said))
	}

	if got := limit(conf); got != "" {
		t.Errorf("cap is still %q after the stream ended", got)
	}
}

// Sunshine has been seen to leave the framerate out. Anything invented here
// would be wrong in the direction that shows: a cap below what the client can
// display looks like a seat that cannot keep up.
func TestFpsInventsNoCapWhenSunshineSaysNothing(t *testing.T) {
	for name, fps := range map[string]string{
		"nothing at all":     "",
		"zero":               "0",
		"not a number":       "sixty",
		"a rate with a unit": "60Hz",
	} {
		conf, code := runFps(t, fps)

		if code != 0 {
			t.Errorf("%s: exit %d, and a prep command that fails stops the stream", name, code)
		}

		if got := limit(conf); got != "" {
			t.Errorf("%s: capped at %q anyway", name, got)
		}
	}
}
