package seat

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The driver imports the helper and asks it about one sway tree, which is the
// only way to test the answer rather than a copy of it: the function is a dozen
// lines of Python inside an asset, and a Go transcription would only prove the
// two agree with each other.
const pointerDriver = `
import importlib.util, json, sys

spec = importlib.util.spec_from_file_location("padpointer", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

print(json.dumps(module.Sway._focused_is_fullscreen(json.loads(sys.argv[2]))))
`

// inFront reports what the helper would conclude about one sway tree.
func inFront(t *testing.T, tree string) bool {
	t.Helper()

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("SKIPPED: no python3, so the helper's behaviour is unverified here")
	}

	script := filepath.Join(t.TempDir(), "pad-pointer.py")
	if err := os.WriteFile(script, asset("assets/pad-pointer.py"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(python, "-c", pointerDriver, script, tree).Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && len(exit.Stderr) > 0 {
			t.Skipf("SKIPPED: the helper could not be loaded here: %s", exit.Stderr)
		}

		t.Fatal(err)
	}

	var answer bool
	if err := json.Unmarshal(out, &answer); err != nil {
		t.Fatalf("the driver printed %q", out)
	}

	return answer
}

// The whole point of the automatic mode: a game is in front, so the controller
// belongs to it.
func TestPointerStandsAsideForAFullscreenWindow(t *testing.T) {
	tree := `{"type":"root","nodes":[{"type":"output","nodes":[
		{"type":"workspace","name":"1","fullscreen_mode":1,"nodes":[
			{"type":"con","name":"Steam Big Picture","focused":true,"fullscreen_mode":1}]}]}]}`

	if !inFront(t, tree) {
		t.Error("a focused fullscreen window was not recognised, so a game would keep a pointer on its stick")
	}
}

func TestPointerStaysOnForAnOrdinaryWindow(t *testing.T) {
	tree := `{"type":"root","nodes":[{"type":"output","nodes":[
		{"type":"workspace","name":"1","fullscreen_mode":1,"nodes":[
			{"type":"con","name":"a terminal","focused":true,"fullscreen_mode":0}]}]}]}`

	if inFront(t, tree) {
		t.Error("an ordinary window was taken for a game, so the desktop would have no pointer")
	}
}

// The bug this was found to have. Sway focuses the workspace itself when no
// window holds focus, and a workspace reports fullscreen_mode 1 whatever is on
// it, a quirk inherited from i3. Reading that as an answer turned the pointer
// off on a plain empty desktop, which is exactly when it is needed.
func TestPointerIsNotFooledByAFocusedWorkspace(t *testing.T) {
	tree := `{"type":"root","nodes":[{"type":"output","nodes":[
		{"type":"workspace","name":"1","focused":true,"fullscreen_mode":1,"nodes":[]}]}]}`

	if inFront(t, tree) {
		t.Error("a focused workspace was taken for a fullscreen window, so an empty desktop loses its pointer")
	}
}

// A fullscreen window somebody else is not looking at says nothing about the
// controller in this session.
func TestPointerOnlyCaresAboutTheFocusedWindow(t *testing.T) {
	tree := `{"type":"root","nodes":[{"type":"output","nodes":[
		{"type":"workspace","name":"1","fullscreen_mode":1,"nodes":[
			{"type":"con","name":"a game on another workspace","focused":false,"fullscreen_mode":1},
			{"type":"con","name":"the launcher","focused":true,"fullscreen_mode":0}]}]}]}`

	if inFront(t, tree) {
		t.Error("an unfocused fullscreen window decided it, so the pointer would depend on windows nobody is using")
	}
}

// Floating windows are kept in a list of their own and games do use them.
func TestPointerSeesAFloatingFullscreenWindow(t *testing.T) {
	tree := `{"type":"root","nodes":[{"type":"output","nodes":[
		{"type":"workspace","name":"1","fullscreen_mode":1,"nodes":[],"floating_nodes":[
			{"type":"floating_con","name":"a game","focused":true,"fullscreen_mode":1}]}]}]}`

	if !inFront(t, tree) {
		t.Error("a floating fullscreen window was missed")
	}
}

// The driver for the other pure half: which output's height sets the speed.
const heightDriver = `
import importlib.util, json, sys

spec = importlib.util.spec_from_file_location("padpointer", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

print(json.dumps(module.Sway._height_of(json.loads(sys.argv[2]))))
`

