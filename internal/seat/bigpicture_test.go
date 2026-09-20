package seat

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The driver imports the helper and asks it one question, the same way the
// pointer tests do: the decisions are a few lines of Python inside an asset,
// and a Go transcription of them would only prove that the transcription
// agrees with itself.
//
// "watch" is the one that matters. The rule it checks spans several events, so
// the two pieces of state are carried across them here exactly as the loop in
// the watcher carries them, and the answers are the window it would have asked
// sway to fullscreen after each event. The three lines below are that loop, and
// a test drives a recorded session through them.
const watchDriver = `
import importlib.util, json, sys

spec = importlib.util.spec_from_file_location("watch", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

question, payload = sys.argv[2], json.loads(sys.argv[3])

if question == "watch":
    state, seen, answers = {}, set(), []

    for step in payload:
        back = module.dethroned(state, step["event"], step["now"])
        fresh = module.cold_start(seen, step["event"])
        answers.append(back if back is not None else fresh)

    print(json.dumps(answers))
elif question == "mine":
    print(json.dumps(module.is_big_picture(payload)))
else:
    node = module.window(payload["tree"], payload["id"])
    print(json.dumps(None if node is None else node.get("id")))
`

func askWatcher(t *testing.T, question, payload string) []byte {
	t.Helper()

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("SKIPPED: no python3, so the helper's behaviour is unverified here")
	}

	script := filepath.Join(t.TempDir(), "bigpicture-watch.py")
	if err := os.WriteFile(script, asset("assets/bigpicture-watch.py"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(python, "-c", watchDriver, script, question, payload).Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && len(exit.Stderr) > 0 {
			t.Skipf("SKIPPED: the helper could not be loaded here: %s", exit.Stderr)
		}

		t.Fatal(err)
	}

	return out
}

// fold plays a sequence of window events through the watcher and answers with
// the window ids it would have put back to fullscreen, one entry per event,
// nil where it would have done nothing.
func fold(t *testing.T, steps string) []any {
	t.Helper()

	var answers []any
	if err := json.Unmarshal(askWatcher(t, "watch", steps), &answers); err != nil {
		t.Fatalf("the driver printed something unreadable")
	}

	return answers
}

// restored reports the ids the sequence put back, dropping the events that
// asked for nothing, so a test can say what it means in one line.
func restored(t *testing.T, steps string) []float64 {
	t.Helper()

	var ids []float64
	for _, answer := range fold(t, steps) {
		if answer != nil {
			ids = append(ids, answer.(float64))
		}
	}

	return ids
}

// The reported case, in the events a seat actually produced: Big Picture is
// dethroned, the game takes fullscreen, the game ends. Recorded from a German
// session, where every window title match in the seat was missing the window,
// which is the whole reason this rule reads ids and not names.
func TestTheGameGivesFullscreenBackWhenItEnds(t *testing.T) {
	steps := `[
		{"now":100.0,"event":{"change":"fullscreen_mode","container":{"id":11,"pid":15442,"fullscreen_mode":0,"name":"Big-Picture-Modus","window_properties":{"class":"steam"}}}},
		{"now":100.0,"event":{"change":"focus","container":{"id":13,"fullscreen_mode":1,"name":"DREDGE"}}},
		{"now":100.0,"event":{"change":"fullscreen_mode","container":{"id":13,"fullscreen_mode":1,"name":"DREDGE"}}},
		{"now":116.0,"event":{"change":"close","container":{"id":13,"fullscreen_mode":1,"name":"DREDGE"}}}]`

	got := restored(t, steps)

	if len(got) != 1 || got[0] != 11 {
		t.Errorf("quitting the game put back %v, want the window it took fullscreen from, id 11", got)
	}
}

