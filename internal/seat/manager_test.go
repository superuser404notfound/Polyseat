package seat

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

// The client's name is read where the seat's Sunshine reports one, and its
// absence is not a parse failure. Both shapes are in the field at once: the name
// arrives in SUNSHINE_CLIENT_NAME from 2026.906.222525 on, and a seat built
// before that pin writes the same marker without it. A seat that answered has to
// keep answering across that line.
func TestTheClientNameIsOptionalInTheMarker(t *testing.T) {
	for _, c := range []struct {
		what   string
		out    string
		client string
		peer   string
	}{
		{
			what:   "a seat on the pin, which reports both",
			out:    "streaming\n{\"app\":\"DREDGE\",\"client\":\"Wohnzimmer\",\"peer\":\"192.168.1.44\"}\n",
			client: "Wohnzimmer",
			peer:   "192.168.1.44",
		},
		{
			what: "a seat built before it, which reports only the address",
			out:  "streaming\n{\"app\":\"DREDGE\",\"peer\":\"192.168.1.44\"}\n",
			peer: "192.168.1.44",
		},
	} {
		session, got := parseStreamCheck(c.out)
		if got != streamBusy {
			t.Errorf("%s was read as %v, want %v", c.what, got, streamBusy)
		}

		if session == nil {
			t.Fatalf("%s lost its description entirely", c.what)
		}

		if session.Client != c.client {
			t.Errorf("%s named the client %q, want %q", c.what, session.Client, c.client)
		}

		if session.Peer != c.peer {
			t.Errorf("%s named the address %q, want %q", c.what, session.Peer, c.peer)
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

// ---------------------------------------------------------------- streaming

// A seat that is not running cannot be streaming, and saying otherwise stops
// the daemon being restarted from its own interface.
//
// The value that used to get stuck is "cannot tell", and every seat passes
// through it: while a seat is being built its container runs and Sunshine does
// not. A build that failed left it set, the poll stopped reading a seat that
// was no longer running, and nothing ever set it back. One such seat greyed out
// "Restart when nobody is playing" on a host with nothing playing at all.
func TestAStoppedSeatIsNotStreaming(t *testing.T) {
	m := &Manager{rt: map[string]*runtime{}}

	rt := m.runtimeOf("vince")
	rt.unclear = true
	rt.streaming = true
	rt.quiet = time.Now()
	rt.session = &Session{}

	if got := m.Streaming(); len(got) != 1 {
		t.Fatalf("Streaming() = %v before the seat stopped, wanted it to count", got)
	}

	m.forgetStream("vince")

	if got := m.Streaming(); len(got) != 0 {
		t.Errorf("Streaming() = %v after the seat stopped, wanted nothing", got)
	}

	if rt.unclear || rt.streaming || rt.session != nil || !rt.quiet.IsZero() {
		t.Errorf("something was left behind: unclear=%v streaming=%v session=%v quiet=%v",
			rt.unclear, rt.streaming, rt.session, rt.quiet)
	}
}

// Both halves of Streaming still count while the seat is up. "Cannot tell" has
// to keep meaning "do not disturb this seat" for a seat that is running, which
// is the reason it exists.
func TestStreamingCountsASeatThatCannotAnswer(t *testing.T) {
	m := &Manager{rt: map[string]*runtime{}}

	m.runtimeOf("one").unclear = true
	m.runtimeOf("two").streaming = true
	m.runtimeOf("three")

	got := m.Streaming()

	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("Streaming() = %v, wanted [one two]", got)
	}
}

// The reading that produced the stuck state, kept apart from the reading that
// genuinely says nothing. A seat whose Sunshine is not running is idle; a seat
// that could not be asked is unknown and leaves what was believed alone.
func TestObserveStreamSeparatesIdleFromUnanswered(t *testing.T) {
	var rt runtime

	rt.observeStream(streamIdle, nil, time.Now())

	if rt.unclear {
		t.Error("a seat with no Sunshine was recorded as one that could not answer")
	}

	rt.observeStream(streamUnknown, nil, time.Now())

	if !rt.unclear {
		t.Error("a seat that could not answer was recorded as one that had")
	}
}

// The bug this is about. Somebody starts a stream and the seat's state does not
// move — it was running before and it is running after — so the old comparison
// found nothing to push and the page, which only reloads when something is
// pushed, went on showing the idle resolution and no Streaming row at all.
func TestTheReadingMovesWhenAStreamStartsThoughTheStateDoesNot(t *testing.T) {
	rt := &runtime{state: StateRunning, output: "1920x1080@60Hz"}

	idle := rt.reading()

	rt.observeStream(streamBusy, &Session{App: "Steam", Width: 2560, Height: 1600}, time.Now())
	rt.output = "2560x1600@120Hz"

	if rt.state != StateRunning {
		t.Fatal("the state moved, which is not the case this is about")
	}

	if rt.reading() == idle {
		t.Error("a stream started and the reading did not move, so nothing is pushed")
	}
}

// And when it ends. The grace period is what decides the moment; this is that
// the moment reaches the page at all.
func TestTheReadingMovesWhenAStreamEnds(t *testing.T) {
	rt := &runtime{state: StateRunning}
	rt.observeStream(streamBusy, &Session{App: "Steam"}, time.Now())

	streaming := rt.reading()

	now := time.Now()
	rt.observeStream(streamIdle, nil, now)

	if !rt.observeStream(streamIdle, nil, now.Add(sessionGrace+time.Second)) {
		t.Fatal("the stream did not end, which is not what this is testing")
	}

	if rt.reading() == streaming {
		t.Error("a stream ended and the reading did not move, so nothing is pushed")
	}
}

// The other half, and the one that decides whether this is usable: a sweep that
// found nothing new must push nothing, or every seat's card is rebuilt every ten
// seconds for the rest of the daemon's life. checked moves on every sweep and is
// the field that would do it.
func TestASweepThatFoundNothingNewPushesNothing(t *testing.T) {
	rt := &runtime{
		state:     StateRunning,
		container: "Running",
		addresses: map[string][]string{"eth1": {"192.168.1.50"}},
		sway:      "active",
		sunshine:  "active",
		encoder:   "hevc_nvenc",
		codecs:    []string{"h264", "hevc"},
		output:    "1920x1080@60Hz",
		devices:   []InputDevice{{Node: "event5", Name: "X-Box pad"}},
		session:   &Session{App: "Steam", Width: 1920, Height: 1080},
		streaming: true,
	}

	before := rt.reading()

	rt.checked = time.Now().Add(time.Minute)

	if rt.reading() != before {
		t.Error("a sweep that read the same thing twice would push a change")
	}
}

// A seat that is not running has no output, the same thing that is said about
// its stream. The card reads output as "what the screen is running at now", so
// a stopped seat went on claiming a live mode.
func TestAStoppedSeatHasNoResolutionOfItsOwn(t *testing.T) {
	m := &Manager{rt: map[string]*runtime{}}

	rt := m.runtimeOf("vince")
	rt.output = "2560x1600@120Hz"

	m.forgetStream("vince")

	if rt.output != "" {
		t.Errorf("a stopped seat still claims to be running at %q", rt.output)
	}
}

// A seat that is already running when the daemon starts is adopted without a
// session start, and the session start is the only other place the uid is
// read. So the record's uid has to be the one used from the first command on,
// and a record without one keeps the default rather than becoming uid 0.
func TestAnAdoptedSeatUsesThePlayerUIDItWasBuiltWith(t *testing.T) {
	m := &Manager{rt: map[string]*runtime{}}

	m.adopt(Seat{Name: "vince", PlayerUID: 1001})
	m.adopt(Seat{Name: "joser"})

	if got := m.uidOf("vince"); got != 1001 {
		t.Errorf("the adopted seat runs as uid %d, want the 1001 its record says", got)
	}

	if got := m.uidOf("joser"); got != 1000 {
		t.Errorf("a record without a uid gave %d, want the default 1000", got)
	}

	if got := m.asPlayer("vince", "true"); !strings.Contains(strings.Join(got, " "), "/run/user/1001") {
		t.Errorf("commands in the adopted seat are run as %q", got)
	}
}

// An operation and a reading of the same seat must not overlap: a sweep that
// looked at busy, found it empty and then went on execing while Stop brought
// the container down is how an exec lands in a shutdown. This walks the
// handshake the two go through, with the real functions, in the order that
// used to go wrong: the sweep is already reading when the operation arrives.
func TestAnOperationStopsAndWaitsOutTheSweepInProgress(t *testing.T) {
	m := &Manager{rt: map[string]*runtime{}}
	rt := m.runtimeOf("vince")

	ctx, end, ok := m.beginSweep(context.Background(), rt, true)
	if !ok {
		t.Fatal("an idle seat could not be swept")
	}

	if err := m.claim(rt, "stopping", func() {}); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if ctx.Err() == nil {
		t.Error("the sweep in progress was not told to stop, so its next exec goes ahead")
	}

	quiet := make(chan struct{})

	go func() {
		rt.quiesce()
		close(quiet)
	}()

	select {
	case <-quiet:
		t.Fatal("the operation went ahead while the sweep was still reading")
	case <-time.After(50 * time.Millisecond):
	}

	end()

	select {
	case <-quiet:
	case <-time.After(5 * time.Second):
		t.Fatal("the operation was still waiting after the sweep ended")
	}

	// And the other order: a sweep that arrives once the operation holds the
	// seat does not read it at all, whether it would have waited or not.
	for _, wait := range []bool{true, false} {
		if _, _, ok := m.beginSweep(context.Background(), rt, wait); ok {
			t.Errorf("a sweep (wait %v) was let into a seat an operation holds", wait)
		}
	}
}

// The timer's sweep leaves a seat to one already being read rather than
// queueing behind it, because it runs on the goroutine that delivers Incus's
// events.
func TestTheTimersSweepDoesNotQueueBehindAnother(t *testing.T) {
	m := &Manager{rt: map[string]*runtime{}}
	rt := m.runtimeOf("vince")

	_, end, ok := m.beginSweep(context.Background(), rt, true)
	if !ok {
		t.Fatal("an idle seat could not be swept")
	}
	defer end()

	done := make(chan bool, 1)

	go func() {
		_, _, ok := m.beginSweep(context.Background(), rt, false)
		done <- ok
	}()

	select {
	case ok := <-done:
		if ok {
			t.Error("two sweeps were reading the same seat at once")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the timer's sweep waited for the one in progress")
	}
}

// A Delete that worked takes the seat's runtime record with it, and the end of
// the operation must not put one back: it used to log "deleting done" and
// reconcile by name, which made a fresh record, and a seat created again under
// that name then showed the old one's log. No Incus client here, so a
// reconcile that still runs fails this test by panicking.
func TestADeletedSeatStaysForgotten(t *testing.T) {
	m := &Manager{rt: map[string]*runtime{}, subs: map[int]chan struct{}{}}
	old := m.runtimeOf("vince")

	ran := make(chan struct{})

	err := m.operate("vince", "deleting", func(context.Context) error {
		m.mu.Lock()
		delete(m.rt, "vince")
		m.mu.Unlock()
		close(ran)

		return nil
	})
	if err != nil {
		t.Fatalf("operate: %v", err)
	}

	<-ran

	deadline := time.Now().Add(5 * time.Second)

	for {
		m.mu.Lock()
		busy := old.busy
		m.mu.Unlock()

		if busy == "" {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("the operation never finished")
		}

		time.Sleep(5 * time.Millisecond)
	}

	// Whatever the end of the operation would still do, give it the moment.
	time.Sleep(50 * time.Millisecond)

	m.mu.Lock()
	_, back := m.rt["vince"]
	m.mu.Unlock()

	if back {
		t.Errorf("the deleted seat has a runtime record again, with log %q", m.Log("vince"))
	}
}

// A build reads the record, works for minutes and then writes down that it
// finished. It used to write back the whole copy it started with, so this is
// the save that happens in those minutes, made through Update, and then the
// end of the build.
func TestABuildKeepsWhatWasSavedWhileItRan(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	m := &Manager{store: store, rt: map[string]*runtime{}, subs: map[int]chan struct{}{}}

	built := Seat{Name: "vince", Label: "Vince", Resolution: "1920x1080@60Hz"}
	if err := store.Put(built); err != nil {
		t.Fatal(err)
	}

	if err := m.Update("vince", func(s *Seat) { s.Label = "Living room" }); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if err := m.recordBuilt("vince", built, 1001); err != nil {
		t.Fatalf("recordBuilt: %v", err)
	}

	got, err := store.Get("vince")
	if err != nil {
		t.Fatal(err)
	}

	if got.Label != "Living room" {
		t.Errorf("the label saved during the build is %q again", got.Label)
	}

	if got.Provisioned != Generation || got.PlayerUID != 1001 {
		t.Errorf("the build was recorded as generation %d, uid %d; want %d, 1001",
			got.Provisioned, got.PlayerUID, Generation)
	}
}

// And a save the build could not have applied leaves the seat needing
// provisioning, rather than being marked current by a build that used the
// old value.
func TestABuildWithOutdatedSettingsIsNotCalledCurrent(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	m := &Manager{store: store, rt: map[string]*runtime{}, subs: map[int]chan struct{}{}}

	built := Seat{Name: "vince", Resolution: "1920x1080@60Hz", Provisioned: Generation - 1}
	if err := store.Put(built); err != nil {
		t.Fatal(err)
	}

	if err := m.Update("vince", func(s *Seat) { s.Resolution = "3840x2160@60Hz" }); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if err := m.recordBuilt("vince", built, 1001); err != nil {
		t.Fatalf("recordBuilt: %v", err)
	}

	got, err := store.Get("vince")
	if err != nil {
		t.Fatal(err)
	}

	if got.Resolution != "3840x2160@60Hz" {
		t.Errorf("the resolution saved during the build is %q again", got.Resolution)
	}

	if got.Provisioned == Generation {
		t.Error("a seat built with the old resolution is marked current")
	}
}

// sessionProbe folds four execs into one script, so the script is what has to
// be right, and it is run here for real under a shell with the seat's tools
// replaced. systemctl answers in the format it gave on the machine this was
// written on, blocks in the order asked for, separated by a blank line.
func TestTheSessionProbeReadsWhatTheFourExecsDid(t *testing.T) {
	run := func(t *testing.T, stubs map[string]string) sessionReading {
		t.Helper()

		bin := t.TempDir()

		for name, body := range stubs {
			if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
				t.Fatal(err)
			}
		}

		cmd := exec.Command("/bin/sh", "-c", sessionProbe(1001))
		cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}

		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("the probe did not exit cleanly: %v\n%s", err, out)
		}

		return parseSessionProbe(string(out))
	}

	units := "cat <<'EOF'\n" +
		"Id=polyseat-sway.service\nActiveState=active\nExecMainStartTimestampMonotonic=12069483\n\n" +
		"Id=polyseat-sunshine.service\nActiveState=activating\nExecMainStartTimestampMonotonic=12070195\n" +
		"EOF\n"

	t.Run("a seat somebody is streaming from", func(t *testing.T) {
		got := run(t, map[string]string{
			"systemctl": units,
			"swaymsg":   `echo '[{"name":"HEADLESS-1","current_mode":{"width":2560,"height":1440,"refresh":60000}}]'` + "\n",
			"ss":        "case \"$*\" in *-Huan*) echo 'UNCONN 0 0 0.0.0.0:47998 0.0.0.0:*' ;; esac\n",
		})

		if got.sway != "active" || got.sunshine != "activating" || got.sunshineStarted != "12070195" {
			t.Errorf("units read as sway %q, sunshine %q started %q", got.sway, got.sunshine, got.sunshineStarted)
		}

		if got.output != "2560x1440@60Hz" {
			t.Errorf("output read as %q", got.output)
		}

		if got.stream != streamBusy {
			t.Errorf("a seat with its stream sockets open read as %v", got.stream)
		}
	})

	// swaymsg failing is what readOutput answered "" for, and the stream check
	// still has to run after it rather than being taken down with it.
	t.Run("an idle seat whose compositor does not answer", func(t *testing.T) {
		got := run(t, map[string]string{
			"systemctl": units,
			"swaymsg":   "echo 'unable to connect' >&2\nexit 1\n",
			"ss":        "exit 0\n",
		})

		if got.output != "" {
			t.Errorf("a failed swaymsg gave output %q", got.output)
		}

		if got.stream != streamIdle {
			t.Errorf("an idle seat read as %v", got.stream)
		}

		if got.sway != "active" {
			t.Errorf("the units were lost along the way: sway %q", got.sway)
		}
	})

	// Nothing at all is a seat that did not answer, not an idle one.
	if got := parseSessionProbe(""); got.stream != streamUnknown || got.sway != "unknown" || got.sunshine != "unknown" {
		t.Errorf("an empty answer read as %+v", got)
	}
}