func heightOf(t *testing.T, outputs string) any {
	t.Helper()

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("SKIPPED: no python3, so the helper's behaviour is unverified here")
	}

	script := filepath.Join(t.TempDir(), "pad-pointer.py")
	if err := os.WriteFile(script, asset("assets/pad-pointer.py"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(python, "-c", heightDriver, script, outputs).Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && len(exit.Stderr) > 0 {
			t.Skipf("SKIPPED: the helper could not be loaded here: %s", exit.Stderr)
		}

		t.Fatal(err)
	}

	var answer any
	if err := json.Unmarshal(out, &answer); err != nil {
		t.Fatalf("the driver printed %q", out)
	}

	return answer
}

// The pointer crosses the same fraction of the screen per second whatever
// resolution the client asked for, so the speed comes from the output's height.
func TestPointerTakesItsSpeedFromTheActiveOutput(t *testing.T) {
	outputs := `[{"name":"HEADLESS-1","active":true,"rect":{"width":1280,"height":720}}]`

	if got := heightOf(t, outputs); got != 720.0 {
		t.Errorf("read %v, want 720", got)
	}
}

// An output sway knows about but is not driving reports a geometry of zero by
// zero. Taken as an answer that is a pointer with a speed of nothing, which
// looks exactly like a helper that has stopped working.
//
// The shape of this was measured rather than imagined: a second headless output
// was added to a seat and disabled, and it reports active false with a rect of
// zeroes. The first version of this test invented an entry with a name that only
// appears in the window tree, and passed whether the code looked at any of it.
func TestPointerIgnoresAnOutputWithNoGeometry(t *testing.T) {
	outputs := `[
		{"name":"HEADLESS-2","active":false,"rect":{"x":0,"y":0,"width":0,"height":0}},
		{"name":"HEADLESS-1","active":true,"rect":{"x":0,"y":0,"width":3840,"height":2160}}]`

	if got := heightOf(t, outputs); got != 2160.0 {
		t.Errorf("read %v, want 2160", got)
	}
}

// No answer rather than a wrong one, so the caller falls back to its own
// assumption instead of multiplying a speed by nothing.
func TestPointerHasNoHeightWhenSwaySaysNothingUseful(t *testing.T) {
	for name, outputs := range map[string]string{
		"an empty list":            `[]`,
		"not a list at all":        `{"error":"no"}`,
		"an output with no rect":   `[{"name":"HEADLESS-1","active":true}]`,
		"an output with no height": `[{"name":"HEADLESS-1","active":true,"rect":{"width":1920}}]`,
	} {
		if got := heightOf(t, outputs); got != nil {
			t.Errorf("%s: read %v, want nothing", name, got)
		}
	}
}

// The driver for the chord. It replays a scripted session against the helper's
// own state machine and reports when the switch by hand would have happened.
//
// Time comes from the script rather than from a clock, so a second of holding
// is tested rather than waited for.
const chordDriver = `
import importlib.util, json, sys

spec = importlib.util.spec_from_file_location("padpointer", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

chord = module.Chord(module.CHORD, module.CHORD_SIZE, module.HOLD)
buttons = dict(zip(("select", "start", "guide"), module.CHORD))
held, fired, tapped = set(), [], []

# A step is a button and a moment. "select" presses it, "-select" lets go, and
# an empty name is the loop coming round with nothing to report, which is what
# a held chord looks like from in here.
labels = {code: name for name, code in buttons.items()}

for name, when in json.loads(sys.argv[2]):
    if name:
        button = buttons[name.lstrip("-")]
        held.discard(button) if name.startswith("-") else held.add(button)

        alone = chord.update(held, 1, when)
        if alone is not None:
            tapped.append(labels[alone])

    if chord.due(when):
        fired.append(when)

print(json.dumps({"fired": fired, "tapped": tapped}))
`

// chordRun is what a replayed session produced: the moments the mode would
// flip, and the buttons that turned out to be a press on their own.
type chordRun struct {
	Fired  []float64 `json:"fired"`
	Tapped []string  `json:"tapped"`
}

// chordFires replays a session and reports the moments the mode would flip.
func chordFires(t *testing.T, script string) []float64 {
	return replayChord(t, script).Fired
}

