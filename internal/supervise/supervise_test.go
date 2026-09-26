package supervise

import (
	"os"
	"strings"
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

// The lines a helper writes just before it exits are the ones that say why it
// did, and they are the ones that went missing: Wait closed the pipe while the
// readers were still behind. A slow OnOutput makes the readers fall behind on
// purpose, and the child writes all of it at once and exits at once.
func TestTheLastLinesBeforeAnExitArrive(t *testing.T) {
	const want = 500

	var mu sync.Mutex

	var got []string

	p := New([]string{"/bin/sh", "-c", `i=1; while [ $i -le 500 ]; do echo "line $i"; i=$((i+1)); done; printf 'no newline at the end'`})
	p.OnOutput = func(line string) {
		time.Sleep(200 * time.Microsecond)
		mu.Lock()
		got = append(got, line)
		mu.Unlock()
	}

	if err := p.runOnce(t.Context()); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(got) != want+1 {
		t.Fatalf("got %d lines of %d", len(got), want+1)
	}

	if got[want-1] != "line 500" || got[want] != "no newline at the end" {
		t.Errorf("the last two lines are %q and %q", got[want-1], got[want])
	}
}

// A line longer than maxLine is passed on in pieces rather than stopping the
// reading, which would leave the child blocked on a full pipe for good.
func TestAnEndlessLineDoesNotStopTheReading(t *testing.T) {
	var total, count int

	p := New([]string{"/bin/sh", "-c", `head -c 600000 /dev/zero | tr '\0' x; echo; echo after`})
	p.OnOutput = func(line string) {
		total += len(line)
		count++
	}

	done := make(chan error, 1)
	go func() { done <- p.runOnce(t.Context()) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the child never finished, so the reading stopped")
	}

	if total != 600000+len("after") || count != 4 {
		t.Errorf("got %d bytes in %d lines", total, count)
	}
}

// slowToLeave is a child that takes 0.3 seconds to go on SIGTERM and writes
// "start" and "end" into log, so that a test has a window to arrive in and a
// record of how many children were alive at once.
func slowToLeave(log string) *Process {
	return New([]string{"/bin/sh", "-c",
		`echo start >> "$0"; trap 'sleep 0.3; echo end >> "$0"; exit 0' TERM; while :; do sleep 0.05; done`, log})
}

// A Start that arrives while a Stop is still waiting for the child to go must
// not launch a second child beside it, and the Stop must not then mark the new
// one stopped.
func TestStartDuringStopWaitsForTheStop(t *testing.T) {
	log := t.TempDir() + "/log"
	p := slowToLeave(log)

	p.Start()
	waitForLines(t, log, 1)

	stopped := make(chan struct{})
	go func() { p.Stop(); close(stopped) }()

	// Inside the 0.3 seconds the child spends leaving.
	time.Sleep(100 * time.Millisecond)
	p.Start()
	<-stopped

	waitForLines(t, log, 3)

	raw, _ := os.ReadFile(log)
	if got := strings.Fields(string(raw)); strings.Join(got, " ") != "start end start" {
		t.Errorf("the children ran as %v, which is two at once", got)
	}

	// Still running after the Stop has returned, and staying so.
	time.Sleep(100 * time.Millisecond)

	if s := p.State(); s != Running {
		t.Errorf("state is %q after the Start that followed the Stop", s)
	}

	p.Stop()
}

// A second Stop arriving while the first still waits has to wait as well,
// because a caller relies on the child being gone when Stop returns.
func TestASecondStopWaitsToo(t *testing.T) {
	log := t.TempDir() + "/log"
	p := slowToLeave(log)

	p.Start()
	waitForLines(t, log, 1)

	go p.Stop()

	time.Sleep(100 * time.Millisecond)
	p.Stop()

	raw, _ := os.ReadFile(log)
	if got := strings.Fields(string(raw)); len(got) != 2 {
		t.Errorf("the second Stop returned with the child still there: %v", got)
	}
}

// waitForLines waits until the file has at least n lines.
func waitForLines(t *testing.T, path string, n int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(path)
		if len(strings.Fields(string(raw))) >= n {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("%s never reached %d lines", path, n)
}
