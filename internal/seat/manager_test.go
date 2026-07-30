package seat

import (
	"errors"
	"strings"
	"testing"
)

// What the banner in the interface offers to fix, and the list the sweep works
// through. Getting it wrong in the quiet direction, leaving a seat out, is a
// seat that stays behind while the interface says everything is up to date.
func TestStaleSeatsAreTheOnesAnotherGenerationBuilt(t *testing.T) {
	seats := []Seat{
		{Name: "current", Provisioned: Generation},
		{Name: "behind", Provisioned: Generation - 1},
		{Name: "ahead", Provisioned: Generation + 1},
		{Name: "also-current", Provisioned: Generation},
	}

	got := staleSeats(seats)

	want := []string{"behind", "ahead"}
	if len(got) != len(want) {
		t.Fatalf("picked %v, want %v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("picked %v, want %v, and in the order the seats are shown", got, want)
		}
	}
}

// A seat nobody has built yet is not out of date, it is new, and starting it
// builds it. Counting it here was how the interface came to open with "seat vince
// was built by an older version of the daemon" above a seat created a minute
// earlier, next to a row saying it was out of date and a button offering to
// provision something that had never been provisioned.
func TestStaleSeatsLeavesANeverBuiltSeatAlone(t *testing.T) {
	seats := []Seat{
		{Name: "brand-new", Provisioned: 0},
		{Name: "behind", Provisioned: Generation - 1},
	}

	got := staleSeats(seats)

	if len(got) != 1 || got[0] != "behind" {
		t.Errorf("picked %v, want only behind", got)
	}
}

// Nothing to do has to be nothing to do, or the interface would offer a button
// that provisions every seat on the machine for no reason.
func TestStaleSeatsFindsNothingWhenEverythingIsCurrent(t *testing.T) {
	if got := staleSeats([]Seat{{Name: "a", Provisioned: Generation}}); len(got) != 0 {
		t.Errorf("picked %v, want nothing", got)
	}
}

// The bug this was written for. Both seats had been started five seconds before
// the sweep began, so both were busy, and the first version called Provision
// straight away, got "busy" back, wrote a note on each seat and reported that it
// had swept two seats. Neither was touched.
func TestSweepWaitsForASeatThatIsBusyRatherThanSkippingIt(t *testing.T) {
	// Busy for the first two looks, then free, which is what a seat coming up
	// does.
	looks := 0
	busy := func(string) string {
		looks++
		if looks <= 2 {
			return "starting"
		}

		return ""
	}

	var provisioned []string

	sweep([]string{"seat1"}, busy,
		func(name string) error { provisioned = append(provisioned, name); return nil },
		func(string, string) {},
		func() {})

	if len(provisioned) != 1 {
		t.Fatalf("provisioned %v, want seat1 once: a seat that was busy for a moment was skipped", provisioned)
	}
}

// One at a time, because four provisioning runs at once turn four slow
// operations into four slower ones and make each log impossible to follow.
func TestSweepDoesOneSeatAtATime(t *testing.T) {
	running := ""
	var order []string

	busy := func(name string) string {
		if name == running {
			// Free it on the next look, so the sweep can move on.
			running = ""

			return "provisioning"
		}

		return ""
	}

	sweep([]string{"a", "b", "c"}, busy,
		func(name string) error {
			if running != "" {
				t.Errorf("started %s while %s was still going", name, running)
			}

			running = name
			order = append(order, name)

			return nil
		},
		func(string, string) {},
		func() {})

	if len(order) != 3 || order[0] != "a" || order[2] != "c" {
		t.Errorf("worked through %v, want a, b, c in order", order)
	}
}

// A seat that cannot be provisioned must not take the rest of the pass with it.
// Stopping at the first failure leaves the others untouched with nothing saying
// why.
func TestSweepCarriesOnPastAFailure(t *testing.T) {
	var tried, noted []string

	sweep([]string{"bad", "good"},
		func(string) string { return "" },
		func(name string) error {
			tried = append(tried, name)
			if name == "bad" {
				return errors.New("no")
			}

			return nil
		},
		func(name, text string) { noted = append(noted, name) },
		func() {})

	if len(tried) != 2 || tried[1] != "good" {
		t.Errorf("tried %v, want both with good after bad", tried)
	}

	if len(noted) != 1 || noted[0] != "bad" {
		t.Errorf("noted %v, want a note on bad only", noted)
	}
}

// A seat stuck in something else must not hold the pass open for ever, and the
// seat it gave up on has to say so on its own card.
func TestSweepGivesUpOnASeatThatNeverFreesUp(t *testing.T) {
	var noted []string

	sweep([]string{"stuck", "fine"},
		func(name string) string {
			if name == "stuck" {
				return "provisioning"
			}

			return ""
		},
		func(name string) error {
			if name == "stuck" {
				t.Error("provisioned a seat that was still busy")
			}

			return nil
		},
		func(name, text string) { noted = append(noted, name+": "+text) },
		func() {})

	if len(noted) != 1 || !strings.Contains(noted[0], "still busy") {
		t.Errorf("noted %v, want one note saying it was still busy", noted)
	}
}
