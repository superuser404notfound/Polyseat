package seat

import (
	"strings"
	"testing"
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

// The resize must have no undo. Putting the size back is sessionEnded's, once a
// stream has stayed gone; an undo would run whenever somebody quits in
// Moonlight and put a game through a mode change in front of nobody.
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
