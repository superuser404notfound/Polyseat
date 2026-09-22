package seat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// seatState is the seat polyseat-steam is run against.
//
// A struct rather than a row of booleans because there are now five of them and
// a call site reading (t, false, false, true, false) says nothing about which
// state it means. Every zero value is the healthy seat: nothing running, and a
// gamescope that comes up when it is started.
type seatState struct {
	// arg is what the Moonlight entry passes, if anything.
	arg string

	// steamOutside is a Steam left running with no gamescope around it, which
	// is what a session restart used to leave behind.
	steamOutside bool

	// gamescopeRunning is this session's own gamescope, already up.
	gamescopeRunning bool

	// staleGamescope is a gamescope from a session that is gone: still in the
	// process table, with no compositor left to draw into.
	staleGamescope bool

	// gamescopeDies is a gamescope that does not survive being started, which
	// is the difference between the ordinary run and the retry.
	gamescopeDies bool

	// bigPictureUp is a window already on the screen, which is what a seat
	// whose autostart did its work looks like when somebody picks Steam in
	// Moonlight.
	bigPictureUp bool

	// dropsFirstRequest is a Steam that is running but not yet listening on its
	// pipe. It does not queue a url that arrives then, it loses it, and asking
	// once is what left seats with a Steam and no Big Picture.
	dropsFirstRequest bool
}

// A Steam that is running is not a Big Picture that is ready: with -silent
// there is no window until somebody asks, and building it is the wait the
// autostart exists to remove. So the script opens it itself, after the pair is
// up - never before, because `steam steam://open/bigpicture` with no Steam
// running starts one outside gamescope.
func TestBigPictureIsOpenedAfterThePairIsUp(t *testing.T) {
	_, said := runSteamScript(t, seatState{})

	if !strings.Contains(said, "starting Steam in gamescope") {
		t.Errorf("the pair was not started first: %q", said)
	}

	if !strings.Contains(said, "opening Big Picture") {
		t.Errorf("Big Picture was never asked for: %q", said)
	}
}

// And when gamescope is already there, asking for the window is the whole of
// the work, which is what makes picking it in Moonlight immediate.
func TestBigPictureIsOpenedEvenWhenNothingHadToStart(t *testing.T) {
	_, said := runSteamScript(t, seatState{gamescopeRunning: true})

	if !strings.Contains(said, "already running") {
		t.Errorf("something was started although gamescope was there: %q", said)
	}

	if !strings.Contains(said, "opening Big Picture") {
		t.Errorf("Big Picture was never asked for: %q", said)
	}
}