func replayChord(t *testing.T, script string) chordRun {
	t.Helper()

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("SKIPPED: no python3, so the helper's behaviour is unverified here")
	}

	dir := t.TempDir()

	path := filepath.Join(dir, "pad-pointer.py")
	if err := os.WriteFile(path, asset("assets/pad-pointer.py"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(python, "-c", chordDriver, path, script).Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && len(exit.Stderr) > 0 {
			t.Skipf("SKIPPED: the helper could not be loaded here: %s", exit.Stderr)
		}

		t.Fatal(err)
	}

	var run chordRun
	if err := json.Unmarshal(out, &run); err != nil {
		t.Fatalf("the driver printed %q", out)
	}

	return run
}

// The point of the change: both buttons at once is something a game can ask
// for, so pressing them is not enough. Only holding them is.
func TestPointerChordHasToBeHeld(t *testing.T) {
	tapped := `[["select",0.0],["start",0.05],["",0.3],["",0.6],["-start",0.8],["-select",0.9],["",3.0]]`

	if fired := chordFires(t, tapped); len(fired) != 0 {
		t.Errorf("a tap flipped the mode at %v, so a game pressing both buttons would take the stick away", fired)
	}
}

func TestPointerChordFiresOnceItHasBeenHeldLongEnough(t *testing.T) {
	held := `[["select",0.0],["start",0.0],["",0.5],["",0.9],["",1.1],["",1.5]]`

	fired := chordFires(t, held)
	if len(fired) != 1 {
		t.Fatalf("holding the chord flipped the mode %d times, want once: %v", len(fired), fired)
	}

	if fired[0] < 1.0 {
		t.Errorf("the mode flipped after %.2f seconds, want a full second", fired[0])
	}
}

// A hand that stays on both buttons afterwards must not toggle once a second,
// which is the difference between an override and a flicker.
func TestPointerChordDoesNotRepeatWhileItIsStillHeld(t *testing.T) {
	leaning := `[["select",0.0],["start",0.0],["",1.2],["",2.4],["",3.6],["",4.8],["",6.0]]`

	if fired := chordFires(t, leaning); len(fired) != 1 {
		t.Errorf("the mode flipped %d times while the chord was held: %v", len(fired), fired)
	}
}

// Letting go and pressing again is a second override, and the clock starts
// over rather than counting the first hold towards it.
func TestPointerChordCanBeUsedAgainAfterLettingGo(t *testing.T) {
	twice := `[["select",0.0],["start",0.0],["",1.1],["-start",1.2],["start",1.3],["",1.9],["",2.4]]`

	fired := chordFires(t, twice)
	if len(fired) != 2 {
		t.Fatalf("the chord fired %d times, want twice: %v", len(fired), fired)
	}

	if fired[1] < 2.3 {
		t.Errorf("the second override came at %.2f, so the first hold counted towards it", fired[1])
	}
}

// Half the chord is not the chord, however long it is held. Select on its own
// is a game input like any other, and so is Guide.
func TestPointerChordIgnoresOneButtonOnItsOwn(t *testing.T) {
	for name, alone := range map[string]string{
		"select": `[["select",0.0],["",1.5],["",3.0],["",9.0]]`,
		"start":  `[["start",0.0],["",1.5],["",3.0],["",9.0]]`,
		"guide":  `[["guide",0.0],["",1.5],["",3.0],["",9.0]]`,
	} {
		if fired := chordFires(t, alone); len(fired) != 0 {
			t.Errorf("%s alone flipped the mode at %v", name, fired)
		}
	}
}

// What a real client actually sends, which is not what the player pressed.
//
// Recorded in a seat: an Xbox controller on an Apple TV, Select and Start held
// together for two seconds. Moonlight builds the Guide button out of that pair
// because tvOS keeps the real one, so BTN_SELECT never arrives and the seat
// sees Start with Guide. A chord that named Select and Start could not be
// pressed at all from there, which is exactly what happened.
func TestPointerChordTakesWhatTheClientActuallySends(t *testing.T) {
	// The timings are from the recording: 315 down, 316 down 20ms later, both
	// up 1.95 seconds after that.
	recorded := `[["start",0.00],["guide",0.02],["",0.50],["",1.00],["",1.50],
		["-start",1.95],["-guide",1.95]]`

	if fired := chordFires(t, recorded); len(fired) != 1 {
		t.Errorf("Start with Guide flipped the mode %d times, want once: %v", len(fired), fired)
	}
}

