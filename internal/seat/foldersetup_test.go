package seat

import (
	"os"
	"path/filepath"
	"testing"
)

// The daemon decides whether to run a folder's setup by looking at the file,
// and a wrong answer either skips a game that would have worked or reports
// "permission denied" out of a log nobody was reading.
func TestRunnable(t *testing.T) {
	dir := t.TempDir()

	if runnable(filepath.Join(dir, folderSetup)) {
		t.Error("a folder with no setup script at all was taken for one with")
	}

	script := filepath.Join(dir, folderSetup)
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The bit is the point. A folder that came off a filesystem which does not
	// carry it, or out of a zip, has a script that cannot be run.
	if runnable(script) {
		t.Error("a script without the executable bit was reported runnable")
	}

	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}

	if !runnable(script) {
		t.Error("an executable script was not reported runnable")
	}

	// A directory of that name carries the executable bit as a matter of
	// course, so IsRegular is what keeps it from being handed to exec.
	asDir := filepath.Join(dir, "sub", folderSetup)
	if err := os.MkdirAll(asDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if runnable(asDir) {
		t.Error("a directory was reported runnable")
	}
}
