package seat

import (
	"encoding/json"
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
const watchDriver = `
import importlib.util, json, sys

spec = importlib.util.spec_from_file_location("watch", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

question, payload = sys.argv[2], json.loads(sys.argv[3])

if question == "worth":
    print(json.dumps(module.worth_looking(payload)))
else:
    print(json.dumps(module.windowed_big_picture(payload)))
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

func worthLooking(t *testing.T, event string) bool {
	t.Helper()

	var answer bool
	if err := json.Unmarshal(askWatcher(t, "worth", event), &answer); err != nil {
		t.Fatalf("the driver printed something unreadable")
	}

	return answer
}

// The reported case, in the words sway uses for it: a game ends, and the event
// is the closing of a window that was fullscreen.
func TestTheWatcherLooksWhenAFullscreenWindowCloses(t *testing.T) {
	event := `{"change":"close","container":{"id":7,"name":"Hades","fullscreen_mode":1}}`

	if !worthLooking(t, event) {
		t.Error("a fullscreen window closing was ignored, which is exactly when Big Picture is left tiled")
	}
}

// An ordinary window closing is not a game ending, and looking at the tree for
// every terminal somebody closes is work for nothing.
func TestTheWatcherIgnoresAnOrdinaryWindowClosing(t *testing.T) {
	event := `{"change":"close","container":{"id":7,"name":"foot","fullscreen_mode":0}}`

	if worthLooking(t, event) {
		t.Error("closing a tiled window sends the watcher to the tree for no reason")
	}
}

// The cold Steam case, which the sway rule cannot catch: the window is mapped
// as "Steam" and renamed a moment later, so a rule matching on the title never
// fires for it.
func TestTheWatcherLooksWhenAWindowIsRenamedToBigPicture(t *testing.T) {
	event := `{"change":"title","container":{"id":9,"name":"Steam Big Picture Mode","fullscreen_mode":0}}`

	if !worthLooking(t, event) {
		t.Error("a window renamed to Big Picture was ignored, so a cold start stays in a corner")
	}
}

// And once it is fullscreen there is nothing to do, whatever else it says.
func TestTheWatcherLeavesAFullscreenBigPictureAlone(t *testing.T) {
	event := `{"change":"title","container":{"id":9,"name":"Steam Big Picture Mode","fullscreen_mode":1}}`

	if worthLooking(t, event) {
		t.Error("a Big Picture that is already fullscreen was still worth looking at")
	}
}

// The one this must never react to. fullscreen_mode changing on a window that
// stays is somebody pressing $mod+f, and a helper that undoes that is a helper
// that has to be killed before the seat can be used.
func TestTheWatcherDoesNotUndoSomebodyLeavingFullscreen(t *testing.T) {
	event := `{"change":"fullscreen_mode","container":{"id":9,"name":"Steam Big Picture Mode","fullscreen_mode":0}}`

	if worthLooking(t, event) {
		t.Error("leaving fullscreen by hand would be undone, so the keybinding does not work")
	}
}

func target(t *testing.T, tree string) any {
	t.Helper()

	var answer any
	if err := json.Unmarshal(askWatcher(t, "target", tree), &answer); err != nil {
		t.Fatalf("the driver printed something unreadable")
	}

	return answer
}

// What it does with the tree afterwards: find the window, and only when it is
// really windowed.
func TestTheWatcherFindsTheWindowedBigPicture(t *testing.T) {
	tree := `{"type":"root","nodes":[{"type":"output","nodes":[
		{"type":"workspace","name":"1","nodes":[
			{"type":"con","id":3,"pid":11,"name":"foot","fullscreen_mode":0},
			{"type":"con","id":4,"pid":12,"name":"Steam Big Picture Mode","fullscreen_mode":0}]}]}]}`

	if got := target(t, tree); got != float64(4) {
		t.Errorf("the watcher would fullscreen %v, want the Big Picture window, id 4", got)
	}

	full := strings.Replace(tree, `"name":"Steam Big Picture Mode","fullscreen_mode":0`,
		`"name":"Steam Big Picture Mode","fullscreen_mode":1`, 1)

	if got := target(t, full); got != nil {
		t.Errorf("a Big Picture that is already fullscreen came back as %v, so sway would be asked for nothing", got)
	}

	none := `{"type":"root","nodes":[{"type":"output","nodes":[
		{"type":"workspace","name":"1","nodes":[
			{"type":"con","id":3,"pid":11,"name":"foot","fullscreen_mode":0}]}]}]}`

	if got := target(t, none); got != nil {
		t.Errorf("a tree with no Big Picture in it answered %v", got)
	}
}

// A window with no pid is a sway container and not somebody's window. Matching
// on the name alone would let a workspace called after the title stand in for
// it, which is the mistake the pointer helper already had to fix once.
func TestTheWatcherNeedsARealWindow(t *testing.T) {
	// The id is here on purpose. Without it this test passed against a helper
	// that had lost the pid check entirely, because the node it then matched
	// had nothing to return.
	tree := `{"type":"root","nodes":[{"type":"output","nodes":[
		{"type":"workspace","id":2,"name":"Steam Big Picture Mode","fullscreen_mode":0,"nodes":[]}]}]}`

	if got := target(t, tree); got != nil {
		t.Errorf("a workspace named after the window was taken for it: %v", got)
	}
}

// The three places that name the window have to agree, or the one that is
// wrong silently does nothing at all.
func TestEverythingAgreesOnWhatBigPictureIsCalled(t *testing.T) {
	const title = "Steam Big Picture Mode"

	for _, file := range []string{
		"assets/bigpicture-watch.py",
		"assets/bigpicture.sh",
		"assets/sway.config",
	} {
		if !strings.Contains(string(asset(file)), title) {
			t.Errorf("%s does not mention %q, so it matches a window nobody has", file, title)
		}
	}
}