// The other order the same client produces: Select goes down first and keeps
// its own code, and Guide arrives instead of Start.
func TestPointerChordTakesSelectWithGuideToo(t *testing.T) {
	recorded := `[["select",0.00],["guide",0.02],["",0.60],["",1.20],["-guide",1.60],["-select",1.61]]`

	if fired := chordFires(t, recorded); len(fired) != 1 {
		t.Errorf("Select with Guide flipped the mode %d times, want once: %v", len(fired), fired)
	}
}

// Start on its own is Enter, which is the binding that confirms a dialog when
// there is no keyboard in the room.
func TestPointerStartOnItsOwnIsAPress(t *testing.T) {
	pressed := `[["start",0.0],["-start",0.2],["",0.5]]`

	run := replayChord(t, pressed)
	if len(run.Tapped) != 1 || run.Tapped[0] != "start" {
		t.Errorf("pressing Start on its own gave %v, want one press of Start", run.Tapped)
	}

	if len(run.Fired) != 0 {
		t.Errorf("pressing Start on its own also flipped the mode at %v", run.Fired)
	}
}

// The reason Start acts on the way up. Every switch by hand holds it, and a key
// sent while it is down would type an Enter into whatever is in front - a
// dialog confirmed by somebody who was only reaching for the pointer.
func TestPointerStartTypesNothingWhenItIsPartOfTheChord(t *testing.T) {
	for name, script := range map[string]string{
		"a chord that took":     `[["start",0.0],["select",0.05],["",1.2],["-start",1.5],["-select",1.6],["",2.0]]`,
		"a chord let go early":  `[["start",0.0],["select",0.05],["-select",0.4],["-start",0.5],["",1.0]]`,
		"the client's version":  `[["start",0.0],["guide",0.02],["",1.2],["-start",1.95],["-guide",1.95]]`,
		"Start released second": `[["select",0.0],["start",0.05],["-select",0.3],["-start",0.6],["",1.0]]`,
	} {
		if run := replayChord(t, script); len(run.Tapped) != 0 {
			t.Errorf("%s: typed %v, want nothing", name, run.Tapped)
		}
	}
}

// The driver for the button mapping: it asks the helper what each face button
// does, by the label printed on the controller rather than by evdev's name for
// it. That distinction is the whole point. evdev calls the X button BTN_NORTH
// and the Y button BTN_WEST, which read like positions and are not.
const mappingDriver = `
import importlib.util, json, sys

spec = importlib.util.spec_from_file_location("padpointer", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

e = module.ecodes
face = {"A": e.BTN_SOUTH, "B": e.BTN_EAST, "X": e.BTN_NORTH, "Y": e.BTN_WEST,
        "LB": e.BTN_TL, "RB": e.BTN_TR, "Start": e.BTN_START}
clicks = {e.BTN_LEFT: "left click", e.BTN_RIGHT: "right click",
          e.BTN_MIDDLE: "middle click"}
keys = {e.KEY_ENTER: "Enter", e.KEY_ESC: "Escape",
        e.KEY_BACKSPACE: "Backspace", e.KEY_TAB: "Tab"}

answer = {}
for label, code in face.items():
    if code in module.CLICKS:
        answer[label] = clicks[module.CLICKS[code]]
    elif code in module.KEYS:
        answer[label] = keys[module.KEYS[code]]
    elif code in module.TAPS:
        answer[label] = keys[module.TAPS[code]]

print(json.dumps(answer))
`

// buttonMapping reports what the helper does with each labelled face button.
func buttonMapping(t *testing.T) map[string]string {
	t.Helper()

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("SKIPPED: no python3, so the helper's behaviour is unverified here")
	}

	path := filepath.Join(t.TempDir(), "pad-pointer.py")
	if err := os.WriteFile(path, asset("assets/pad-pointer.py"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(python, "-c", mappingDriver, path).Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && len(exit.Stderr) > 0 {
			t.Skipf("SKIPPED: the helper could not be loaded here: %s", exit.Stderr)
		}

		t.Fatal(err)
	}

	var answer map[string]string
	if err := json.Unmarshal(out, &answer); err != nil {
		t.Fatalf("the driver printed %q", out)
	}

	return answer
}

