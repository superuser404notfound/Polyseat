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

// runSteamPad loads polyseat-steam-pad into a Python driver and prints what the
// driver prints. The helper is run rather than transcribed, for the reason the
// pointer helper's tests give: a Go copy would only prove the copy.
//
// The driver swaps evdev's UInput for a recorder, so nothing here touches a real
// /dev/uinput.
func runSteamPad(t *testing.T, driver string, args ...string) []byte {
	t.Helper()

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("SKIPPED: no python3, so the helper's behaviour is unverified here")
	}

	script := filepath.Join(t.TempDir(), "steam-pad.py")
	if err := os.WriteFile(script, asset("assets/steam-pad.py"), 0o644); err != nil {
		t.Fatal(err)
	}

	const prelude = `
import importlib.util, json, sys

spec = importlib.util.spec_from_file_location("steampad", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
e = module.ecodes
module.log = lambda message: None
`

	out, err := exec.Command(python, append([]string{"-c", prelude + driver, script}, args...)...).Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && len(exit.Stderr) > 0 {
			if strings.Contains(string(exit.Stderr), "No module named 'evdev'") {
				t.Skip("SKIPPED: no python-evdev here, so the helper's behaviour is unverified")
			}

			t.Fatalf("the driver raised: %s", exit.Stderr)
		}

		t.Fatal(err)
	}

	return out
}

func steamInFront(t *testing.T, tree string) bool {
	t.Helper()

	out := runSteamPad(t, `print(json.dumps(module.steam_in_front(json.loads(sys.argv[2]))))`, tree)

	var answer bool
	if err := json.Unmarshal(out, &answer); err != nil {
		t.Fatalf("the driver printed %q", out)
	}

	return answer
}

// What the whole helper turns on. Steam lives in gamescope, and the only window
// sway ever sees of it is gamescope's.
func TestSteamPadSeesSteamInGamescope(t *testing.T) {
	tree := `{"type":"root","nodes":[{"type":"workspace","name":"2","nodes":[
		{"type":"con","pid":7,"app_id":"gamescope","focused":true,"fullscreen_mode":1}]}]}`

	if !steamInFront(t, tree) {
		t.Error("gamescope in front was not taken for Steam")
	}
}

// The report this exists for: Eden in front, fullscreen, with Steam on the other
// workspace.
func TestSteamPadLetsAGameOutsideSteamHaveTheController(t *testing.T) {
	tree := `{"type":"root","nodes":[
		{"type":"workspace","name":"1","nodes":[
			{"type":"con","pid":9,"app_id":null,"focused":true,"fullscreen_mode":1,
			 "window_properties":{"class":"eden"}}]},
		{"type":"workspace","name":"2","nodes":[
			{"type":"con","pid":7,"app_id":"gamescope","focused":false,"fullscreen_mode":1}]}]}`

	if steamInFront(t, tree) {
		t.Error("Steam was given the controller while Eden was in front")
	}
}

// polyseat-steam's fallback when gamescope will not start: Steam on the
// session's own X server, with a window of its own.
func TestSteamPadSeesSteamOutsideGamescope(t *testing.T) {
	tree := `{"type":"root","nodes":[{"type":"workspace","nodes":[
		{"type":"con","pid":7,"focused":true,"window_properties":{"class":"steam"}}]}]}`

	if !steamInFront(t, tree) {
		t.Error("a Steam window of its own was not taken for Steam")
	}
}

// An empty workspace is what is focused after the last window on it closes.
// That is not Steam, whatever is on the other workspace.
func TestSteamPadDoesNotTakeAnEmptyWorkspaceForSteam(t *testing.T) {
	tree := `{"type":"root","nodes":[
		{"type":"workspace","name":"1","focused":true,"nodes":[]},
		{"type":"workspace","name":"2","nodes":[
			{"type":"con","pid":7,"app_id":"gamescope","fullscreen_mode":1}]}]}`

	if steamInFront(t, tree) {
		t.Error("an empty workspace was taken for Steam")
	}
}

// The helper must not mirror its own pads, the pointer helper's devices, or
// the virtual pads Steam makes for a game: the last would feed Steam its own
// output.
func TestSteamPadOnlyFollowsControllersFromClients(t *testing.T) {
	out := runSteamPad(t, `
pad = ([e.BTN_SOUTH, e.BTN_MODE], [e.ABS_X, e.ABS_Y])
print(json.dumps({
    "sunshine": module.is_source("Sunshine (libvirtualhid) X-Box Series Controller", 0x045e, *pad),
    "ours": module.is_source("polyseat:steam pad (vince)", 0x046d, *pad),
    "valve": module.is_source("Microsoft X-Box 360 pad 0", 0x28de, *pad),
    "keyboard": module.is_source("libvirtualhid Keyboard", 0x1209, [e.KEY_A], []),
}))
`)

	var got map[string]bool
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("the driver printed %q", out)
	}

	want := map[string]bool{"sunshine": true, "ours": false, "valve": false, "keyboard": false}
	for name, follow := range want {
		if got[name] != follow {
			t.Errorf("%s: followed %v, want %v", name, got[name], follow)
		}
	}
}

