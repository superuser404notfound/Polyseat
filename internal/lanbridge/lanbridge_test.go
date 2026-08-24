package lanbridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// script writes a stand-in for polyseat-lan-bridge and points the lookup at it.
//
// The real one cannot run here: it needs root, NetworkManager and an Incus, and
// it takes this machine off the network in the middle. What is worth testing is
// everything around it, and one thing that is not around it at all — that the
// seats are put back whether the script succeeded or failed, which is the only
// behaviour in this package the script does not already have.
func script(t *testing.T, body string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, Name)

	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}

	old := Dirs
	Dirs = []string{dir}

	t.Cleanup(func() { Dirs = old })

	return path
}

// waitIdle waits for the run to finish. Running goes false in the same locked
// section that records the outcome, so anything read after this has settled.
func waitIdle(t *testing.T, r *Runner) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)

	for r.Running() {
		if time.Now().After(deadline) {
			t.Fatal("the run never finished")
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func TestCommandFindsTheScript(t *testing.T) {
	want := script(t, "true\n")

	got, err := Command()
	if err != nil {
		t.Fatalf("did not find %s: %v", Name, err)
	}

	if got != want {
		t.Errorf("found %q, wanted %q", got, want)
	}
}

// The error is the whole answer a checkout install gets, since that installer
// did not place this command until the interface learned to run it. It has to
// say both where it looked and what to type instead.
func TestCommandSaysWhereItLookedAndWhatToRun(t *testing.T) {
	old := Dirs
	Dirs = []string{t.TempDir()}

	defer func() { Dirs = old }()

	_, err := Command()
	if err == nil {
		t.Fatal("found something in an empty directory")
	}

	if !strings.Contains(err.Error(), Dirs[0]) {
		t.Errorf("the error does not say where it looked: %v", err)
	}

	if !strings.Contains(err.Error(), "./host/lan-bridge.sh") {
		t.Errorf("the error does not say what to run instead: %v", err)
	}
}

// The direction is the only thing this package tells the script, and it tells
// it by leaving one argument off. A silent slip there would put the uplink back
// on a bridge when somebody asked for the opposite.
func TestRunSaysWhichDirection(t *testing.T) {
	for _, tc := range []struct {
		name string
		undo bool
		want string
	}{
		{name: "bridging", undo: false, want: "args:"},
		{name: "undoing", undo: true, want: "args:--undo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := script(t, `echo "args:$*"`+"\n")

			var lines []string

			if err := Run(context.Background(), path, tc.undo, func(line string) {
				lines = append(lines, line)
			}); err != nil {
				t.Fatal(err)
			}

			if got := strings.Join(lines, "\n"); got != tc.want {
				t.Errorf("the script was given %q, wanted %q", got, tc.want)
			}
		})
	}
}

func TestRunReportsAFailure(t *testing.T) {
	path := script(t, "echo far enough to stop the seats\nexit 1\n")

	err := Run(context.Background(), path, false, nil)
	if err == nil {
		t.Fatal("a script that exited 1 was reported as a success")
	}

	if !strings.Contains(err.Error(), Name) {
		t.Errorf("the error does not name the script: %v", err)
	}
}

// The one that matters. A run that fails has already stopped every seat: the
// script stops them before it touches the interface, and its rollback puts the
// network back and leaves them down. Seats left down after a failure is the
// worse outcome, not the safer one, so whatever puts them back runs either way
// and says so in the log somebody is watching.
func TestTheSeatsComeBackAfterAFailedRun(t *testing.T) {
	script(t, "echo stopping seat1\nexit 1\n")

	var r Runner

	resumed := false

	if err := r.Start(false, nil, func(log func(string)) {
		resumed = true

		log("Starting the seats that were up before this:")
		log("  seat1")
	}); err != nil {
		t.Fatal(err)
	}

	waitIdle(t, &r)

	if !resumed {
		t.Fatal("the seats were not put back after a run that failed")
	}

	state := r.State()

	if state.Error == "" {
		t.Error("a run that exited 1 was reported as a success")
	}

	if state.Done {
		t.Error("a run that exited 1 was reported as done")
	}

	if !strings.Contains(strings.Join(state.Log, "\n"), "  seat1") {
		t.Errorf("the seats coming back is not in the log: %v", state.Log)
	}
}

func TestTheSeatsComeBackAfterAGoodRun(t *testing.T) {
	script(t, "echo done\n")

	var r Runner

	resumed := false

	if err := r.Start(true, nil, func(func(string)) { resumed = true }); err != nil {
		t.Fatal(err)
	}

	waitIdle(t, &r)

	if !resumed {
		t.Fatal("the seats were not put back after a run that worked")
	}

	state := r.State()

	if !state.Done || state.Error != "" {
		t.Errorf("a run that exited 0 was reported as done=%v error=%q", state.Done, state.Error)
	}

	if !state.Undo {
		t.Error("the state does not say which direction was run")
	}
}

// Two at once would take the address off one interface while the other was
// putting it on, which is a machine with its address on neither.
func TestOnlyOneRunAtATime(t *testing.T) {
	script(t, "sleep 1\n")

	var r Runner

	if err := r.Start(false, nil, nil); err != nil {
		t.Fatal(err)
	}

	if err := r.Start(true, nil, nil); err == nil {
		t.Fatal("a second run started while the first was going")
	}

	waitIdle(t, &r)
}

// NO_COLOR is honoured by the script's own helpers and by nothing it calls. In
// a terminal a stray sequence is invisible; in a browser it is printed.
func TestCleanTakesTheTerminalOut(t *testing.T) {
	got := clean("  \x1b[32m✓\x1b[0m enp4s0 is a port of br0  ")

	if want := "  ✓ enp4s0 is a port of br0"; got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}
