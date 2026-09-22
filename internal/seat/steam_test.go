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
func runSteamScript(t *testing.T, steamOutside bool) (started, said string) {
	t.Helper()

	return runSteamScriptWith(t, steamOutside, false, true)
}

// The Moonlight entry runs the script with this argument, and what it must not
// do is reach `steam steam://open/bigpicture` while there is no Steam: that
// command starts one, outside gamescope, where the overlay does not work. So
// the url has to come after the pair is up, and it has to come even when there
// was nothing to start.
func TestBigPictureIsAskedForAfterTheePairIsUp(t *testing.T) {
	_, said := runSteamScriptArg(t, "bigpicture", false, false, true)

	if !strings.Contains(said, "starting Steam in gamescope") {
		t.Errorf("the pair was not started first: %q", said)
	}

	if !strings.Contains(said, "opening Big Picture") {
		t.Errorf("Big Picture was never asked for: %q", said)
	}
}

// And when gamescope is already there, the argument is the whole of the work.
func TestBigPictureIsAskedForEvenWhenNothingHadToStart(t *testing.T) {
	_, said := runSteamScriptArg(t, "bigpicture", false, true, true)

	if !strings.Contains(said, "already running") {
		t.Errorf("something was started although gamescope was there: %q", said)
	}

	if !strings.Contains(said, "opening Big Picture") {
		t.Errorf("Big Picture was never asked for: %q", said)
	}
}

// runSteamScriptWith drives the script through the three states a seat can be
// in: nothing running, a Steam running outside gamescope, and a gamescope
// already there. The last argument is whether the gamescope it starts survives,
// which is the difference between the ordinary run and the retry.
func runSteamScriptWith(t *testing.T, steamOutside, gamescopeRunning, gamescopeSurvives bool) (started, said string) {
	t.Helper()

	return runSteamScriptArg(t, "", steamOutside, gamescopeRunning, gamescopeSurvives)
}

// runSteamScriptArg is the same with an argument, which is how the Moonlight
// entry calls it.
func runSteamScriptArg(t *testing.T, arg string, steamOutside, gamescopeRunning, gamescopeSurvives bool) (started, said string) {
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

	// Two files stand in for the process table, because the script asks about
	// two different processes and acts on the difference. A Steam outside
	// gamescope has to be closed; a gamescope that is there means there is
	// nothing to do at all.
	gsMarker := filepath.Join(home, "gamescope")
	steamMarker := filepath.Join(home, "steam-running")

	if gamescopeRunning {
		if err := os.WriteFile(gsMarker, []byte("running\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if steamOutside {
		if err := os.WriteFile(steamMarker, []byte("running\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	stub("pgrep", "#!/bin/sh\n"+
		"case \"$*\" in\n"+
		"*gamescope*) [ -f "+gsMarker+" ] && exit 0 || exit 1 ;;\n"+
		"*steam*) [ -f "+steamMarker+" ] && exit 0 || exit 1 ;;\n"+
		"esac\n"+
		"exit 1\n")

	stub("setsid", "#!/bin/sh\nexec \"$@\"\n")

	// A screen to read the starting size off, in sway's own shape.
	stub("swaymsg", "#!/bin/sh\ncat <<'JSON'\n"+
		`[{"name":"HEADLESS-1","current_mode":{"width":2560,"height":1440,"refresh":60000}}]`+
		"\nJSON\n")

	record := "echo \"$*\" > " + gsMarker + "\n"
	if !gamescopeSurvives {
		// Written somewhere pgrep does not look, so the script sees a
		// gamescope that is gone and has to decide what to do about it.
		record = "echo \"$*\" >> " + filepath.Join(home, "attempts") + "\n"
	}

	// gamescope writes down how it was called and then runs what came after
	// the separator, so that the whole chain is exercised rather than just its
	// first link.
	stub("gamescope", "#!/bin/sh\n"+record+
		"for a in \"$@\"; do shift; [ \"$a\" = -- ] && break; done\n"+
		"exec \"$@\"\n")

	// What Steam was started with, whether the cap came with it, and a
	// -shutdown that actually stops answering pgrep afterwards.
	stub("steam", "#!/bin/sh\n"+
		"case \"$1\" in -shutdown) rm -f "+steamMarker+"; exit 0 ;; esac\n"+
		"echo \"$* mangohud=$MANGOHUD\" > "+filepath.Join(home, "started")+"\n")

	// The real wrapper rather than a stub of it, because the thing being
	// checked is that these two files still agree about how a capped process
	// is started.
	capped := filepath.Join(bin, "polyseat-capped")
	if err := os.WriteFile(capped, asset("assets/capped.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	argv := []string{script}
	if arg != "" {
		argv = append(argv, arg)
	}

	cmd := exec.Command("/bin/sh", argv...)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"),
		"POLYSEAT_CAPPED="+capped,
		// The waits are what a seat needs and what a test has no patience
		// for. One round of each is enough to drive every branch.
		"POLYSEAT_SESSION_WAIT=2",
		"POLYSEAT_GAMESCOPE_SETTLE=0")

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

	if b, err := os.ReadFile(gsMarker); err == nil {
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
func TestNothingIsStartedWhenGamescopeIsAlreadyThere(t *testing.T) {
	started, said := runSteamScriptWith(t, false, true, true)

	if started != "" {
		t.Errorf("started a second steam with %q", started)
	}

	if !strings.Contains(said, "already running") {
		t.Errorf("said %q, which does not say why nothing was started", said)
	}
}

// And a Steam without a gamescope is the state that cost a morning. gamescope
// is started with setsid, so a session restart takes gamescope with it and
// leaves Steam behind, detached and talking to whatever X server it can find.
// A guard that asks about Steam then finds one and does nothing, and what is
// left is Steam outside gamescope: no in-game overlay, Big Picture in the
// corner. So this one has to be closed rather than counted as success.
func TestSteamOutsideGamescopeIsClosedAndStartedAgainInside(t *testing.T) {
	started, said := runSteamScript(t, true)

	if !strings.Contains(said, "outside gamescope") {
		t.Errorf("said %q, which does not say what was wrong", said)
	}

	if gamescopeArgs == "" {
		t.Error("gamescope was never started, so Steam stayed where it was")
	}

	if !strings.HasPrefix(started, "-silent") {
		t.Errorf("started steam %q, want -silent inside gamescope", started)
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

// A gamescope that does not survive the session start is the expensive failure,
// because what it leaves behind looks like success: Steam is running, so nothing
// retries, and it is running on the session's own X server instead of inside
// gamescope. No overlay, and Big Picture back in the corner. It happened once
// on a television, which is why the script now looks again.
func TestGamescopeIsTriedAgainWhenItDoesNotComeUp(t *testing.T) {
	_, said := runSteamScriptWith(t, false, false, false)

	if !strings.Contains(said, "did not come up") {
		t.Errorf("the script did not notice a gamescope that never started: %q", said)
	}

	if !strings.Contains(said, "trying once more") {
		t.Errorf("the script noticed but did not try again: %q", said)
	}
}
