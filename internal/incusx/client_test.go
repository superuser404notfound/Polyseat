package incusx

import (
	"context"
	"errors"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
)

// The bound exists so that an operation whose result never arrives fails instead
// of waiting for ever. It must not shorten a caller that set its own deadline:
// provisioning installs packages for minutes at a time and passes a context of
// its own, and a three minute ceiling applied to that would kill a working run.
func TestBoundedLeavesACallersOwnDeadlineAlone(t *testing.T) {
	own := 20 * time.Minute

	ctx, cancel := context.WithTimeout(context.Background(), own)
	defer cancel()

	got, stop := bounded(ctx, opTimeout)
	defer stop()

	deadline, ok := got.Deadline()
	if !ok {
		t.Fatal("the deadline was dropped")
	}

	if left := time.Until(deadline); left < own-time.Minute {
		t.Errorf("the caller's %s deadline was shortened to %s", own, left.Round(time.Second))
	}
}

// And it has to add one when there is none, which is the case that hung: the
// manager runs its operations on a context with cancellation and no deadline.
func TestBoundedAddsADeadlineWhenThereIsNone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, ok := ctx.Deadline(); ok {
		t.Fatal("this test needs a context without a deadline")
	}

	got, stop := bounded(ctx, opTimeout)
	defer stop()

	deadline, ok := got.Deadline()
	if !ok {
		t.Fatal("no deadline was added, so a stalled operation would wait for ever")
	}

	if left := time.Until(deadline); left > opTimeout+time.Second {
		t.Errorf("the deadline is %s away, want about %s", left.Round(time.Second), opTimeout)
	}
}

// Cancelling still has to reach through it, or cancelling a provisioning run
// would stop reporting and keep going.
func TestBoundedStillCarriesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	got, stop := bounded(ctx, opTimeout)
	defer stop()

	cancel()

	select {
	case <-got.Done():
	case <-time.After(time.Second):
		t.Error("cancelling the caller's context did not reach the bounded one")
	}
}

// stalled replaces the connection only for the failure that means the connection
// is the problem. Anything else has to pass through untouched, or a container
// that genuinely refused to start would take the connection down with it every
// time.
func TestStalledOnlyReactsToATimedOutWait(t *testing.T) {
	dials := 0

	c := &Client{dial: func() (incus.InstanceServer, error) {
		dials++

		return nil, errors.New("not a real connection, and not needed for this")
	}}

	if err := c.stalled(nil); err != nil {
		t.Errorf("a success became %v", err)
	}

	ordinary := context.Canceled
	if err := c.stalled(ordinary); err != ordinary {
		t.Errorf("an ordinary error came back as %v", err)
	}

	if dials != 0 {
		t.Errorf("reconnected %d times for errors that are not a stalled wait", dials)
	}

	if err := c.stalled(context.DeadlineExceeded); err != context.DeadlineExceeded {
		t.Errorf("the timeout came back as %v", err)
	}

	if dials != 1 {
		t.Errorf("reconnected %d times for a stalled wait, want once", dials)
	}
}

// And the error still comes back after the repair. Swallowing it would leave the
// caller believing the operation had worked.
func TestStalledReportsTheTimeoutItRepairedFor(t *testing.T) {
	c := &Client{}

	err := c.stalled(context.DeadlineExceeded)
	if err != context.DeadlineExceeded {
		t.Errorf("returned %v, want the deadline error", err)
	}
}