// A game is often two windows, and the one that closes first is not the end of
// the game. Forza Horizon 6 goes fullscreen, starts the game proper, which
// takes fullscreen from it, and then closes: reported as Big Picture coming
// back windowed, and visible in the seat's journal as the watcher putting a
// window of the game back on screen while somebody was playing it.
//
// So every window that takes the screen is remembered, and Big Picture is put
// back when the last of them is gone.
func TestALauncherClosingMidGameIsNotTheEndOfTheGame(t *testing.T) {
	const game = `"pid":63505,"window_properties":{"class":"steam_app_2483190"}`

	steps := `[
		{"now":100.0,"event":{"change":"fullscreen_mode","container":{"id":11,"pid":15442,"fullscreen_mode":0,"name":"Big-Picture-Modus","window_properties":{"class":"steam"}}}},
		{"now":100.0,"event":{"change":"fullscreen_mode","container":{"id":21,` + game + `,"fullscreen_mode":1,"name":"Forza Horizon 6"}}},
		{"now":140.0,"event":{"change":"fullscreen_mode","container":{"id":21,` + game + `,"fullscreen_mode":0,"name":"Forza Horizon 6"}}},
		{"now":140.0,"event":{"change":"fullscreen_mode","container":{"id":26,` + game + `,"fullscreen_mode":1,"name":null}}},
		{"now":141.0,"event":{"change":"close","container":{"id":21,` + game + `,"fullscreen_mode":0,"name":"Forza Horizon 6"}}}]`

	if got := restored(t, steps); len(got) != 0 {
		t.Errorf("the game lost the screen to %v while it was being played", got)
	}

	ended := steps[:len(steps)-1] + `,
		{"now":900.0,"event":{"change":"close","container":{"id":26,` + game + `,"fullscreen_mode":1,"name":null}}}]`

	got := restored(t, ended)

	if len(got) != 1 || got[0] != 11 {
		t.Errorf("quitting the game put back %v, want Big Picture, id 11", got)
	}
}

// And a game passing fullscreen between its own windows is not a dethroning at
// all. This is the line the seat's journal carried while the bug was open,
// `put window 21 back to fullscreen`, where 21 was a window of the game and
// Big Picture sat tiled behind it: the memory had been spent on windows that
// were never Big Picture.
func TestOnlyBigPictureIsRememberedAsTheWindowThatLostTheScreen(t *testing.T) {
	const game = `"pid":63505,"window_properties":{"class":"steam_app_2483190"}`

	steps := `[
		{"now":100.0,"event":{"change":"fullscreen_mode","container":{"id":21,` + game + `,"fullscreen_mode":0,"name":"Forza Horizon 6"}}},
		{"now":100.0,"event":{"change":"fullscreen_mode","container":{"id":26,` + game + `,"fullscreen_mode":1,"name":null}}},
		{"now":200.0,"event":{"change":"close","container":{"id":26,` + game + `,"fullscreen_mode":1,"name":null}}}]`

	if got := restored(t, steps); len(got) != 0 {
		t.Errorf("a window of the game was put back as if it were Big Picture: %v", got)
	}
}