// runSteamScript runs polyseat-steam against a stubbed Steam and a real process
// table, and answers with what it started and what it said.
//
// The script is five decisions long and every one of them is about processes,
// so it is run rather than transcribed: a Go copy of "does pgrep find one"
// would only prove the copy agrees with itself.
func runSteamScript(t *testing.T, state seatState) (started, said string) {
	t.Helper()

	// Not t.TempDir(), and that is not a preference. The script backgrounds
	// gamescope on purpose and returns before it is finished, so the stubs
	// keep writing here for a moment after the test has its answer. t.TempDir
	// treats a directory that grows while it is being removed as a failure,
	// which turned this into a test that passed or failed depending on the
	// machine's mood - once in CI, on a commit that had passed on the tag.
	// Removing it ourselves keeps the tidying and drops the verdict.
	home, err := os.MkdirTemp("", "polyseat-steam")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(home) })

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

	if state.gamescopeRunning {
		if err := os.WriteFile(gsMarker, []byte("running\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if state.steamOutside {
		if err := os.WriteFile(steamMarker, []byte("running\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Real processes, because the script tells its own gamescope from a
	// leftover by their start times in /proc, and because taking a leftover
	// away is a kill it has to be able to make. A pid that belongs to nothing
	// has no start time, and a stub that made one up would be testing the stub.
	//
	// Started oldest first, with a pause between them, so that the order is the
	// one a seat has: the leftover predates sway, and this session's gamescope
	// comes after it.
	helper := func() int {
		t.Helper()

		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		// Waited on in the background so that a killed one is reaped rather
		// than left as a zombie. A zombie still answers kill -0, and the script
		// would then spend ten seconds waiting for a process that is already
		// dead to go away.
		go func() { _ = cmd.Wait() }()

		t.Cleanup(func() { _ = cmd.Process.Kill() })

		return cmd.Process.Pid
	}

	stale := ""
	stalePid = 0

	if state.staleGamescope {
		stalePid = helper()
		stale = strconv.Itoa(stalePid)

		time.Sleep(40 * time.Millisecond)
	}

	sway := strconv.Itoa(helper())

	time.Sleep(40 * time.Millisecond)

	gamescope := strconv.Itoa(helper())

	// The process table, in three answers. A gamescope that is there answers
	// with a pid younger than sway's; a leftover answers with an older one, and
	// only for as long as it is actually alive, so that the script's kill is
	// what makes it stop answering.
	stub("pgrep", "#!/bin/sh\n"+
		"case \"$*\" in\n"+
		"*gamescope*)\n"+
		"  [ -f "+gsMarker+" ] && { echo "+gamescope+"; exit 0; }\n"+
		"  [ -n \""+stale+"\" ] && kill -0 "+stale+" 2>/dev/null && { echo "+stale+"; exit 0; }\n"+
		"  exit 1 ;;\n"+
		"*sway*) echo "+sway+"; exit 0 ;;\n"+
		"*steam*) [ -f "+steamMarker+" ] && exit 0 || exit 1 ;;\n"+
		"esac\n"+
		"exit 1\n")

	stub("setsid", "#!/bin/sh\nexec \"$@\"\n")

	// A screen to read the starting size off, in sway's own shape, and a window
	// tree that grows a gamescope window when, and only when, Big Picture has
	// opened. That is how a seat behaves: gamescope is started with a silent
	// Steam, which has no window, so gamescope has nothing to present and sway
	// has nothing to show until a url is answered. The script reads the tree to
	// find out whether what it asked for happened, so a stub that showed a
	// window from the start would be testing nothing.
	url := filepath.Join(home, "url")

	stub("swaymsg", "#!/bin/sh\n"+
		"case \"$*\" in\n"+
		"*get_tree*)\n"+
		"  if [ -f "+url+" ]; then\n"+
		"    echo '{\"nodes\": [{\"app_id\": \"gamescope\"}]}'\n"+
		"  else\n"+
		"    echo '{\"nodes\": []}'\n"+
		"  fi\n"+
		"  exit 0 ;;\n"+
		"esac\n"+
		"cat <<'JSON'\n"+
		`[{"name":"HEADLESS-1","current_mode":{"width":2560,"height":1440,"refresh":60000}}]`+
		"\nJSON\n")

	if state.bigPictureUp {
		if err := os.WriteFile(url, []byte("steam://open/bigpicture\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	record := "echo \"$*\" > " + gsMarker + "\n"
	if state.gamescopeDies {
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
	// How many requests it takes before one is answered. One, unless the test is
	// the seat that reported this: a Steam whose pipe is not listening yet
	// throws the url away, and every request before that is simply lost.
	answers := 1
	if state.dropsFirstRequest {
		answers = 2
	}

	stub("steam", "#!/bin/sh\n"+
		"case \"$1\" in\n"+
		"-shutdown) rm -f "+steamMarker+"; exit 0 ;;\n"+
		"steam://*)\n"+
		"  echo \"$1\" >> "+filepath.Join(home, "requests")+"\n"+
		"  [ \"$(wc -l < "+filepath.Join(home, "requests")+")\" -ge "+strconv.Itoa(answers)+" ] &&\n"+
		"    echo \"$1\" > "+url+"\n"+
		"  exit 0 ;;\n"+
		"esac\n"+
		"echo \"$* mangohud=$MANGOHUD\" > "+filepath.Join(home, "started")+"\n"+
		"touch "+steamMarker+"\n")

	// The real wrapper rather than a stub of it, because the thing being
	// checked is that these two files still agree about how a capped process
	// is started.
	capped := filepath.Join(bin, "polyseat-capped")
	if err := os.WriteFile(capped, asset("assets/capped.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	argv := []string{script}
	if state.arg != "" {
		argv = append(argv, state.arg)
	}

	cmd := exec.Command("/bin/sh", argv...)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"),
		"POLYSEAT_CAPPED="+capped,
		// The waits are what a seat needs and what a test has no patience
		// for. One round of each is enough to drive every branch.
		"POLYSEAT_SESSION_WAIT=2",
		"POLYSEAT_GAMESCOPE_SETTLE=0",
		// Long enough for a second request to be made and answered, which is
		// the thing being checked, and short enough that a seat that never
		// opens one does not hold the suite for a minute and a half.
		"POLYSEAT_BIGPICTURE_WAIT=12")

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

	requests = nil

	if b, err := os.ReadFile(filepath.Join(home, "requests")); err == nil {
		requests = strings.Fields(string(b))
	}

	return strings.TrimSpace(string(body)), string(out)
}

// gamescopeArgs is how the last run called gamescope. A package level value
// rather than a third return, because only two of the tests below look at it
// and the rest read better without it.
var gamescopeArgs string

// requests is every url the last run asked Steam for. Counted because asking
// once and hoping is the bug this file now holds the script to.
var requests []string

// stalePid is the process the last run offered as a gamescope left behind by a
// session that is gone. Here for the same reason as gamescopeArgs: one test
// looks at whether it survived, and the others read better without it.
var stalePid int

// gone answers whether that process is really gone, rather than whether the
// script said so.
func gone(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return true
	}

	return p.Signal(syscall.Signal(0)) != nil
}

// Silent is the whole point of starting it early rather than late: a Steam that
// opens its window would put a store page on the screen of a seat nobody is
// sitting at, and Big Picture would cost twice the memory for a thing nobody
// asked to see.
func TestSteamIsStartedSilentlyInsideGamescope(t *testing.T) {
	started, _ := runSteamScript(t, seatState{})

	if !strings.HasPrefix(started, "-silent") {
		t.Errorf("started steam %q, want -silent", started)
	}
}

// The in-game overlay does not work under rootless Xwayland at all, and -e is
// what makes gamescope carry it. Without the flag the overlay never appears;
// without gamescope it appears at one or two frames a second. steam.sh has the
// measurement.
func TestSteamRunsInsideGamescopeWithTheSteamIntegration(t *testing.T) {
	runSteamScript(t, seatState{})

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
	runSteamScript(t, seatState{})

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
	started, _ := runSteamScript(t, seatState{})

	if !strings.Contains(started, "mangohud=1") {
		t.Errorf("Steam was started without the cap: %q", started)
	}
}

// The daemon calls this after closing one, and the session calls it at startup.
// Those two can overlap on a seat that is provisioned while it is running, and
// a second client against the same home directory does not come up as a second
// Steam: it hands the first one the arguments and leaves.
func TestNothingIsStartedWhenGamescopeIsAlreadyThere(t *testing.T) {
	started, said := runSteamScript(t, seatState{gamescopeRunning: true})

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
	started, said := runSteamScript(t, seatState{steamOutside: true})

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
	_, said := runSteamScript(t, seatState{gamescopeDies: true})

	if !strings.Contains(said, "did not come up") {
		t.Errorf("the script did not notice a gamescope that never started: %q", said)
	}

	if !strings.Contains(said, "trying once more") {
		t.Errorf("the script noticed but did not try again: %q", said)
	}
}

// The leftover gamescope is what made the autostart useless, and it is the
// reason a player who clicked Steam in Moonlight waited twenty to thirty
// seconds for Big Picture.
//
// gamescope keeps its own session so that it does not die with sway, but its
// Wayland connection dies anyway, and it follows a few seconds later. In those
// few seconds the session restarts, this script runs as one of sway's first
// exec lines, and the old guard found the leftover and decided there was
// nothing to do. So the seat had no Steam at all, and whatever was picked first
// paid for building the pair. Measured in both seats on 2026-09-22.
//
// Age is what tells them apart: a gamescope older than the sway it is supposed
// to be nested in cannot be nested in it.
func TestAGamescopeFromAnOldSessionIsNotThisSessionsGamescope(t *testing.T) {
	started, said := runSteamScript(t, seatState{staleGamescope: true})

	if strings.Contains(said, "already running") {
		t.Errorf("a leftover gamescope was taken for this session's: %q", said)
	}

	if !strings.Contains(said, "session that is gone") {
		t.Errorf("said %q, which does not say what was found", said)
	}

	if gamescopeArgs == "" {
		t.Error("no gamescope was started, so the seat is left without one")
	}

	if !strings.HasPrefix(started, "-silent") {
		t.Errorf("started steam %q, want -silent inside a new gamescope", started)
	}
}

// And it is taken away rather than waited for. A compositor with no compositor
// to draw into cannot be handed a window, and leaving it in the process table
// is what defeated the guard in the first place.
func TestTheLeftoverGamescopeIsTakenAway(t *testing.T) {
	_, said := runSteamScript(t, seatState{staleGamescope: true})

	if !strings.Contains(said, "taking it away") {
		t.Errorf("said %q, which does not say the leftover was removed", said)
	}

	if !gone(stalePid) {
		t.Error("the leftover gamescope is still running")
	}
}

// The dropped request is the other half of the twenty to thirty seconds.
//
// `steam steam://open/bigpicture` writes into a pipe in the home directory, and
// a Steam that is not listening on it yet does not queue what arrives, it loses
// it. The script used to wait a fixed twenty seconds, ask once, and say
// "opening Big Picture" whether or not anything had heard it - so a seat could
// sit there with Steam running, no window, and a log claiming success, and the
// player who picked Steam in Moonlight watched Big Picture being built.
//
// So it asks again until sway shows the window.
func TestBigPictureIsAskedForAgainWhenTheFirstRequestIsLost(t *testing.T) {
	_, said := runSteamScript(t, seatState{dropsFirstRequest: true})

	if len(requests) < 2 {
		t.Errorf("asked %d times and gave up, so the seat has no Big Picture", len(requests))
	}

	if !strings.Contains(said, "Big Picture is up") {
		t.Errorf("said %q, which never says the window arrived", said)
	}
}

// And the window itself is the answer, not the request: a run that finds one
// already there asks for nothing at all.
//
// That is the path a Moonlight entry takes on a seat whose autostart did its
// work, and it is what makes picking Steam there immediate. It also keeps a url
// away from somebody who is playing: a game under gamescope is that same
// window, and asking for Big Picture over it would take them out of the game.
func TestNothingIsAskedForWhenBigPictureIsAlreadyOnTheScreen(t *testing.T) {
	_, said := runSteamScript(t, seatState{gamescopeRunning: true, bigPictureUp: true})

	if len(requests) != 0 {
		t.Errorf("asked for %v although the window was already there", requests)
	}

	if !strings.Contains(said, "already running and on screen") {
		t.Errorf("said %q, which does not say why nothing was done", said)
	}
}
