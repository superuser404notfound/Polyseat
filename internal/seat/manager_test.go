package seat

import (
	"errors"
	"strings"
	"testing"
	"time"
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

// The sequence that ended somebody's stream, replayed against the decision that
// used to make it. An iPhone dropped for twelve seconds and came back; Sunshine
// kept the application running and so never rewrote the marker file; the daemon
// concluded the seat was idle and rebuilt the app list under a live session.
func TestAStreamSurvivesTheClientDroppingAndComingBack(t *testing.T) {
	rt := &runtime{}
	start := time.Date(2026, 7, 30, 17, 47, 11, 0, time.UTC)

	playing := &Session{App: "Desktop", Width: 1920, Height: 1080}

	if ended := rt.observeStream(streamBusy, playing, start); ended {
		t.Fatal("the start of a stream was reported as its end")
	}

	// The dropout. Twelve seconds is what was measured, and the reconnect
	// carries no marker with it because no prep command ran.
	for _, at := range []time.Duration{10 * time.Second, 20 * time.Second} {
		if ended := rt.observeStream(streamIdle, nil, start.Add(at)); ended {
			t.Fatalf("a dropout of %s ended the stream", at)
		}
	}

	if ended := rt.observeStream(streamBusy, nil, start.Add(25*time.Second)); ended {
		t.Error("the reconnect was reported as an end")
	}

	if !rt.streaming {
		t.Error("the seat is not streaming after the client came back, which is what let the app list be rebuilt under it")
	}

	if rt.session == nil || rt.session.App != "Desktop" {
		t.Errorf("the description was lost across the reconnect: %+v, and the card would say nobody is playing", rt.session)
	}
}

// A seat that did not answer is not a seat that is idle. This is the reading
// that used to be indistinguishable from "nobody is streaming", and acting on it
// is what rebuilds the app list under a live session.
func TestAReadingThatSaysNothingLeavesTheStreamAlone(t *testing.T) {
	rt := &runtime{}
	start := time.Date(2026, 7, 30, 17, 47, 11, 0, time.UTC)

	rt.observeStream(streamBusy, &Session{App: "DREDGE"}, start)

	// Long enough that treating these as absences would have ended it twice
	// over.
	for _, at := range []time.Duration{time.Second, 2 * sessionGrace, 4 * sessionGrace} {
		if ended := rt.observeStream(streamUnknown, nil, start.Add(at)); ended {
			t.Fatalf("a reading that said nothing ended the stream after %s", at)
		}
	}

	if !rt.streaming {
		t.Error("the seat stopped counting as streaming because it did not answer")
	}

	if !rt.unclear {
		t.Error("nothing recorded that the answer was missing, so the app list would be rebuilt anyway")
	}

	// And the grace period starts from the first real absence, not from the
	// readings that said nothing.
	if ended := rt.observeStream(streamIdle, nil, start.Add(5*sessionGrace)); ended {
		t.Error("the first real absence ended it immediately, so a client that dropped for a moment loses its session")
	}

	if rt.unclear {
		t.Error("a seat that answered is still marked as not having answered")
	}

	if ended := rt.observeStream(streamIdle, nil, start.Add(6*sessionGrace+time.Second)); !ended {
		t.Error("a stream really gone for the whole grace period never ended")
	}
}

// What the check prints, and what each of those means. The dangerous direction
// is the one that reads as an idle seat, so every answer that is not the word
// for it has to come back as unknown.
func TestOnlyTheWordIdleMeansNobodyIsStreaming(t *testing.T) {
	for _, c := range []struct {
		what  string
		out   string
		want  streamState
		named string
	}{
		{what: "an idle seat", out: "idle\n", want: streamIdle},
		{
			what:  "a stream with a marker",
			out:   "streaming\n{\"app\":\"DREDGE\",\"width\":2560,\"height\":1440}\n",
			want:  streamBusy,
			named: "DREDGE",
		},
		{what: "a stream whose marker is gone", out: "streaming\n", want: streamBusy},
		{what: "a stream whose marker is nonsense", out: "streaming\nnot json", want: streamBusy},
		{what: "nothing at all", out: "", want: streamUnknown},
		{what: "an error from the shell", out: "sh: ss: not found\n", want: streamUnknown},
		{what: "a truncated answer", out: "idl", want: streamUnknown},
	} {
		session, got := parseStreamCheck(c.out)
		if got != c.want {
			t.Errorf("%s was read as %v, want %v", c.what, got, c.want)
		}

		switch {
		case c.named == "" && session != nil:
			t.Errorf("%s produced a description out of nowhere: %+v", c.what, session)
		case c.named != "" && (session == nil || session.App != c.named):
			t.Errorf("%s lost its description: %+v", c.what, session)
		}
	}
}

// And a stream that really ends still has to end, or the resolution is never put
// back and the card claims somebody is playing long after they closed Moonlight.
func TestAStreamThatStaysGoneEndsAfterTheGrace(t *testing.T) {
	rt := &runtime{}
	start := time.Date(2026, 7, 30, 17, 47, 11, 0, time.UTC)

	rt.observeStream(streamBusy, &Session{App: "DREDGE"}, start)

	if ended := rt.observeStream(streamIdle, nil, start.Add(time.Second)); ended {
		t.Fatal("the first missing reading ended it, which is the bug this is about")
	}

	// The grace runs from the first reading that missed it, not from the start
	// of the stream.
	if ended := rt.observeStream(streamIdle, nil, start.Add(time.Second+sessionGrace)); !ended {
		t.Fatal("a stream gone for the whole grace period never ended")
	}

	if rt.streaming || rt.session != nil {
		t.Errorf("the seat still looks busy after the end: streaming=%v session=%+v", rt.streaming, rt.session)
	}

	// Once only. A second reading of the same absence would run the undo
	// commands and the pending app list rebuild again.
	if ended := rt.observeStream(streamIdle, nil, start.Add(2*sessionGrace)); ended {
		t.Error("the same end was reported twice")
	}
}

// Both lines come from a real seat. The order systemd prints properties in is
// not something to depend on, so the reversed case is here as well: read
// positionally, a unit's state would become a timestamp, every seat would look
// unknown, and the card would go dark on a machine that is working perfectly.
func TestUnitShowIsReadByKeyRatherThanByPosition(t *testing.T) {
	for name, tc := range map[string]struct {
		out     string
		state   string
		started string
	}{
		"a running unit": {
			"ActiveState=active\nExecMainStartTimestampMonotonic=307122230849\n",
			"active", "307122230849",
		},
		"the same the other way round": {
			"ExecMainStartTimestampMonotonic=307122230849\nActiveState=active\n",
			"active", "307122230849",
		},
		// What a seat answers for a unit it does not have. Zero is not a time,
		// and taken as one it would be a moment every seat shares.
		"a unit that is not there": {
			"ActiveState=inactive\nExecMainStartTimestampMonotonic=0\n",
			"inactive", "",
		},
		"nothing at all": {"", "unknown", ""},
		"noise":          {"Failed to connect to bus\n", "unknown", ""},
	} {
		state, started := parseUnitShow(tc.out)

		if state != tc.state || started != tc.started {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", name, state, started, tc.state, tc.started)
		}
	}
}

// Skipping the journal read is only safe while the answer cannot have changed.
// Getting this wrong is quiet: the interface would go on reporting the encoder
// of a Sunshine that has since restarted, and that line is the one place a seat
// says whether it fell back to software.
func TestEncodersAreReadAgainWhenSunshineRestarted(t *testing.T) {
	rt := &runtime{}

	if rt.encodersOnRecord("100") {
		t.Error("a seat nothing has been read from yet claimed to know its encoder")
	}

	rt.encoder, rt.codecs, rt.encodersFrom = "nvenc", []string{"H.264"}, "100"

	if !rt.encodersOnRecord("100") {
		t.Error("the same Sunshine is still running and the journal would be read again")
	}

	if rt.encodersOnRecord("200") {
		t.Error("Sunshine restarted and the encoder on record was kept")
	}

	// systemd reporting no start time is not a reason to go back to reading a
	// growing journal every ten seconds.
	if !rt.encodersOnRecord("") {
		t.Error("a missing start time sent it back to reading the journal every tick")
	}
}