// The whole recorded session, noise and all, because the two events that
// matter arrived among sixteen that did not: a Steam window that opens and
// closes again, a Proton dialog, focus changes, and a game whose window class
// is its own. Anything this fires on besides the two lines below is something
// the player did not ask for.
//
// Taken from a seat with `swaymsg -t subscribe` while somebody played, which is
// how the translated title was found in the first place.
func TestTheRecordedSessionAsksForFullscreenTwiceAndNoMoreThanThat(t *testing.T) {
	const steam = `"window_properties":{"class":"steam"}`
	const game = `"window_properties":{"class":"steam_app_1562430"}`

	steps := `[
		{"now":0.0,"event":{"change":"new","container":{"id":10,"pid":15442,"fullscreen_mode":0,"name":null,` + steam + `}}},
		{"now":0.0,"event":{"change":"title","container":{"id":10,"pid":15442,"fullscreen_mode":0,"name":"Steam",` + steam + `}}},
		{"now":0.0,"event":{"change":"focus","container":{"id":10,"pid":15442,"fullscreen_mode":0,"name":"Steam",` + steam + `}}},
		{"now":9.0,"event":{"change":"new","container":{"id":11,"pid":15442,"fullscreen_mode":0,"name":null,` + steam + `}}},
		{"now":9.0,"event":{"change":"title","container":{"id":11,"pid":15442,"fullscreen_mode":0,"name":"Big-Picture-Modus",` + steam + `}}},
		{"now":9.0,"event":{"change":"focus","container":{"id":11,"pid":15442,"fullscreen_mode":0,"name":"Big-Picture-Modus",` + steam + `}}},
		{"now":9.0,"event":{"change":"fullscreen_mode","container":{"id":11,"pid":15442,"fullscreen_mode":1,"name":"Big-Picture-Modus",` + steam + `}}},
		{"now":9.0,"event":{"change":"close","container":{"id":10,"pid":15442,"fullscreen_mode":0,"name":"Steam",` + steam + `}}},
		{"now":31.0,"event":{"change":"new","container":{"id":12,"pid":63423,"fullscreen_mode":0,"name":null,"app_id":"zenity"}}},
		{"now":31.0,"event":{"change":"floating","container":{"id":12,"pid":63423,"fullscreen_mode":0,"name":null,"app_id":"zenity"}}},
		{"now":31.0,"event":{"change":"title","container":{"id":12,"pid":63423,"fullscreen_mode":0,"name":"ProtonFixes","app_id":"zenity"}}},
		{"now":31.0,"event":{"change":"close","container":{"id":12,"pid":63423,"fullscreen_mode":0,"name":"ProtonFixes","app_id":"zenity"}}},
		{"now":33.0,"event":{"change":"new","container":{"id":13,"pid":63505,"fullscreen_mode":0,"name":null,` + game + `}}},
		{"now":33.0,"event":{"change":"title","container":{"id":13,"pid":63505,"fullscreen_mode":0,"name":"DREDGE",` + game + `}}},
		{"now":33.0,"event":{"change":"fullscreen_mode","container":{"id":11,"pid":15442,"fullscreen_mode":0,"name":"Big-Picture-Modus",` + steam + `}}},
		{"now":33.0,"event":{"change":"focus","container":{"id":13,"pid":63505,"fullscreen_mode":1,"name":"DREDGE",` + game + `}}},
		{"now":33.0,"event":{"change":"fullscreen_mode","container":{"id":13,"pid":63505,"fullscreen_mode":1,"name":"DREDGE",` + game + `}}},
		{"now":49.0,"event":{"change":"close","container":{"id":13,"pid":63505,"fullscreen_mode":1,"name":"DREDGE",` + game + `}}},
		{"now":49.0,"event":{"change":"focus","container":{"id":11,"pid":15442,"fullscreen_mode":0,"name":"Big-Picture-Modus",` + steam + `}}}]`

	answers := fold(t, steps)

	var asked []string
	for at, answer := range answers {
		if answer != nil {
			asked = append(asked, fmt.Sprintf("event %d: id %v", at, answer))
		}
	}

	want := []string{"event 4: id 11", "event 17: id 11"}

	if strings.Join(asked, ", ") != strings.Join(want, ", ") {
		t.Errorf("the session asked sway for [%s], want [%s]: Big Picture when it is named, and again when the game ends",
			strings.Join(asked, ", "), strings.Join(want, ", "))
	}
}

// The one this must never undo. $mod+f looks like the first event of the
// sequence above and nothing follows it, so nothing may be put back.
//
// The rename at the end is the part that is easy to miss. Steam renames its
// window while it runs, and the cold start branch matches on the title, so
// without a memory of which windows it has already put on screen that rename
// would drag the player back into Big Picture seconds after they left it.
func TestLeavingFullscreenByHandIsNotUndone(t *testing.T) {
	steps := `[
		{"now":100.0,"event":{"change":"title","container":{"id":11,"pid":15442,"fullscreen_mode":0,"name":"Big-Picture-Modus","window_properties":{"class":"steam"}}}},
		{"now":180.0,"event":{"change":"fullscreen_mode","container":{"id":11,"pid":15442,"fullscreen_mode":0,"name":"Big-Picture-Modus","window_properties":{"class":"steam"}}}},
		{"now":190.0,"event":{"change":"title","container":{"id":11,"pid":15442,"fullscreen_mode":0,"name":"Big-Picture-Modus","window_properties":{"class":"steam"}}}},
		{"now":200.0,"event":{"change":"close","container":{"id":5,"pid":360,"fullscreen_mode":0,"name":"player@vince:~"}}}]`

	answers := fold(t, steps)

	if answers[0] != float64(11) {
		t.Fatalf("Big Picture appearing was answered with %v, want it put on screen as id 11", answers[0])
	}

	for _, answer := range answers[1:] {
		if answer != nil {
			t.Errorf("something was fullscreened after the player left it by hand: %v", answers[1:])

			break
		}
	}
}

