package seat

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The lines as they reach the daemon: journalctl --output cat, so Sunshine's own
// timestamp and nothing in front of it. Copied from louis on 2026-10-03.
func TestSunshineLineKnowsAClientArrivingFromOneLeaving(t *testing.T) {
	cases := map[string]string{
		"[2026-10-03 11:10:33.706]: Info: CLIENT DISCONNECTED":                    sunshineDisconnected,
		"[2026-10-03 11:10:47.641]: Info: CLIENT CONNECTED":                       sunshineConnected,
		"[2026-10-03 11:10:47.641]: Info: CLIENT CONNECTED\r":                     sunshineConnected,
		"[2026-10-03 11:10:47.642]: Info: [wayland] Resolution: 3840x1080":        "",
		"[2026-10-03 11:16:05.918]: Info: Executing Undo Cmd: [polyseat-session]": "",
		"[2026-10-03 11:00:00.000]: Info: Paired client CLIENT DISCONNECTED ok":   "",
		"": "",
	}

	for line, want := range cases {
		if got := sunshineLine(line); got != want {
			t.Errorf("sunshineLine(%q) = %q, want %q", line, got, want)
		}
	}
}

// A line can arrive in pieces, and several can arrive in one write.
func TestLineWriterHandsOnWholeLines(t *testing.T) {
	var got []string

	w := &lineWriter{line: func(s string) { got = append(got, s) }}

	for _, part := range []string{"one\ntw", "o\n", "", "three\nfour\nfi"} {
		if n, err := w.Write([]byte(part)); n != len(part) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", part, n, err)
		}
	}

	if want := "one|two|three|four"; strings.Join(got, "|") != want {
		t.Errorf("lines %q, want %q", strings.Join(got, "|"), want)
	}
}

// The case that was reported: the client leaves and the stream is gone a moment
// later, so the application is closed and the next connection is a launch.
func TestLetGoClosesOnceTheStreamIsGone(t *testing.T) {
	var asked, closed atomic.Int32

	idle := func(context.Context) bool { return asked.Add(1) >= 3 }

	ok := letGo(context.Background(), func() bool { return false }, idle,
		func(context.Context) { closed.Add(1) })

	if !ok || closed.Load() != 1 {
		t.Errorf("letGo = %v with %d closes, want true and one", ok, closed.Load())
	}
}

// A client that is back before the stream was seen to go must keep it.
// Closing the application under them ends what they just reconnected to.
func TestLetGoLeavesAClientThatCameBack(t *testing.T) {
	var asked, closed atomic.Int32

	back := func() bool { return asked.Load() >= 2 }
	idle := func(context.Context) bool { asked.Add(1); return false }

	ok := letGo(context.Background(), back, idle, func(context.Context) { closed.Add(1) })

	if ok || closed.Load() != 0 {
		t.Errorf("letGo = %v with %d closes, want false and none", ok, closed.Load())
	}
}

// And the same when the client comes back while the seat is being asked: the
// question takes an exec, and a reconnect can land inside it.
func TestLetGoAsksOnceMoreBeforeClosing(t *testing.T) {
	var returned, closed atomic.Bool

	idle := func(context.Context) bool { returned.Store(true); return true }

	ok := letGo(context.Background(), returned.Load, idle,
		func(context.Context) { closed.Store(true) })

	if ok || closed.Load() {
		t.Errorf("letGo = %v, closed %v, want neither", ok, closed.Load())
	}
}

// A seat that never stops looking busy is somebody playing, and is left to
// sessionEnded. Bounded, because this runs once per departure.
func TestLetGoGivesUpOnASeatThatStaysBusy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()

	var closed atomic.Bool

	start := time.Now()
	ok := letGo(ctx, func() bool { return false }, func(context.Context) bool { return false },
		func(context.Context) { closed.Store(true) })

	if ok || closed.Load() {
		t.Errorf("letGo = %v, closed %v, want neither", ok, closed.Load())
	}

	if waited := time.Since(start); waited > 2*time.Second {
		t.Errorf("letGo waited %v past its deadline", waited)
	}
}

// The resize must have no undo: Sunshine would run it on every brief drop, now
// that the daemon closes the application the moment a client leaves.
func TestSunshineConfigHasNoUndoForTheResize(t *testing.T) {
	conf := string(asset("assets/sunshine.conf"))

	for _, line := range strings.Split(conf, "\n") {
		if !strings.HasPrefix(line, "global_prep_cmd") {
			continue
		}

		if !strings.Contains(line, `{"do":"/usr/local/bin/polyseat-resize"}`) {
			t.Errorf("the resize is not a do on its own: %s", line)
		}

		return
	}

	t.Fatal("no global_prep_cmd in sunshine.conf")
}

// Nor the cap. MangoHud rereads its file in a running game, so an undo that
// took the cap off left a game somebody had not quit rendering flat out behind
// them: 7000 frames a second from an OpenGL program in joser on 2026-10-07.
func TestSunshineConfigHasNoUndoForTheCap(t *testing.T) {
	conf := string(asset("assets/sunshine.conf"))

	for _, line := range strings.Split(conf, "\n") {
		if !strings.HasPrefix(line, "global_prep_cmd") {
			continue
		}

		if !strings.Contains(line, `{"do":"/usr/local/bin/polyseat-fps"}`) {
			t.Errorf("the cap is not a do on its own: %s", line)
		}

		return
	}

	t.Fatal("no global_prep_cmd in sunshine.conf")
}
