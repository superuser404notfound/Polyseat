package uninstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func place(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, Name)

	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	old := Dirs
	Dirs = []string{dir}

	t.Cleanup(func() { Dirs = old })

	return path
}

func TestCommandFindsTheScript(t *testing.T) {
	want := place(t)

	got, err := Command()
	if err != nil {
		t.Fatalf("did not find %s: %v", Name, err)
	}

	if got != want {
		t.Errorf("found %q, wanted %q", got, want)
	}
}

func TestCommandSaysWhereItLooked(t *testing.T) {
	old := Dirs
	Dirs = []string{t.TempDir()}

	defer func() { Dirs = old }()

	if _, err := Command(); err == nil || !strings.Contains(err.Error(), Dirs[0]) {
		t.Errorf("wanted an error naming %q, got %v", Dirs[0], err)
	}
}

// These five words decide whether somebody's seats still exist afterwards, so
// they are read rather than trusted.
func TestArgsSayExactlyWhatWasAskedFor(t *testing.T) {
	cases := []struct {
		why   string
		opts  Options
		wants []string
		nots  []string
	}{
		{
			why:   "the daemon only",
			opts:  Options{},
			wants: []string{"--yes"},
			nots:  []string{"--seats", "--library"},
		},
		{
			why:   "the seats as well",
			opts:  Options{Seats: true},
			wants: []string{"--yes", "--seats"},
			nots:  []string{"--library"},
		},
		{
			why:   "and the library",
			opts:  Options{Seats: true, Library: true},
			wants: []string{"--yes", "--seats", "--library"},
		},
	}

	for _, c := range cases {
		got := args("/usr/bin/"+Name, c.opts)
		line := strings.Join(got, " ")

		for _, want := range c.wants {
			if !strings.Contains(line, want) {
				t.Errorf("%s: %q is missing from %q", c.why, want, line)
			}
		}

		for _, not := range c.nots {
			if strings.Contains(line, not) {
				t.Errorf("%s: %q should not be in %q", c.why, not, line)
			}
		}

		script := indexOf(got, "/usr/bin/"+Name)
		if script < 0 {
			t.Errorf("%s: the script itself is missing from %q", c.why, line)

			continue
		}

		// Everything before the script path belongs to systemd-run and
		// everything after it to the script, and the boundary is not a detail:
		// --seats read by systemd-run is an unknown option and nothing is
		// removed at all, while --unit read by the script is an unknown option
		// and it exits before it starts.
		for _, flag := range []string{"--yes", "--seats", "--library"} {
			if at := indexOf(got, flag); at >= 0 && at < script {
				t.Errorf("%s: %s landed on systemd-run's side: %q", c.why, flag, line)
			}
		}

		for _, flag := range []string{"--collect", "--on-active=2s"} {
			if at := indexOf(got, flag); at < 0 || at > script {
				t.Errorf("%s: %s landed on the script's side: %q", c.why, flag, line)
			}
		}
	}
}

func indexOf(list []string, want string) int {
	for i, item := range list {
		if item == want {
			return i
		}
	}

	return -1
}

// The library is not a thing on its own: it is the pool the seats share, and
// taking it out from under seats that are staying would leave a working
// installation with no games in it.
func TestStartRefusesTheLibraryWithoutTheSeats(t *testing.T) {
	place(t)

	if err := Start(Options{Library: true}); err == nil {
		t.Error("the library was accepted without the seats")
	}
}

func TestStartSaysWhenTheScriptIsNotThere(t *testing.T) {
	old := Dirs
	Dirs = []string{t.TempDir()}

	defer func() { Dirs = old }()

	if err := Start(Options{}); err == nil {
		t.Error("a removal was scheduled with nothing to run")
	}
}