// And a window somebody left fullscreen an hour ago is not dragged back by the
// next game that ends. Nothing took fullscreen from it, and the gap says so.
func TestAWindowLeftAloneLongAgoIsNotDraggedBack(t *testing.T) {
	steps := `[
		{"now":100.0,"event":{"change":"fullscreen_mode","container":{"id":11,"pid":15442,"fullscreen_mode":0,"name":"Big-Picture-Modus","window_properties":{"class":"steam"}}}},
		{"now":3700.0,"event":{"change":"fullscreen_mode","container":{"id":13,"fullscreen_mode":1,"name":"DREDGE"}}},
		{"now":3900.0,"event":{"change":"close","container":{"id":13,"fullscreen_mode":1,"name":"DREDGE"}}}]`

	if got := restored(t, steps); len(got) != 0 {
		t.Errorf("a game an hour later was treated as the thief: %v", got)
	}
}

// A game that drops out of fullscreen before it quits took fullscreen all the
// same, and leaves the same tiled Big Picture behind.
func TestAGameThatLeavesFullscreenBeforeQuittingStillGivesItBack(t *testing.T) {
	steps := `[
		{"now":100.0,"event":{"change":"fullscreen_mode","container":{"id":11,"pid":15442,"fullscreen_mode":0,"name":"Big-Picture-Modus","window_properties":{"class":"steam"}}}},
		{"now":100.0,"event":{"change":"fullscreen_mode","container":{"id":13,"fullscreen_mode":1,"name":"DREDGE"}}},
		{"now":300.0,"event":{"change":"fullscreen_mode","container":{"id":13,"fullscreen_mode":0,"name":"DREDGE"}}},
		{"now":310.0,"event":{"change":"close","container":{"id":13,"fullscreen_mode":0,"name":"DREDGE"}}}]`

	got := restored(t, steps)

	if len(got) != 1 || got[0] != 11 {
		t.Errorf("a game that windowed itself first put back %v, want id 11", got)
	}
}

// A fullscreen window closing on its own says nothing about anybody else. This
// is what the first version keyed on, and it is why that version would have
// gone to the tree for every game whether or not one had ever been dethroned.
func TestAFullscreenWindowClosingOnItsOwnRestoresNothing(t *testing.T) {
	steps := `[
		{"now":100.0,"event":{"change":"fullscreen_mode","container":{"id":11,"pid":15442,"fullscreen_mode":0,"name":"Big-Picture-Modus","window_properties":{"class":"steam"}}}},
		{"now":140.0,"event":{"change":"close","container":{"id":13,"fullscreen_mode":1,"name":"DREDGE"}}}]`

	if got := restored(t, steps); len(got) != 0 {
		t.Errorf("a window nobody was dethroned by put something back: %v", got)
	}
}

// And if Big Picture itself is gone by then there is nothing to put back. Its
// id would be somebody else's window by the time the game ends.
func TestAVictimThatClosedFirstIsForgotten(t *testing.T) {
	steps := `[
		{"now":100.0,"event":{"change":"fullscreen_mode","container":{"id":11,"pid":15442,"fullscreen_mode":0,"name":"Big-Picture-Modus","window_properties":{"class":"steam"}}}},
		{"now":100.0,"event":{"change":"fullscreen_mode","container":{"id":13,"fullscreen_mode":1,"name":"DREDGE"}}},
		{"now":200.0,"event":{"change":"close","container":{"id":11,"pid":15442,"fullscreen_mode":0,"name":"Big-Picture-Modus","window_properties":{"class":"steam"}}}},
		{"now":300.0,"event":{"change":"close","container":{"id":13,"fullscreen_mode":1,"name":"DREDGE"}}}]`

	if got := restored(t, steps); len(got) != 0 {
		t.Errorf("a window that had already closed was put back: %v", got)
	}
}

// A Big Picture that comes up fullscreen on its own is the ordinary case on a
// warm Steam, and asking sway for what it has already done is a round trip for
// nothing.
func TestABigPictureThatIsAlreadyFullscreenIsLeftAlone(t *testing.T) {
	steps := `[
		{"now":100.0,"event":{"change":"title","container":{"id":11,"pid":15442,"fullscreen_mode":1,"name":"Big-Picture-Modus","window_properties":{"class":"steam"}}}}]`

	if got := restored(t, steps); len(got) != 0 {
		t.Errorf("sway was asked to fullscreen a window that already was: %v", got)
	}
}

func mine(t *testing.T, node string) bool {
	t.Helper()

	var answer bool
	if err := json.Unmarshal(askWatcher(t, "mine", node), &answer); err != nil {
		t.Fatalf("the driver printed something unreadable")
	}

	return answer
}

