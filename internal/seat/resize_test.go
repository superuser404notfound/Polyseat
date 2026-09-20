package seat

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runResize runs the embedded script the way Sunshine runs it, against a
// runtime directory with a sway socket in it and a swaymsg that records rather
// than does. What comes back is the mode sway was asked for, what the script
// said on the way out, and its exit code.
//
// The script itself rather than a Go transcription, because the number that
// matters is the one sway is handed.
func runResize(t *testing.T, env []string, args ...string) (string, string, int) {
	t.Helper()

	home := t.TempDir()

	script := filepath.Join(home, "polyseat-resize")
	if err := os.WriteFile(script, asset("assets/resize.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A socket rather than a file, because the script asks whether it is one
	// and a seat with no session is a case of its own.
	runtime := filepath.Join(home, "run")
	if err := os.MkdirAll(runtime, 0o700); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("unix", filepath.Join(runtime, "sway-ipc.1000.1.sock"))
	if err != nil {
		t.Skipf("SKIPPED: no unix socket here, so the script cannot be run: %v", err)
	}

	defer listener.Close()

	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}

	asked := filepath.Join(home, "asked")
	recorder := "#!/bin/sh\necho \"$*\" >> " + asked + "\n"

	if err := os.WriteFile(filepath.Join(bin, "swaymsg"), []byte(recorder), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/sh", append([]string{script}, args...)...)
	cmd.Env = append([]string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + home,
		"XDG_RUNTIME_DIR=" + runtime,
	}, env...)

	var said strings.Builder
	cmd.Stderr = &said

	code := 0

	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running the script failed: %v", err)
		}

		code = exit.ExitCode()
	}

	recorded, err := os.ReadFile(asked)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	return strings.TrimSpace(string(recorded)), said.String(), code
}

// The width is handed on exactly as the client asked for it, including the
// ones a seat cannot be photographed at - 2250 is one, and the phone on the
// couch asks for it. That cost nothing until 0.22.0 tried to read the screen
// in such a seat and found it could not; nothing does any more, and the note
// in the script says what to change if something ever has to again.
func TestTheClientGetsTheSizeItAskedFor(t *testing.T) {
	sunshine := func(w, h, fps string) []string {
		return []string{
			"SUNSHINE_CLIENT_WIDTH=" + w,
			"SUNSHINE_CLIENT_HEIGHT=" + h,
			"SUNSHINE_CLIENT_FPS=" + fps,
		}
	}

	asked, said, code := runResize(t, sunshine("2250", "1206", "120"))

	if want := "-- output HEADLESS-1 mode 2250x1206@120Hz"; asked != want {
		t.Errorf("sway was asked for %q, want %q", asked, want)
	}

	if !strings.Contains(said, "2250x1206@120Hz") {
		t.Errorf("the journal says %q, want the mode it set in it", said)
	}

	if code != 0 {
		t.Errorf("the script returned %d, and a non-zero prep command stops the stream", code)
	}

	// The undo side, which hands the seat's own configured mode back in.
	if asked, _, _ := runResize(t, nil, "1920x1080@60Hz"); !strings.HasSuffix(asked, "1920x1080@60Hz") {
		t.Errorf("sway was asked for %q, want the explicit mode as it came", asked)
	}
}

// Nothing here may end the stream, and nothing here may guess a size.
func TestResizeSaysWhatItDidAndSurvivesEveryPathOutOfItself(t *testing.T) {
	asked, said, code := runResize(t, []string{"SUNSHINE_CLIENT_WIDTH=2250"})

	if asked != "" {
		t.Errorf("sway was asked for %q on a client that reported no height", asked)
	}

	if !strings.Contains(said, "no client size") || code != 0 {
		t.Errorf("a client with no size reported gave %q and code %d", said, code)
	}

	asked, said, code = runResize(t, []string{
		"SUNSHINE_CLIENT_WIDTH=wide", "SUNSHINE_CLIENT_HEIGHT=1206",
	})

	if asked != "" || code != 0 {
		t.Errorf("a client size that is not a size gave %q and code %d", asked, code)
	}

	if !strings.Contains(said, "is not a size") {
		t.Errorf("the journal says %q, want it to name what it would not use", said)
	}

	// Sunshine has been seen to leave the framerate out, and a headless output
	// still needs one.
	if asked, _, _ := runResize(t, []string{
		"SUNSHINE_CLIENT_WIDTH=1920", "SUNSHINE_CLIENT_HEIGHT=1080",
	}); !strings.HasSuffix(asked, "1920x1080@60Hz") {
		t.Errorf("sway was asked for %q, want a refresh rate of its own", asked)
	}
}
