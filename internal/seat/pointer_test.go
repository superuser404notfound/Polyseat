package seat

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