// The title Steam gives the window depends on the language it is in, and the
// English sentence the first version matched on is the bug this release fixes.
func TestBigPictureIsRecognisedInEveryLanguageSteamSpeaks(t *testing.T) {
	for _, title := range []string{
		"Steam Big Picture Mode", // English, and what the first version wanted
		"Big-Picture-Modus",      // German, recorded from the seat
		"Mode Big Picture",       // French
		"Modo Big Picture",       // Spanish
	} {
		node := `{"id":11,"pid":15442,"name":"` + title + `","window_properties":{"class":"steam"}}`

		if !mine(t, node) {
			t.Errorf("%q was not recognised as Big Picture, so nothing in the seat would fullscreen it", title)
		}
	}
}

// Not everything with those words in the title is Steam. A browser reading
// about Big Picture is a window somebody is using, not one to take over.
func TestSomethingElseWithTheWordsInItIsNotBigPicture(t *testing.T) {
	node := `{"id":11,"pid":15442,"name":"Big Picture - Firefox","window_properties":{"class":"firefox"}}`

	if mine(t, node) {
		t.Error("a browser window was taken for Big Picture and would be forced fullscreen")
	}

	// A window with no pid is a sway container and not somebody's window.
	// Matching without this would let a workspace named after the title stand
	// in for it, which is the mistake the pointer helper already had to fix.
	none := `{"id":2,"name":"Big-Picture-Modus","window_properties":{"class":"steam"}}`

	if mine(t, none) {
		t.Error("a container with no window behind it was taken for Big Picture")
	}
}

func target(t *testing.T, tree string, id int) any {
	t.Helper()

	payload := fmt.Sprintf(`{"id":%d,"tree":%s}`, id, tree)

	var answer any
	if err := json.Unmarshal(askWatcher(t, "target", payload), &answer); err != nil {
		t.Fatalf("the driver printed something unreadable")
	}

	return answer
}

// What it does with the tree before asking for anything. An id is only good
// for as long as the window behind it lives, and by the time a game ends the
// window that lost fullscreen may be gone and its id somebody else's.
func TestTheWatcherOnlyPutsBackAWindowThatIsStillThere(t *testing.T) {
	tree := `{"type":"root","nodes":[{"type":"output","nodes":[
		{"type":"workspace","id":2,"name":"1","nodes":[
			{"type":"con","id":3,"pid":11,"name":"foot","window_properties":{"class":"foot"},"fullscreen_mode":0},
			{"type":"con","id":4,"pid":12,"name":"Big-Picture-Modus","window_properties":{"class":"steam"},"fullscreen_mode":0}]}]}]}`

	if got := target(t, tree, 4); got != float64(4) {
		t.Errorf("the window it remembered came back as %v, want id 4", got)
	}

	if got := target(t, tree, 13); got != nil {
		t.Errorf("a window that had closed came back as %v, so sway would be sent an id nobody holds", got)
	}

	// A node with no pid is a sway container and not somebody's window, and
	// container ids and window ids come from the same counter.
	if got := target(t, tree, 2); got != nil {
		t.Errorf("a workspace was taken for a window: %v", got)
	}
}

// The three places that match the title have to carry the same pattern, or the
// one that is wrong silently does nothing at all, which is precisely how the
// English sentence survived in all three until a German seat was streamed.
func TestEverythingAgreesOnHowBigPictureIsMatched(t *testing.T) {
	const pattern = `[Bb]ig[ _-][Pp]icture`

	for _, file := range []string{
		"assets/bigpicture-watch.py",
		"assets/bigpicture.sh",
		"assets/sway.config",
	} {
		if !strings.Contains(string(asset(file)), pattern) {
			t.Errorf("%s does not carry %s, so it matches a window nobody has", file, pattern)
		}
	}
}