// What the desktop pad-to-mouse tools do, JoyXoff among them, and what the
// console convention already trains: A clicks, X right clicks, B goes back, Y
// confirms.
var padMapping = map[string]string{
	"A":     "left click",
	"X":     "right click",
	"B":     "Escape",
	"Y":     "Enter",
	"LB":    "Backspace",
	"RB":    "Tab",
	"Start": "Enter",
}

func TestPointerUsesTheFamiliarButtonMapping(t *testing.T) {
	got := buttonMapping(t)

	for label, want := range padMapping {
		if got[label] != want {
			t.Errorf("%s does %q, want %q", label, got[label], want)
		}
	}
}

// The bug this is really here for. The mapping was right in the code and
// backwards in the help text - Escape was on X and the terminal said it was on
// Y - because evdev's BTN_NORTH is the X button and reads like it is not.
// Nobody holding the controller can tell which of the two is wrong, so the two
// are checked against each other.
func TestPointerHelpTextAgreesWithTheButtons(t *testing.T) {
	welcome := string(asset("assets/welcome.sh"))

	for label, want := range padMapping {
		line := regexp.MustCompile(`(?m)^\s*printf '\s+` + label + `\s\s+(.+?)\\n'`)

		found := line.FindStringSubmatch(welcome)
		if found == nil {
			t.Errorf("the welcome text says nothing about %s", label)

			continue
		}

		if got := strings.TrimSpace(found[1]); got != want {
			t.Errorf("the welcome text says %s does %q, the helper does %q", label, got, want)
		}
	}
}

// The driver for the speed the daemon writes into a seat: it points the helper's
// configuration path at a file built for the test and asks what it reads.
const speedDriver = `
import importlib.util, json, sys

spec = importlib.util.spec_from_file_location("padpointer", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

module.CONFIG = sys.argv[2]
print(json.dumps(module.configured_speed()))
`

// readSpeed writes a configuration file and reports what the helper makes of it.
func readSpeed(t *testing.T, contents string) any {
	t.Helper()

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("SKIPPED: no python3, so the helper's behaviour is unverified here")
	}

	dir := t.TempDir()

	script := filepath.Join(dir, "pad-pointer.py")
	if err := os.WriteFile(script, asset("assets/pad-pointer.py"), 0o644); err != nil {
		t.Fatal(err)
	}

	conf := filepath.Join(dir, "pointer.conf")
	if contents != "" {
		if err := os.WriteFile(conf, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, err := exec.Command(python, "-c", speedDriver, script, conf).Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && len(exit.Stderr) > 0 {
			t.Skipf("SKIPPED: the helper could not be loaded here: %s", exit.Stderr)
		}

		t.Fatal(err)
	}

	var answer any
	if err := json.Unmarshal(out, &answer); err != nil {
		t.Fatalf("the driver printed %q", out)
	}

	return answer
}

// What the daemon actually writes, comments and all.
func TestPointerReadsTheSpeedTheDaemonWrote(t *testing.T) {
	conf := "# Generated by polyseatd from the seat's settings. Edits are overwritten.\n" +
		"#\n# How much of the screen the gamepad pointer crosses in a second.\nspeed=0.75\n"

	if got := readSpeed(t, conf); got != 0.75 {
		t.Errorf("read %v, want 0.75", got)
	}
}

// Nothing rather than a wrong answer, so the helper keeps its own default. A
// seat with no file is the ordinary case for one provisioned before this
// existed, and a pointer that stops working because of a malformed line would
// leave somebody on a television with no way to fix it.
func TestPointerKeepsItsDefaultWhenTheFileSaysNothingUsable(t *testing.T) {
	for name, conf := range map[string]string{
		"no file at all":    "",
		"empty file":        "\n\n",
		"only comments":     "# nothing here\n",
		"another key":       "curve=2.5\n",
		"not a number":      "speed=fast\n",
		"blank value":       "speed=\n",
		"below the minimum": "speed=0.01\n",
		"above the maximum": "speed=9.0\n",
		"negative":          "speed=-0.5\n",
	} {
		if got := readSpeed(t, conf); got != nil {
			t.Errorf("%s: read %v, want nothing", name, got)
		}
	}
}
