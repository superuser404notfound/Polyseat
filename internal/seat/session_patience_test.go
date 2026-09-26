package seat

import (
	"slices"
	"strconv"
	"testing"
	"time"
)

// Both looks at the session end in a cat of a file in the player's home. With
// a FIFO there, only a limit inside the seat ends that cat: this side's deadline
// makes the daemon stop waiting and leaves the process behind, one per sweep.
func TestALookAtTheSessionIsEndedInsideTheSeat(t *testing.T) {
	m := &Manager{rt: map[string]*runtime{}}

	for _, script := range []string{streamCheck, sessionProbe(1000)} {
		argv := m.sessionArgv("seat1", script)

		at := slices.Index(argv, "timeout")
		sh := slices.Index(argv, "sh")

		if at < 0 || sh < at {
			t.Fatalf("the script is not run under timeout: %q", argv)
		}

		limit, err := strconv.Atoi(argv[at+3])
		if err != nil {
			t.Fatalf("no limit after timeout: %q", argv)
		}

		if time.Duration(limit)*time.Second >= quickTimeout {
			t.Errorf("the seat's limit %ds is not shorter than this side's %s", limit, quickTimeout)
		}

		if argv[0] != "sudo" || argv[2] != Player {
			t.Errorf("not run as the player: %q", argv)
		}
	}
}