// runBigPicture runs the launcher the way Sunshine runs it, against a sway
// that answers with the trees this test hands it and writes down everything
// else it is told, and a Steam that writes the one line this now turns on:
// what it thinks its own scaling is.
//
// reacts is whether that Steam answers an off and on the way the real one
// does, by working the factor out again. Both halves are needed, because the
// thing being checked is a conversation and not a command.
func runBigPicture(t *testing.T, factor string, reacts bool, trees ...string) (asked []string, said string) {
	t.Helper()

	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("SKIPPED: no python3, so the script cannot read a tree here")
	}

	home := t.TempDir()

	script := filepath.Join(home, "polyseat-bigpicture")
	if err := os.WriteFile(script, asset("assets/bigpicture.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	logs := filepath.Join(home, ".local/share/Steam/logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}

	// Lines from before this launch, which must be ignored: the factor that
	// matters is the one Steam works out for the window being opened now.
	stale := "[2026-09-19 12:00:00] ThreadSetForceDeviceScaleFactors 1.000000 * 9.999999 = 10.000000\n"
	if err := os.WriteFile(filepath.Join(logs, "webhelper.txt"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	// A socket rather than a file, because the script asks whether it is one.
	runtime := filepath.Join(home, "run")
	if err := os.MkdirAll(runtime, 0o700); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("unix", filepath.Join(runtime, "sway-ipc.1000.1.sock"))
	if err != nil {
		t.Skipf("SKIPPED: no unix socket here, so the script cannot be run: %v", err)
	}

	defer listener.Close()

	for i, tree := range trees {
		name := filepath.Join(home, fmt.Sprintf("tree.%d", i+1))
		if err := os.WriteFile(name, []byte(tree), 0o644); err != nil {
			t.Fatal(err)
		}
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

	const right = "ThreadSetForceDeviceScaleFactors 1.000000 * 1.423025 = 1.420000"

	answer := ""
	if reacts {
		answer = `case "$*" in *"fullscreen enable") echo '[t] ` + right + `' >> ` +
			filepath.Join(logs, "webhelper.txt") + ";; esac\n"
	}

	// Each get_tree is answered with the next tree the test gave, and with the
	// last one for ever after, so that a test can describe a player leaving
	// Big Picture between one command and the next.
	stub("swaymsg", `#!/bin/sh
if [ "$1" = "-t" ]; then
    n=$(cat `+home+`/calls 2>/dev/null || echo 0)
    n=$((n + 1))
    echo "$n" > `+home+`/calls
    file=`+home+`/tree.$n
    [ -f "$file" ] || file=$(ls `+home+`/tree.* | sort -V | tail -1)
    cat "$file"
    exit 0
fi
echo "$*" >> `+home+`/asked
`+answer)

	// Steam writes what it made of the window's size, or nothing at all.
	write := ""
	if factor != "" {
		write = "echo '[t] ThreadSetForceDeviceScaleFactors 1.000000 * " + factor +
			"' >> " + filepath.Join(logs, "webhelper.txt") + "\n"
	}

	stub("steam", "#!/bin/sh\n"+write)
	stub("setsid", "#!/bin/sh\nexec \"$@\"\n")

	// Short rather than instant: Steam writes its line from a process this
	// script put in the background, and a sleep that returns before that
	// happens would be testing the test.
	stub("sleep", "#!/bin/sh\nexec /bin/sleep 0.1\n")

	cmd := exec.Command("/bin/sh", script)
	cmd.Env = []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"XDG_RUNTIME_DIR=" + runtime,
	}

	var complaints strings.Builder
	cmd.Stderr = &complaints

	if err := cmd.Run(); err != nil {
		t.Fatalf("running the script failed: %v: %s", err, complaints.String())
	}

	recorded, err := os.ReadFile(filepath.Join(home, "asked"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	if len(recorded) == 0 {
		return nil, complaints.String()
	}

	return strings.Split(strings.TrimSpace(string(recorded)), "\n"), complaints.String()
}

const bigPictureFull = `{"type":"root","nodes":[{"type":"output","nodes":[
	{"type":"workspace","id":2,"name":"1","nodes":[
		{"type":"con","id":6,"pid":2965,"name":"Big-Picture-Modus",
		 "window_properties":{"class":"steam"},"fullscreen_mode":1,
		 "rect":{"x":0,"y":0,"width":1920,"height":1080},
		 "geometry":{"x":0,"y":0,"width":1280,"height":800}}]}]}]}`

const bigPictureWindowed = `{"type":"root","nodes":[{"type":"output","nodes":[
	{"type":"workspace","id":2,"name":"1","nodes":[
		{"type":"con","id":6,"pid":2965,"name":"Big-Picture-Modus",
		 "window_properties":{"class":"steam"},"fullscreen_mode":0,
		 "rect":{"x":0,"y":0,"width":960,"height":1050},
		 "geometry":{"x":0,"y":0,"width":1280,"height":800}}]}]}]}`

var (
	off = `-- [class="^steam$" title="[Bb]ig[ _-][Pp]icture"] fullscreen disable`
	on  = `-- [class="^steam$" title="[Bb]ig[ _-][Pp]icture"] fullscreen enable`
)

// The number this now turns on, and where it comes from. Big Picture is a
// fixed 1280x800 interface that Steam scales to its window, so filling a
// 1920x1080 screen with it takes sqrt(1920/1280 * 1080/800) = 1.423025, and
// that is to six places what Steam writes in its own log when it gets it
// right. When it gets it wrong it writes 1.000000 and never looks again.
func TestSteamsOwnFactorDecidesWhetherAnythingIsDone(t *testing.T) {
	asked, said := runBigPicture(t, "1.423025 = 1.420000", false, bigPictureFull)

	if len(asked) != 0 {
		t.Errorf("sway was asked for %q on a Big Picture that already filled the screen", asked)
	}

	if !strings.Contains(said, "came up at the size of the screen") {
		t.Errorf("the journal says %q, want it to say it found nothing to do", said)
	}
}

// The reported bug, and the repair, with Steam answering the way the seat's
// log shows it answering: the off and on is a change of size, and a change of
// size is the one thing that makes Steam work the factor out again.
func TestAWrongFactorIsRepairedAndTheRepairIsChecked(t *testing.T) {
	asked, said := runBigPicture(t, "1.000000 = 1.000000", true, bigPictureFull)

	if want := []string{off, on}; !reflect.DeepEqual(asked, want) {
		t.Errorf("sway was asked for %q, want one off and on", asked)
	}

	if !strings.Contains(said, "fills the screen after 1 off and on") {
		t.Errorf("the journal says %q, want it to say the repair worked", said)
	}
}

// And when it does not work, it says so with both numbers in the line, rather
// than flashing the screen until somebody notices.
func TestAFactorThatWillNotBudgeIsGivenUpOnOutLoud(t *testing.T) {
	asked, said := runBigPicture(t, "1.000000 = 1.000000", false, bigPictureFull)

	if len(asked) != 4 {
		t.Errorf("sway was asked for %q, want two tries of an off and on", asked)
	}

	if !strings.Contains(said, "1.000000") || !strings.Contains(said, "1.423025") {
		t.Errorf("the journal says %q, want what Steam is drawing at and what it should be", said)
	}
}

// A Steam that says nothing about its own scaling is not a Steam that is
// wrong. This is the one place 0.22.0 got backwards: it could not read the
// screen, called that "nothing painted yet", and waited it out in silence.
func TestASilentSteamIsSaidOutLoudAndGivenOneTry(t *testing.T) {
	asked, said := runBigPicture(t, "", false, bigPictureFull)

	if want := []string{off, on}; !reflect.DeepEqual(asked, want) {
		t.Errorf("sway was asked for %q, want exactly one off and on, done blind", asked)
	}

	if !strings.Contains(said, "says nothing about its own scaling") {
		t.Errorf("the journal says %q, want it to say it could not tell", said)
	}
}

// What this must never do. By the time the factor has been read the player may
// have left Big Picture or started a game from it, and pulling the screen off
// a game and handing it back is worse than the corner this is here to fix.
func TestNothingIsTakenFromAWindowThatIsNoLongerBigPicture(t *testing.T) {
	asked, said := runBigPicture(t, "1.000000 = 1.000000", false,
		bigPictureFull, bigPictureWindowed)

	if len(asked) != 0 {
		t.Errorf("sway was asked for %q after the player had left Big Picture", asked)
	}

	if !strings.Contains(said, "no longer has the screen") {
		t.Errorf("the journal says %q, want it to say why it stopped", said)
	}
}

// The other half of the job, which the rule in the sway configuration misses
// on a cold start because the window is mapped before it has a title.
func TestABigPictureThatNeverTakesTheScreenIsAskedAndThenLeft(t *testing.T) {
	asked, said := runBigPicture(t, "1.000000 = 1.000000", false, bigPictureWindowed)

	if len(asked) == 0 {
		t.Fatalf("a windowed Big Picture was never asked to go fullscreen")
	}

	for _, command := range asked {
		if strings.Contains(command, "fullscreen disable") {
			t.Fatalf("a window that never had the screen was taken off it: %q", asked)
		}
	}

	if !strings.Contains(said, "never had the screen") {
		t.Errorf("the journal says %q, want it to say it gave up", said)
	}
}
