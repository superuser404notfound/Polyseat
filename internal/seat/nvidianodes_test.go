package seat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// present is the Exists half of a guard, driven by a set the test controls.
func present(nodes ...string) func(string) bool {
	have := map[string]bool{}
	for _, node := range nodes {
		have[node] = true
	}

	return func(path string) bool { return have[path] }
}

func quiet(string, ...any) {}

// TestGuardLeavesAMDAlone checks the short circuit rather than the message,
// because on AMD the nodes this looks for do not exist and never will: asking
// for them there and then running nvidia-modprobe would turn every AMD start
// into a failure.
func TestGuardLeavesAMDAlone(t *testing.T) {
	called := false

	guard := nodeGuard{
		Vendor: VendorAMD,
		Nodes:  nvidiaNodesFor(0),
		Exists: present(),
		Create: func(context.Context) error { called = true; return nil },
		Log:    quiet,
	}

	if err := guard.run(context.Background()); err != nil {
		t.Fatalf("an AMD host should not care about /dev/nvidia0: %v", err)
	}

	if called {
		t.Fatal("nvidia-modprobe was run on an AMD host")
	}
}

// TestGuardDoesNothingWhenTheCardIsThere is the ordinary case, which is every
// start after the machine has been up for a while.
func TestGuardDoesNothingWhenTheCardIsThere(t *testing.T) {
	called := false

	guard := nodeGuard{
		Vendor: VendorNVIDIA,
		Nodes:  nvidiaNodesFor(0),
		Exists: present(nvidiaNodesFor(0)...),
		Create: func(context.Context) error { called = true; return nil },
		Log:    quiet,
	}

	if err := guard.run(context.Background()); err != nil {
		t.Fatalf("nothing was missing: %v", err)
	}

	if called {
		t.Fatal("nvidia-modprobe was run although both nodes were there")
	}
}

// TestGuardCreatesTheMissingCard is the boot case that this exists for:
// /dev/nvidiactl is there, /dev/nvidia0 is not, which is exactly the state both
// seats were started in on this machine.
func TestGuardCreatesTheMissingCard(t *testing.T) {
	have := map[string]bool{"/dev/nvidiactl": true}
	runs := 0

	guard := nodeGuard{
		Vendor: VendorNVIDIA,
		Nodes:  nvidiaNodesFor(0),
		Exists: func(path string) bool { return have[path] },
		Create: func(context.Context) error {
			runs++
			have["/dev/nvidia0"] = true

			return nil
		},
		Log: quiet,
	}

	if err := guard.run(context.Background()); err != nil {
		t.Fatalf("the node was created, so this should have passed: %v", err)
	}

	if runs != 1 {
		t.Fatalf("nvidia-modprobe ran %d times, want 1", runs)
	}
}

// TestGuardRefusesWhenTheCardStaysMissing is the point of asking a second time.
// A Create that reports success without having done anything would otherwise
// hand the container a host with no card, which is the silent failure this
// whole file is about.
func TestGuardRefusesWhenTheCardStaysMissing(t *testing.T) {
	guard := nodeGuard{
		Vendor: VendorNVIDIA,
		Nodes:  nvidiaNodesFor(0),
		Exists: present("/dev/nvidiactl"),
		Create: func(context.Context) error { return nil },
		Log:    quiet,
	}

	err := guard.run(context.Background())
	if err == nil {
		t.Fatal("a start with no /dev/nvidia0 was allowed to continue")
	}

	if !strings.Contains(err.Error(), "/dev/nvidia0") {
		t.Fatalf("the message does not say which node is missing: %v", err)
	}
}

// TestGuardNamesBothMissingNodes covers the machine where the driver has not
// been touched at all, so that the message is about the driver rather than
// about one file.
func TestGuardNamesBothMissingNodes(t *testing.T) {
	var said string

	guard := nodeGuard{
		Vendor: VendorNVIDIA,
		Nodes:  nvidiaNodesFor(0),
		Exists: present(),
		Create: func(context.Context) error { return nil },
		Log:    func(f string, a ...any) { said = f },
	}

	err := guard.run(context.Background())
	if err == nil {
		t.Fatal("a host with no nvidia nodes at all was allowed to start a seat")
	}

	for _, node := range nvidiaNodesFor(0) {
		if !strings.Contains(err.Error(), node) {
			t.Fatalf("%s is missing from the message: %v", node, err)
		}
	}

	if said == "" {
		t.Fatal("nothing was written to the seat log before creating them")
	}
}