// The driver for a Mirror: a source pad with a scripted state and a Steam pad
// that records what it is told.
const mirrorDriver = `
from types import SimpleNamespace as NS

written = []

class Pad:
    fd = 99
    def __init__(self, events, **kw):
        self.events, self.kw = events, kw
    def write(self, kind, code, value):
        written.append([e.EV[kind] if kind in e.EV else kind, code, value])
    def syn(self):
        written.append(["SYN"])
    def close(self):
        pass

module.evdev.UInput = Pad

stick = NS(min=-32768, max=32767, value=12000)
trigger = NS(min=0, max=1023, value=700)
hat = NS(min=-1, max=1, value=-1)
values = {e.ABS_X: stick, e.ABS_Z: trigger, e.ABS_HAT0X: hat}

class Source:
    fd, path, name, ff_effects_count = 5, "/dev/input/event5", "Sunshine pad", 0
    def capabilities(self, absinfo=False):
        return {e.EV_KEY: [e.BTN_SOUTH, e.BTN_MODE], e.EV_ABS: list(values.items())}
    def active_keys(self):
        return [e.BTN_SOUTH]
    def absinfo(self, code):
        return values[code]

mirror = module.Mirror(Source(), "vince", None)
event = lambda kind, code, value: NS(type=kind, code=code, value=value)
steps = sys.argv[2].split(",")

for step in steps:
    if step == "press":
        mirror.forward(event(e.EV_KEY, e.BTN_MODE, 1))
    elif step == "on":
        mirror.activate()
    elif step == "off":
        mirror.deactivate()
    elif step == "mark":
        written.append(["MARK"])

print(json.dumps({"written": written, "name": mirror.pad.kw["name"],
                  "id": [mirror.pad.kw["vendor"], mirror.pad.kw["product"]]}))
`

type mirrorRun struct {
	Written [][]any `json:"written"`
	Name    string  `json:"name"`
	ID      []int   `json:"id"`
}

func runMirror(t *testing.T, steps string) mirrorRun {
	t.Helper()

	var run mirrorRun
	if err := json.Unmarshal(runSteamPad(t, mirrorDriver, steps), &run); err != nil {
		t.Fatal(err)
	}

	return run
}

// The point of it all: with something else in front, the Xbox button, or any
// other, does not reach Steam.
func TestSteamPadIsSilentWhileSomethingElseIsInFront(t *testing.T) {
	run := runMirror(t, "press")

	if len(run.Written) != 0 {
		t.Errorf("the Steam pad carried %v while Steam was not in front", run.Written)
	}
}

func TestSteamPadCarriesTheControllerWhileSteamIsInFront(t *testing.T) {
	run := runMirror(t, "on,mark,press")

	tail := run.Written[len(run.Written)-1]
	if len(tail) != 3 || tail[0] != "EV_KEY" || int(tail[1].(float64)) != 0x13c || int(tail[2].(float64)) != 1 {
		t.Errorf("the Xbox button did not reach Steam while it was in front: %v", run.Written)
	}
}

// Coming to the front hands Steam the controller as it is, held buttons and
// stick included, rather than as if nothing were touched.
func TestSteamPadStartsFromWhatIsHeldNow(t *testing.T) {
	run := runMirror(t, "on")

	want := [][]any{
		{"EV_KEY", float64(0x130), float64(1)}, // A, held
		{"EV_KEY", float64(0x13c), float64(0)}, // Xbox, not held
		{"EV_ABS", float64(0x00), float64(12000)},
		{"EV_ABS", float64(0x02), float64(700)},
		{"EV_ABS", float64(0x10), float64(-1)},
		{"SYN"},
	}

	if !sameWrites(run.Written, want) {
		t.Errorf("Steam was handed %v, want %v", run.Written, want)
	}
}

