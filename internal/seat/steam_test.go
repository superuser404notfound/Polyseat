package seat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runSteamScript runs polyseat-steam against a stubbed Steam, and answers with
// what it started and what it said.
//
// The script is four decisions long and every one of them is about the process
// table, so it is run rather than transcribed: a Go copy of "does pgrep find
// one" would only prove the copy agrees with itself.
func runSteamScript(t *testing.T, alreadyRunning bool) (started, said string) {
	t.Helper()

	home := t.TempDir()

	script := filepath.Join(home, "polyseat-steam")
	if err := os.WriteFile(script, asset("assets/steam.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}

	stub := func(name, body string) {
		t.Helper()

		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// The one question the script asks about the world.
	code := "1"
	if alreadyRunning {
		code = "0"
	}

	stub("pgrep", "#!/bin/sh\nexit "+code+"\n")
	stub("setsid", "#!/bin/sh\nexec \"$@\"\n")

	// A screen to read the starting size off, in sway's own shape.
	stub("swaymsg", "#!/bin/sh\ncat <<'JSON'\n"+
		`[{"name":"HEADLESS-1","current_mode":{"width":2560,"height":1440,"refresh":60000}}]`+
		"\nJSON\n")

	// gamescope writes down how it was called and then runs what came after
	// the separator, so that the whole chain is exercised rather than just its
	// first link.
	stub("gamescope", "#!/bin/sh\n"+
		"echo \"$*\" > "+filepath.Join(home, "gamescope")+"\n"+
		"for a in \"$@\"; do shift; [ \"$a\" = -- ] && break; done\n"+
		"exec \"$@\"\n")

	// What Steam was started with, and whether the cap came with it.
	stub("steam", "#!/bin/sh\necho \"$* mangohud=$MANGOHUD\" > "+
		filepath.Join(home, "started")+"\n")

	// The real wrapper rather than a stub of it, because the thing being
	// checked is that these two files still agree about how a capped process
	// is started.
	capped := filepath.Join(bin, "polyseat-capped")
	if err := os.WriteFile(capped, asset("assets/capped.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/sh", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"),
		"POLYSEAT_CAPPED="+capped)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the script failed: %v\n%s", err, out)
	}

	// The script returns before Steam has started, on purpose: it is an exec
	// line in a session with other things to start. So the test waits for the
	// stub the way the session waits for Steam, which is not at all, and gives
	// up after a second rather than hanging a suite over a thing that failed.
	var body []byte

	for i := 0; i < 100; i++ {
		if b, err := os.ReadFile(filepath.Join(home, "started")); err == nil {
			body = b

			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	if b, err := os.ReadFile(filepath.Join(home, "gamescope")); err == nil {
		gamescopeArgs = strings.TrimSpace(string(b))
	} else {
		gamescopeArgs = ""
	}

	return strings.TrimSpace(string(body)), string(out)
}

// gamescopeArgs is how the last run called gamescope. A package level value
// rather than a third return, because only two of the tests below look at it
// and the rest read better without it.
var gamescopeArgs string

// Silent is the whole point of starting it early rather than late: a Steam that
// opens its window would put a store page on the screen of a seat nobody is
// sitting at, and Big Picture would cost twice the memory for a thing nobody
// asked to see.
func TestSteamIsStartedSilentlyInsideGamescope(t *testing.T) {
	started, _ := runSteamScript(t, false)

	if !strings.HasPrefix(started, "-silent") {
		t.Errorf("started steam %q, want -silent", started)
	}
}

// The in-game overlay does not work under rootless Xwayland at all, and -e is
// what makes gamescope carry it. Without the flag the overlay never appears;
// without gamescope it appears at one or two frames a second. steam.sh has the
// measurement.
func TestSteamRunsInsideGamescopeWithTheSteamIntegration(t *testing.T) {
	runSteamScript(t, false)

	for _, want := range []string{"-e", "--xwayland-count 2", "-f", "--backend wayland"} {
		if !strings.Contains(gamescopeArgs, want) {
			t.Errorf("gamescope was called without %q: %s", want, gamescopeArgs)
		}
	}
}

// The starting size is the screen as it is, not a number in a file: a seat
// streams to whatever a client asks for, and polyseat-resize changes the output
// under a running gamescope, which then follows it.
func TestSteamTakesTheScreenSizeFromSway(t *testing.T) {
	runSteamScript(t, false)

	for _, want := range []string{"-W 2560", "-H 1440", "-r 60"} {
		if !strings.Contains(gamescopeArgs, want) {
			t.Errorf("gamescope did not get %q from sway: %s", want, gamescopeArgs)
		}
	}
}

// The framerate cap reaches a game by inheritance and by nothing else: Sunshine
// sets it on what it launches, Steam is one of those, and every game Steam
// starts inherits it. A Steam the session starts instead has to pick the same
// variables up on the way, or every game launched out of it runs uncapped and
// nothing anywhere says so. That is what happened for an afternoon, which is
// why this is a test and not a comment.
func TestSteamIsStartedBehindTheCap(t *testing.T) {
	started, _ := runSteamScript(t, false)

	if !strings.Contains(started, "mangohud=1") {
		t.Errorf("Steam was started without the cap: %q", started)
	}
}

// The daemon calls this after closing one, and the session calls it at startup.
// Those two can overlap on a seat that is provisioned while it is running, and
// a second client against the same home directory does not come up as a second
// Steam: it hands the first one the arguments and leaves.
func TestSteamIsNotStartedTwice(t *testing.T) {
	started, said := runSteamScript(t, true)

	if started != "" {
		t.Errorf("started a second steam with %q", started)
	}

	if !strings.Contains(said, "already running") {
		t.Errorf("said %q, which does not say why nothing was started", said)
	}
}

// The session has to start the same script the daemon does, or the two disagree
// about what a started Steam is the moment either of them changes.
func TestTheSessionStartsSteamThroughTheScript(t *testing.T) {
	out, err := render("assets/sway.config", map[string]string{
		"Resolution": "1920x1080",
		"Keyboard":   Keyboard{}.swayInput(),
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(string(out), "exec "+steamScriptPath) {
		t.Errorf("the session does not start Steam:\n%s", out)
	}
}

// A pointer in a seat belongs to Sunshine or to the gamepad helper, and both
// leave it standing where they stopped. wlroots draws it into the picture
// Sunshine captures, so without a timeout it stays on somebody's television.
func TestTheSessionHidesAPointerNobodyIsUsing(t *testing.T) {
	out, err := render("assets/sway.config", map[string]string{
		"Resolution": "1920x1080",
		"Keyboard":   Keyboard{}.swayInput(),
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(string(out), "hide_cursor") {
		t.Errorf("the session never hides the pointer:\n%s", out)
	}
}
