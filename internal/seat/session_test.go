package seat

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runSession runs polyseat-session the way Sunshine does, with the given
// environment, and returns the record it wrote exactly as the daemon would read
// it.
func runSession(t *testing.T, env ...string) (*Session, string) {
	t.Helper()

	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("SKIPPED: no sh")
	}

	home := t.TempDir()

	script := filepath.Join(home, "polyseat-session")
	if err := os.WriteFile(script, asset("assets/session.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	// No connection to find: ss answers with nothing, the way it does in a
	// seat between a client leaving and the record being cleared.
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(bin, "ss"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(sh, script)
	cmd.Env = append(os.Environ(), append([]string{
		"HOME=" + home,
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"XDG_RUNTIME_DIR=" + home,
	}, env...)...)

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the script failed: %v\n%s", err, out)
	}

	raw, err := os.ReadFile(filepath.Join(home, ".local/share/polyseat/session.json"))
	if err != nil {
		t.Fatal(err)
	}

	var session Session
	if err := json.Unmarshal(raw, &session); err != nil {
		return nil, string(raw)
	}

	return &session, string(raw)
}

// The names in the record are chosen by other people: the application's by
// whoever set up the list, the client's by whoever paired it. A line break in
// either used to reach the file raw, which JSON does not allow, and the whole
// record became unreadable.
func TestSessionRecordSurvivesAnyName(t *testing.T) {
	app := "Two\nLines \"quoted\" and a \\ and a\ttab"
	client := "Living room\r\nTV"

	session, raw := runSession(t,
		"SUNSHINE_APP_NAME="+app,
		"SUNSHINE_CLIENT_NAME="+client,
		"SUNSHINE_CLIENT_WIDTH=1920",
		"SUNSHINE_CLIENT_HEIGHT=1080",
		"SUNSHINE_CLIENT_FPS=60")

	if session == nil {
		t.Fatalf("the record is not JSON:\n%s", raw)
	}

	if !strings.Contains(session.App, `"quoted"`) || !strings.Contains(session.App, `\`) {
		t.Errorf("the application's name came back as %q", session.App)
	}

	if session.Width != 1920 || session.Height != 1080 || session.FPS != 60 {
		t.Errorf("the size came back as %dx%d@%d", session.Width, session.Height, session.FPS)
	}
}

// The size is written as numbers, unquoted, so anything that is not digits
// would break the record as surely as a line break in a name.
func TestSessionRecordLeavesOutASizeThatIsNotANumber(t *testing.T) {
	session, raw := runSession(t,
		"SUNSHINE_APP_NAME=Desktop",
		"SUNSHINE_CLIENT_WIDTH=1920,",
		"SUNSHINE_CLIENT_HEIGHT=1080",
		"SUNSHINE_CLIENT_FPS=sixty")

	if session == nil {
		t.Fatalf("the record is not JSON:\n%s", raw)
	}

	if session.Width != 0 || session.FPS != 0 {
		t.Errorf("kept %d wide at %d frames, want both left out", session.Width, session.FPS)
	}

	if session.App != "Desktop" {
		t.Errorf("the application came back as %q", session.App)
	}
}
