package seat

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// probeAgainst runs the real idleProbe against the real /proc of the machine
// running the test, with the two mount points pointed at directories the test
// owns.
//
// The shell fragment itself rather than a Go transcription of it. What has to
// hold is what that fragment answers, and a second implementation would only
// prove that the two agree with each other.
func probeAgainst(t *testing.T, games, steam string) int {
	t.Helper()

	script := strings.ReplaceAll(idleProbe, LibraryMount, games)
	script = strings.ReplaceAll(script, steamApps, steam)

	if strings.Contains(script, LibraryMount) || strings.Contains(script, steamApps) {
		t.Fatal("the probe still names the real mount points, so this test would be asking about the machine it runs on")
	}

	cmd := exec.Command("/bin/sh", "-c", script)

	if err := cmd.Run(); err != nil {
		// A probe that could not be run at all must not be mistaken for a probe
		// that answered, which is the reading that permits an update.
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running the probe failed: %v", err)
		}

		return exit.ExitCode()
	}

	return 0
}

// held starts a process that uses the library the way the probe is meant to
// notice, and waits until the probe does.
//
// The wait is for the process to become visible in /proc, not for the answer to
// come out the way the test wants: a process that never appears fails the test
// rather than passing it late.
func held(t *testing.T, games, steam string, cmd *exec.Cmd) {
	t.Helper()

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the holder failed: %v", err)
	}

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		if probeAgainst(t, games, steam) == 1 {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("the probe never noticed the process using the library, so an update would have replaced the files underneath it")
}

// The probe decides whether a seat's copy of a game may be replaced while the
// seat is running. Getting it wrong in this direction corrupts an install, so
// each of the three ways a file can be in use is checked separately: they are
// found by three different mechanisms and any one of them can rot on its own.
func TestIdleProbeNoticesEveryWayTheLibraryIsUsed(t *testing.T) {
	games := t.TempDir()
	steam := t.TempDir()

	// Without this the rest proves nothing: a probe that answered "in use" for
	// everything would pass every case below.
	if code := probeAgainst(t, games, steam); code != 0 {
		t.Fatalf("nothing is using these directories and the probe said %d", code)
	}

	t.Run("an open file", func(t *testing.T) {
		file := filepath.Join(games, "held.bin")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}

		held(t, games, steam, exec.Command("/bin/sh", "-c", `exec sleep 30 < "$1"`, "sh", file))
	})

	t.Run("a working directory", func(t *testing.T) {
		// Under the mount rather than at it, which is where a game actually
		// sits: steamapps/common/<game>. Both this probe and the one it
		// replaced look for paths below the mount point and neither matches the
		// mount point itself, and nothing holds a game's files open by standing
		// in the directory above them.
		dir := filepath.Join(steam, "common", "Some Game")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command("sleep", "30")
		cmd.Dir = dir

		held(t, games, steam, cmd)
	})

	t.Run("a mapped executable", func(t *testing.T) {
		// A running game is found by its maps rather than by an open
		// descriptor, because an executable is mapped and then closed.
		src, err := exec.LookPath("sleep")
		if err != nil {
			t.Skip("no sleep to copy")
		}

		body, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}

		binary := filepath.Join(games, "game")
		if err := os.WriteFile(binary, body, 0o755); err != nil {
			t.Fatal(err)
		}

		held(t, games, steam, exec.Command(binary, "30"))
	})

	// And back to idle once the holders are gone, or the first game anybody
	// ran would freeze the library for as long as the seat stayed up.
	t.Cleanup(func() {
		if code := probeAgainst(t, games, steam); code != 0 {
			t.Errorf("everything has exited and the probe still says %d", code)
		}
	})
}
