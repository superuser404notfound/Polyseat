package seat

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// hostIdle decides whether the daemon may replace files in the host's Steam
// library, so a probe that answers "idle" while something is reading them
// corrupts an install. The three ways a file can be in use are asked for
// separately, against this process and the real /proc, because a fake one would
// only prove that the parser reads what the test wrote.
//
// Each of these was run once against a deliberately broken probe. A check that
// has never failed is not known to check anything.
func TestHostIdle(t *testing.T) {
	dir := t.TempDir()

	// Resolved, because /proc reports real paths and a temporary directory on a
	// Mac or under a symlinked /tmp is not the path we were handed.
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	apps := filepath.Join(dir, "steamapps")
	if err := os.MkdirAll(filepath.Join(apps, "common"), 0o755); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(apps, "common", "game.bin")
	if err := os.WriteFile(path, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}

	uid := os.Geteuid()

	if !hostIdle(apps, uid) {
		t.Fatal("a library nothing has touched was reported busy")
	}

	t.Run("an open file", func(t *testing.T) {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}

		defer f.Close()

		if hostIdle(apps, uid) {
			t.Error("a library with an open file in it was reported idle")
		}
	})

	t.Run("a mapped file", func(t *testing.T) {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}

		data, err := syscall.Mmap(int(f.Fd()), 0, 4096, syscall.PROT_READ, syscall.MAP_PRIVATE)

		// Closed straight away, so the mapping is the only thing left holding
		// the file. A running game is found this way and not through its
		// descriptors.
		f.Close()

		if err != nil {
			t.Fatal(err)
		}

		defer syscall.Munmap(data)

		if hostIdle(apps, uid) {
			t.Error("a library with a mapped file in it was reported idle")
		}
	})

	t.Run("a working directory", func(t *testing.T) {
		back, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}

		if err := os.Chdir(filepath.Join(apps, "common")); err != nil {
			t.Fatal(err)
		}

		defer os.Chdir(back)

		if hostIdle(apps, uid) {
			t.Error("a library that a process is sitting in was reported idle")
		}
	})

	if !hostIdle(apps, uid) {
		t.Error("the library was still reported busy after everything let go of it")
	}

	// A neighbour whose path begins with the same characters. Without the
	// separator this is the case that would call the library busy forever
	// because of a directory that has nothing to do with it.
	t.Run("a directory with a similar name", func(t *testing.T) {
		other := apps + "-backup"
		if err := os.MkdirAll(other, 0o755); err != nil {
			t.Fatal(err)
		}

		name := filepath.Join(other, "game.bin")
		if err := os.WriteFile(name, nil, 0o644); err != nil {
			t.Fatal(err)
		}

		f, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}

		defer f.Close()

		if !hostIdle(apps, uid) {
			t.Error("a file next to the library, not in it, was taken for a file in it")
		}
	})

	// Nobody's processes. The walk skips every process that does not belong to
	// the owner of the library, and the point of that is speed on a host that
	// also runs every seat's processes under a mapped uid.
	t.Run("another owner", func(t *testing.T) {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}

		defer f.Close()

		if !hostIdle(apps, uid+4242) {
			t.Error("a process belonging to somebody else was taken into account")
		}
	})
}