// TestModprobeReportsWhatWentWrong runs the real command path against programs
// the test writes, rather than a stand-in for exec, because the three answers
// that matter are all answers from the operating system.
func TestModprobeReportsWhatWentWrong(t *testing.T) {
	dir := t.TempDir()

	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}

		return path
	}

	restore := modprobeBin
	t.Cleanup(func() { modprobeBin = restore })

	// The arguments are checked rather than assumed, because getting them wrong
	// is not a compile error and not a runtime error either: "nvidia-modprobe
	// -c 0 -u" exits 0 and creates nothing, since -u replaces the module it
	// works on rather than adding to it. That spelling was written here first
	// and only the machine found it.
	t.Run("success", func(t *testing.T) {
		args := filepath.Join(dir, "args")
		modprobeBin = write("ok", "printf '%s' \"$*\" > "+args+"\nexit 0\n")

		if err := nvidiaModprobe(context.Background(), 0); err != nil {
			t.Fatalf("a command that succeeded was reported as a failure: %v", err)
		}

		got, err := os.ReadFile(args)
		if err != nil {
			t.Fatal(err)
		}

		if string(got) != "-c 0" {
			t.Fatalf("called with %q, want %q", got, "-c 0")
		}
	})

	// Both spellings, because they fail differently: a bare name is a PATH
	// lookup and an absolute path is a stat, and production uses the first
	// while a test naturally reaches for the second.
	for _, spelling := range []struct{ what, bin string }{
		{"not installed", "polyseat-no-such-program"},
		{"not at that path", filepath.Join(dir, "no-such-program")},
	} {
		t.Run(spelling.what, func(t *testing.T) {
			modprobeBin = spelling.bin

			err := nvidiaModprobe(context.Background(), 0)
			if err == nil {
				t.Fatal("a missing nvidia-modprobe was reported as success")
			}

			if !strings.Contains(err.Error(), "nvidia-utils") {
				t.Fatalf("the message does not say where it comes from: %v", err)
			}
		})
	}

	t.Run("failed", func(t *testing.T) {
		modprobeBin = write("angry", "echo 'no permission to create /dev/nvidia0' >&2\nexit 1\n")

		err := nvidiaModprobe(context.Background(), 0)
		if err == nil {
			t.Fatal("a command that exited 1 was reported as success")
		}

		// The tool's own words, because they are more specific than anything
		// this program could say about why it failed.
		if !strings.Contains(err.Error(), "no permission") {
			t.Fatalf("the command's own output was dropped: %v", err)
		}
	})
}

// fakeProc makes a /proc with the driver's description of one card in it, the
// real description from the machine this was written on with only the minor
// number changed. Written at run time rather than kept under testdata, because
// the directory is named by the PCI address and a colon is not something a Go
// module may carry in a path.
func fakeProc(t *testing.T, pci string, minor int) string {
	t.Helper()

	real, err := os.ReadFile("testdata/nvidia-gpu-information.txt")
	if err != nil {
		t.Fatal(err)
	}

	info := strings.Replace(string(real), "Device Minor: \t 0",
		fmt.Sprintf("Device Minor: \t %d", minor), 1)
	if minor != 0 && info == string(real) {
		t.Fatal("the recorded description no longer has the Device Minor line this edits")
	}

	root := t.TempDir()
	dir := filepath.Join(root, "driver/nvidia/gpus", pci)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "information"), []byte(info), 0o644); err != nil {
		t.Fatal(err)
	}

	return root
}

func TestNvidiaMinorIsWhatTheDriverSays(t *testing.T) {
	if minor, ok := nvidiaMinor(fakeProc(t, "0000:01:00.0", 0), "0000:01:00.0"); !ok || minor != 0 {
		t.Errorf("the real description read as %d, %v; want 0, true", minor, ok)
	}

	if minor, ok := nvidiaMinor(fakeProc(t, "0000:02:00.0", 1), "0000:02:00.0"); !ok || minor != 1 {
		t.Errorf("a second card read as %d, %v; want 1, true", minor, ok)
	}

	// A card the driver does not describe, and one with no address at all, are
	// not guessed at here: the caller decides what to fall back to.
	root := fakeProc(t, "0000:01:00.0", 0)

	for _, pci := range []string{"0000:09:00.0", ""} {
		if _, ok := nvidiaMinor(root, pci); ok {
			t.Errorf("an answer was made up for %q", pci)
		}
	}
}

// The guard as the manager builds it, for a seat pinned to a second card. The
// node it has to see is that card's, and nvidia-modprobe has to be asked for
// that card; card 0 is what both used to be, whatever the seat was given.
func TestTheGuardLooksForTheSeatsOwnCard(t *testing.T) {
	const pci = "0000:02:00.0"

	restoreProc, restoreBin := procRoot, modprobeBin
	t.Cleanup(func() { procRoot, modprobeBin = restoreProc, restoreBin })

	procRoot = fakeProc(t, pci, 7)

	if _, err := os.Stat("/dev/nvidia7"); err == nil {
		t.Skip("this machine has a /dev/nvidia7, so the guard would rightly do nothing")
	}

	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	modprobeBin = filepath.Join(dir, "nvidia-modprobe")

	if err := os.WriteFile(modprobeBin, []byte("#!/bin/sh\nprintf '%s' \"$*\" > "+args+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := &Manager{gpu: GPU{Vendor: VendorNVIDIA, PCI: pci},
		rt: map[string]*runtime{}, subs: map[int]chan struct{}{}}

	err := m.awaitGPU(context.Background(), "vince")
	if err == nil || !strings.Contains(err.Error(), "/dev/nvidia7") {
		t.Errorf("the guard did not look for the seat's card: %v", err)
	}

	got, readErr := os.ReadFile(args)
	if readErr != nil {
		t.Fatalf("nvidia-modprobe was never run: %v", readErr)
	}

	if string(got) != "-c 7" {
		t.Errorf("nvidia-modprobe was asked for %q, want %q", got, "-c 7")
	}
}
