package supervise

import (
	"sync"
	"testing"
	"time"
)

// waitForState blocks until the process reaches want, or fails the test.
//
// Polled rather than driven off OnState, because the interesting assertion in
// these tests is that a state is reached and then *stays*, and a callback can
// only tell you it happened once.
func waitForState(t *testing.T, p *Process, want State, within time.Duration) {
	t.Helper()

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if p.State() == want {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatalf("state is %q after %s, want %q", p.State(), within, want)
}

// countingProcess is a child that records how often it was started.
//
// /bin/sh rather than a Go test binary re-executing itself: the thing under
// test is exit codes, and a shell is the shortest way to produce an exact one.
func countingProcess(t *testing.T, code string) (*Process, func() int) {
	t.Helper()

	var mu sync.Mutex

	starts := 0

	p := New([]string{"/bin/sh", "-c", "exit " + code})
	p.OnState = func(s State) {
		if s == Running {
			mu.Lock()
			starts++
			mu.Unlock()
		}
	}

	return p, func() int {
		mu.Lock()
		defer mu.Unlock()

		return starts
	}
}

func TestFatalExitStopsRestarting(t *testing.T) {
	p, starts := countingProcess(t, "3")
	p.Fatal = func(code int) bool { return code == 3 }

	p.Start()
	defer p.Stop()

	waitForState(t, p, Failed, 2*time.Second)

	// The point of Failed is that nothing happens afterwards. Backoff starts at
	// a second, so half of one is long enough for a restart to show up if the
	// give-up did not take.
	got := starts()
	time.Sleep(500 * time.Millisecond)

	if again := starts(); again != got {
		t.Errorf("started %d more times after failing", again-got)
	}

	if p.State() != Failed {
		t.Errorf("state is %q, want %q", p.State(), Failed)
	}
}

// The mutation that matters: a Fatal that answers for the wrong code must not
// stop anything. Without this, a Fatal returning true unconditionally would
// pass the test above and break every broker on the machine.
func TestOtherExitCodesStillRestart(t *testing.T) {
	p, starts := countingProcess(t, "1")
	p.Fatal = func(code int) bool { return code == 3 }

	p.Start()
	defer p.Stop()

	waitForState(t, p, Restarting, 2*time.Second)

	if p.State() == Failed {
		t.Fatal("gave up on exit 1, which Fatal did not claim")
	}

	if n := starts(); n < 1 {
		t.Errorf("started %d times, want at least 1", n)
	}
}

// Nil Fatal is what every broker uses, so it has to keep the old behaviour
// exactly. This is the check that would catch a nil dereference in the new
// branch, which is the obvious way to break it.
func TestNilFatalRestartsEverything(t *testing.T) {
	p, _ := countingProcess(t, "3")

	p.Start()
	defer p.Stop()

	waitForState(t, p, Restarting, 2*time.Second)

	if p.State() == Failed {
		t.Error("gave up with no Fatal set")
	}
}

// A process that exits 0 is still a process that stopped, and the supervisor
// restarts it. Included because Fatal is asked about an ExitError and a clean
// exit produces none, so this is the path where errors.As has to fall through.
func TestCleanExitIsNotFatal(t *testing.T) {
	p, _ := countingProcess(t, "0")
	p.Fatal = func(code int) bool { return true }

	p.Start()
	defer p.Stop()

	waitForState(t, p, Restarting, 2*time.Second)

	if p.State() == Failed {
		t.Error("gave up on a clean exit, which produces no exit code to judge")
	}
}