// Leaving lets go of everything, so nothing stays held in Steam until it is in
// front again: sticks to their centre, the trigger and the D-pad to rest.
func TestSteamPadLetsGoWhenSteamLeavesTheFront(t *testing.T) {
	run := runMirror(t, "on,mark,off")

	var after [][]any
	for i, w := range run.Written {
		if len(w) == 1 && w[0] == "MARK" {
			after = run.Written[i+1:]
		}
	}

	want := [][]any{
		{"EV_KEY", float64(0x130), float64(0)},
		{"EV_KEY", float64(0x13c), float64(0)},
		{"EV_ABS", float64(0x00), float64(-1)}, // the centre of -32768..32767
		{"EV_ABS", float64(0x02), float64(0)},
		{"EV_ABS", float64(0x10), float64(0)},
		{"SYN"},
	}

	if !sameWrites(after, want) {
		t.Errorf("leaving the front wrote %v, want %v", after, want)
	}
}

func sameWrites(got, want [][]any) bool {
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)

	return string(g) == string(w)
}

// The pointer helper reads every pad. Ours carries the same presses as the
// client's while Steam is in front, so it has to keep away from it.
func TestPointerHelperLeavesTheSteamPadAlone(t *testing.T) {
	run := runMirror(t, "")

	if !strings.HasPrefix(run.Name, "polyseat:") {
		t.Fatalf("the Steam pad is called %q, which the pointer helper does not skip", run.Name)
	}

	pointer := string(asset("assets/pad-pointer.py"))
	if !strings.Contains(pointer, `.startswith("polyseat:")`) {
		t.Error("the pointer helper no longer skips devices called polyseat:")
	}
}

// Four places name the Steam pad: the helper that makes it, the exception
// polyseat-steam starts Steam with, and the list both session units hand
// everything else. If they drift apart, Steam hears no controller at all, or
// every game outside it finds a silent one.
func TestEverythingAgreesOnWhichPadIsSteams(t *testing.T) {
	run := runMirror(t, "")
	id := "0x" + hex4(run.ID[0]) + "/0x" + hex4(run.ID[1])

	script := string(asset("assets/steam.sh"))
	if m := regexp.MustCompile(`(?m)^STEAM_PAD=(\S+)$`).FindStringSubmatch(script); m == nil || m[1] != id {
		t.Errorf("polyseat-steam points Steam at %v, the helper makes %s", m, id)
	}

	for _, unit := range []string{"assets/polyseat-sway.service", "assets/polyseat-sunshine.service"} {
		want := "Environment=SDL_GAMECONTROLLER_IGNORE_DEVICES=" + id + "\n"
		if !strings.Contains(string(asset(unit)), want) {
			t.Errorf("%s does not tell its children to ignore %s", unit, id)
		}
	}
}

func hex4(n int) string {
	const digits = "0123456789abcdef"

	return string([]byte{digits[n>>12&15], digits[n>>8&15], digits[n>>4&15], digits[n&15]})
}

// The session starts the helper, again after it dies like the pointer helper,
// and provisioning puts it where that line looks.
func TestTheSessionKeepsTheSteamPadRunning(t *testing.T) {
	config := string(asset("assets/sway.config"))
	if !regexp.MustCompile(`(?m)^exec sh -c 'while :; do .*/usr/local/bin/polyseat-steam-pad && exit 0;.*'$`).MatchString(config) {
		t.Error("the session does not start polyseat-steam-pad in a restart loop")
	}

	source, err := os.ReadFile("provision.go")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(source), `{"/usr/local/bin/polyseat-steam-pad", asset("assets/steam-pad.py")}`) {
		t.Error("provisioning does not install polyseat-steam-pad")
	}
}

// Steam is pointed at the Steam pad, and the list the session hands everything
// else is taken away from it: a Steam that ignored its own pad would hear
// nothing.
func TestSteamListensOnlyToTheSteamPad(t *testing.T) {
	started, _ := runSteamScript(t, seatState{steamPad: true})

	if !strings.Contains(started, "pad-except=0x046d/0xc21d ") {
		t.Errorf("Steam was not pointed at the Steam pad: %q", started)
	}

	if !strings.HasSuffix(strings.TrimSpace(started), "pad-ignore=") {
		t.Errorf("Steam kept the list that hides its own pad: %q", started)
	}
}

// Without the helper, or without the uinput it needs, there is no Steam pad,
// and Steam is left to read the controller the way it always did.
func TestSteamKeepsTheControllerWhereThereIsNoSteamPad(t *testing.T) {
	for name, state := range map[string]seatState{
		"no helper": {},
		"no uinput": {noUinput: true},
	} {
		started, _ := runSteamScript(t, state)

		if !strings.Contains(started, "pad-except= ") {
			t.Errorf("%s: Steam was pointed at a pad nobody keeps: %q", name, started)
		}
	}
}
