package seat

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/superuser404notfound/Polyseat/internal/library"
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

// TestAdoptable is where the rules for taking a Steam library without being
// asked are pinned down.
//
// Every case but the first is a case for doing nothing, which is the point:
// adopting is cheap to get right and expensive to get wrong, since the library
// the daemon picks is the one it clones the seats' games into.
func TestAdoptable(t *testing.T) {
	const pool = "/srv/polyseat/library/pool/steamapps"

	shares := func(from, to string) error { return nil }
	never := func(from, to string) error { return errors.New("different filesystems") }

	cases := []struct {
		name       string
		candidates []string
		unwatched  []string
		shares     func(string, string) error
		want       string
		why        bool
	}{
		{
			name:       "one candidate that can share is adopted",
			candidates: []string{"/home/a/.local/share/Steam/steamapps"},
			shares:     shares,
			want:       "/home/a/.local/share/Steam/steamapps",
		},
		{
			name:   "nothing found means nothing to say",
			shares: shares,
		},
		{
			name:       "two candidates are a question for a person",
			candidates: []string{"/home/a/steamapps", "/home/b/steamapps"},
			shares:     shares,
			why:        true,
		},
		{
			name:       "a candidate on another filesystem is left alone",
			candidates: []string{"/mnt/games/steamapps"},
			shares:     never,
			why:        true,
		},
		{
			name:       "a library somebody removed is not taken back",
			candidates: []string{"/home/a/steamapps"},
			unwatched:  []string{"/home/a/steamapps"},
			shares:     shares,
		},
		{
			name:       "one left after a removal is still adopted",
			candidates: []string{"/home/a/steamapps", "/home/b/steamapps"},
			unwatched:  []string{"/home/a/steamapps"},
			shares:     shares,
			want:       "/home/b/steamapps",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := adoptable(tc.candidates, tc.unwatched, pool, tc.shares)

			if got != tc.want {
				t.Errorf("adopted %q, want %q", got, tc.want)
			}

			// A silent refusal is a bug of its own here. Somebody whose games
			// are not being shared has to be able to find out why from the log
			// rather than from reading this function.
			if tc.why && why == "" {
				t.Error("nothing was adopted and no reason was given")
			}

			if !tc.why && why != "" {
				t.Errorf("a reason was given where none was called for: %s", why)
			}
		})
	}
}

// TestAdoptableDoesNotMutateItsInput guards the caller's slice, which is the
// pool's own candidate list.
func TestAdoptableDoesNotMutateItsInput(t *testing.T) {
	candidates := []string{"/home/a/steamapps", "/home/b/steamapps"}

	adoptable(candidates, []string{"/home/a/steamapps"}, "/pool",
		func(from, to string) error { return nil })

	if candidates[0] != "/home/a/steamapps" || len(candidates) != 2 {
		t.Errorf("the candidate list was rewritten: %v", candidates)
	}
}

// TestAdoptHostLibrary runs the whole automatic path against a real pool on a
// real filesystem: the search is answered for a machine other than this one, but
// the directories, the clones and the state file are genuine.
//
// What it is really asking is whether a decision somebody reverses stays
// reversed. The pass runs every minute, so a removal that the next pass undid
// would be a loop nobody could get out of from the interface.
func TestAdoptHostLibrary(t *testing.T) {
	root := reflinkDirFor(t)

	pool, err := library.Open(filepath.Join(root, "library"))
	if err != nil {
		t.Fatal(err)
	}

	host := filepath.Join(root, "home", "player", ".local", "share", "Steam", "steamapps")
	if err := os.MkdirAll(filepath.Join(host, "common", "Dota 2"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := &Manager{
		pool: pool,
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		libraries: func(exclude string, tracked []string) []string {
			if slices.Contains(tracked, host) {
				return nil
			}

			return []string{host}
		},
	}

	m.adoptHostLibrary()

	if got := pool.Sources(); len(got) != 1 || got[0] != host {
		t.Fatalf("the host's library was not adopted: %v", got)
	}

	// Adopting twice would mean the pass never settles.
	m.adoptHostLibrary()

	if got := pool.Sources(); len(got) != 1 {
		t.Fatalf("a second pass changed the sources: %v", got)
	}

	if err := m.UnwatchLibrary(host); err != nil {
		t.Fatal(err)
	}

	// The search is answered as it would be after a removal: the library is
	// still there and still a candidate, which is exactly the state that would
	// make an unguarded pass take it straight back.
	m.libraries = func(exclude string, tracked []string) []string {
		return []string{host}
	}

	for i := 0; i < 3; i++ {
		m.adoptHostLibrary()
	}

	if got := pool.Sources(); len(got) != 0 {
		t.Fatalf("the daemon took back a library that was removed by hand: %v", got)
	}

	// Asking for it by hand still works, and clears the note that kept the
	// daemon off it. The pool's own entry point rather than the manager's,
	// because ImportLibrary goes on to a full sync, and a sync needs the seat
	// store this test deliberately does not have.
	if _, err := pool.AddSource(host, nil); err != nil {
		t.Fatal(err)
	}

	if got := pool.Sources(); len(got) != 1 || got[0] != host {
		t.Fatalf("importing by hand after a removal did not work: %v", got)
	}

	if got := pool.Unwatched(); len(got) != 0 {
		t.Errorf("the note outlived the import: %v", got)
	}
}

// reflinkDirFor is the seat package's copy of the library package's rule: a
// scratch directory that can actually share blocks, since the adoption is gated
// on measuring exactly that and /tmp is tmpfs on most machines.
func reflinkDirFor(t *testing.T) string {
	t.Helper()

	candidates := []string{os.TempDir(), "/var/tmp"}
	if dir := os.Getenv("POLYSEAT_TEST_DIR"); dir != "" {
		candidates = append([]string{dir}, candidates...)
	}

	for _, base := range candidates {
		dir, err := os.MkdirTemp(base, "polyseat-seat-")
		if err != nil {
			continue
		}

		if err := library.SupportsReflink(dir); err != nil {
			os.RemoveAll(dir)

			continue
		}

		t.Cleanup(func() { os.RemoveAll(dir) })

		return dir
	}

	t.Skipf("none of %v can share blocks, so the adoption cannot be tested "+
		"here. Set POLYSEAT_TEST_DIR to a directory on btrfs or on XFS with "+
		"reflink=1.", candidates)

	return ""
}
