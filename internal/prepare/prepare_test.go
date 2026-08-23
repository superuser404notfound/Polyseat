package prepare

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// script writes a stand-in for polyseat-prepare and points the lookup at it.
//
// A real prepare.sh cannot run here: it installs packages and initialises
// Incus, and a test that needed root and a spare machine would never be run.
// What is worth testing is everything around it, which is where the failures
// would be quiet: the lookup order, the environment the script is handed, and
// whether its output arrives a line at a time or in one lump at the end.
func script(t *testing.T, body string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, Name)

	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}

	old := Dirs
	Dirs = []string{dir}

	t.Cleanup(func() { Dirs = old })

	return path
}

func TestCommandFindsTheScript(t *testing.T) {
	want := script(t, "true\n")

	got, err := Command()
	if err != nil {
		t.Fatalf("did not find %s: %v", Name, err)
	}

	if got != want {
		t.Errorf("found %q, wanted %q", got, want)
	}
}

// The local copy wins, the same way it does for the input helpers and for the
// binary itself. Somebody testing a change from a checkout on a machine that
// also has the package gets the copy they just built.
func TestCommandPrefersTheFirstDirectory(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()

	for _, dir := range []string{first, second} {
		if err := os.WriteFile(filepath.Join(dir, Name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	old := Dirs
	Dirs = []string{first, second}

	defer func() { Dirs = old }()

	got, err := Command()
	if err != nil {
		t.Fatal(err)
	}

	if got != filepath.Join(first, Name) {
		t.Errorf("found %q, wanted the one in %q", got, first)
	}
}

func TestCommandSaysWhereItLooked(t *testing.T) {
	old := Dirs
	Dirs = []string{t.TempDir()}

	defer func() { Dirs = old }()

	_, err := Command()
	if err == nil {
		t.Fatal("found something in an empty directory")
	}

	if !strings.Contains(err.Error(), Dirs[0]) {
		t.Errorf("the error does not say where it looked: %v", err)
	}
}

// The whole point of running it from here rather than telling somebody to open
// a terminal: the lines arrive while it is still going.
func TestRunReportsEveryLine(t *testing.T) {
	path := script(t, "echo one\necho two >&2\necho three\n")

	var lines []string

	if err := Run(context.Background(), path, "", func(line string) {
		lines = append(lines, line)
	}); err != nil {
		t.Fatalf("the script failed: %v", err)
	}

	// Standard error is in there with the rest and in order. Two pipes would
	// have put every warning at the end, which is the one place a warning about
	// a step is no use.
	if got := strings.Join(lines, ","); got != "one,two,three" {
		t.Errorf("got %q", got)
	}
}

func TestRunPassesTheAccountAndTakesTheColourAway(t *testing.T) {
	path := script(t, `printf '%s %s %s\n' "$POLYSEAT_INPUT_USER" "$NO_COLOR" "$POLYSEAT_FROM_DAEMON"`)

	var lines []string

	if err := Run(context.Background(), path, "vincent", func(line string) {
		lines = append(lines, line)
	}); err != nil {
		t.Fatal(err)
	}

	if len(lines) != 1 || lines[0] != "vincent 1 1" {
		t.Errorf("the script was handed %q", lines)
	}
}

// Without an account nothing is set, rather than an empty variable being set:
// prepare.sh falls back to SUDO_USER, and a machine with neither says so and
// adds nobody to anything.
func TestRunWithoutAnAccountSetsNothing(t *testing.T) {
	path := script(t, `printf '%s\n' "${POLYSEAT_INPUT_USER-unset}"`)

	var lines []string

	if err := Run(context.Background(), path, "", func(line string) {
		lines = append(lines, line)
	}); err != nil {
		t.Fatal(err)
	}

	if len(lines) != 1 || lines[0] != "unset" {
		t.Errorf("got %q", lines)
	}
}

func TestRunSaysWhichScriptFailed(t *testing.T) {
	path := script(t, "echo nearly\nexit 3\n")

	err := Run(context.Background(), path, "", nil)
	if err == nil {
		t.Fatal("a script that exited 3 was reported as a success")
	}

	if !strings.Contains(err.Error(), Name) {
		t.Errorf("the error does not name the script: %v", err)
	}
}

// pacman redraws its progress bar by returning the carriage, so one line of its
// output carries every frame of the bar. A terminal shows the last one; without
// this the page shows all of them end to end.
func TestLastFrameKeepsWhatATerminalWouldShow(t *testing.T) {
	cases := map[string]string{
		"downloading  10%\rdownloading  90%\rdownloading 100%": "downloading 100%",
		"an ordinary line":  "an ordinary line",
		"trailing space   ": "trailing space",
		"":                  "",
	}

	for in, want := range cases {
		if got := lastFrame(in); got != want {
			t.Errorf("lastFrame(%q) = %q, wanted %q", in, got, want)
		}
	}
}

// The list exists for one question, whose account goes in the input group, and
// answering it with root or with a service account would be worse than not
// answering it at all.
func TestAccountsAreThePeople(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "passwd")

	content := strings.Join([]string{
		"root:x:0:0::/root:/usr/bin/bash",
		"bin:x:1:1::/:/usr/bin/nologin",
		"vincent:x:1000:1000::/home/vincent:/usr/bin/zsh",
		"guest:x:1001:1001::/home/guest:/bin/bash",
		"locked:x:1002:1002::/home/locked:/usr/bin/false",
		"nobody:x:65534:65534:Nobody:/:/usr/bin/nologin",
		"malformed",
		"",
	}, "\n")

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got := strings.Join(accountsFrom(path), ",")
	if got != "vincent,guest" {
		t.Errorf("got %q, wanted vincent,guest", got)
	}
}

// One at a time, because the work is pacman and a second one would sit on the
// database lock and report a failure that says nothing about what happened.
func TestOnlyOneRunAtATime(t *testing.T) {
	script(t, "sleep 2\n")

	var runner Runner

	if err := runner.Start("", nil); err != nil {
		t.Fatalf("the first run was refused: %v", err)
	}

	if err := runner.Start("", nil); err == nil {
		t.Error("a second run was allowed while the first was going")
	}
}

func TestStartRefusesAnAccountThisMachineHasNever(t *testing.T) {
	script(t, "true\n")

	var runner Runner

	if err := runner.Start("nobody-by-that-name", nil); err == nil {
		t.Error("an account that does not exist was accepted")
	}
}
