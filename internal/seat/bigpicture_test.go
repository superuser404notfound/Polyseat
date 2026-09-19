package seat

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
elif question == "picture":
    painted = payload["painted"]

    def sample(x, y):
        if painted is None:
            return 0

        left, top, wide, high = painted

        return 40 if left <= x < left + wide and top <= y < top + high else 0

    print(json.dumps(module.unpainted(payload["node"], sample)))
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

func verdict(t *testing.T, node, painted string) any {
	t.Helper()

	payload := fmt.Sprintf(`{"node":%s,"painted":%s}`, node, painted)

	var answer any
	if err := json.Unmarshal(askWatcher(t, "picture", payload), &answer); err != nil {
		t.Fatalf("the driver printed something unreadable")
	}

	return answer
}

// The second reported case, in the numbers a seat produced: a fullscreen
// window of 3840x2160 that was mapped at 1280x800, with everything outside
// that corner exactly black. Nothing in sway's tree says a client painted only
// part of its window, so this is read off the screen.
func TestAPictureInTheCornerIsSeenForWhatItIs(t *testing.T) {
	const window = `{"id":6,"pid":2965,"name":"Big-Picture-Modus","window_properties":{"class":"steam"},
		"fullscreen_mode":1,"rect":{"x":0,"y":0,"width":3840,"height":2160},
		"geometry":{"x":0,"y":0,"width":1280,"height":800}}`

	if got := verdict(t, window, `[0,0,1280,800]`); got != true {
		t.Errorf("a picture drawn in the top left corner was answered with %v, want it recognised", got)
	}

	if got := verdict(t, window, `[0,0,3840,2160]`); got != false {
		t.Errorf("a picture that fills the screen was answered with %v, want it left alone", got)
	}

	// The half minute a cold Steam spends unpacking its UI. There is nothing
	// on the screen to judge, and answering "it fills the screen" there would
	// mean the picture is never looked at again, which is the whole bug.
	if got := verdict(t, window, `null`); got != nil {
		t.Errorf("a screen with nothing painted on it yet was answered with %v, want no answer", got)
	}
}

// What it must not touch. A window that is not fullscreen is somebody's tiled
// window, and a window mapped at the size of the screen has no smaller picture
// it could be stuck at, so neither is worth a screenshot.
func TestAWindowWithNothingToRepairIsNotLookedAt(t *testing.T) {
	windowed := `{"id":6,"pid":2965,"name":"Big-Picture-Modus","window_properties":{"class":"steam"},
		"fullscreen_mode":0,"rect":{"x":0,"y":0,"width":1920,"height":2130},
		"geometry":{"x":0,"y":0,"width":1280,"height":800}}`

	if got := verdict(t, windowed, `[0,0,1280,800]`); got != false {
		t.Errorf("a tiled window was answered with %v, want it left alone", got)
	}

	whole := `{"id":6,"pid":2965,"name":"Big-Picture-Modus","window_properties":{"class":"steam"},
		"fullscreen_mode":1,"rect":{"x":0,"y":0,"width":1920,"height":1080},
		"geometry":{"x":0,"y":0,"width":1920,"height":1080}}`

	if got := verdict(t, whole, `null`); got != false {
		t.Errorf("a window mapped at the size of the screen was answered with %v, want it left alone", got)
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
