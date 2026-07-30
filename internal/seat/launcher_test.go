package seat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The desktop is opened by a Sunshine prep command, so it inherits the
// environment Sunshine gives the applications it starts, and that environment
// contains MangoHud's OpenGL shim. Anything started from the launcher inherits
// it in turn, including a terminal and anything started from that.
//
// Firefox dies of it immediately, every time: measured in a seat, SIGSEGV during
// EGL setup, a minidump written and nothing on screen. This is what stops the
// shim at the launcher, and it is worth a test because the failure is somebody
// else's crash in an unrelated program with nothing to connect it back to here.
//
// The script itself with a stub in place of fuzzel, rather than a check that the
// file contains the word unset: what has to hold is the environment the started
// process really gets.
func TestLauncherStartsTheDesktopWithoutTheOpenGLShim(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")

	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}

	dump := filepath.Join(home, "environment")

	stubs := map[string]string{
		// Stands in for the launcher and records what it was started with.
		"fuzzel": "#!/bin/sh\nenv > " + dump + "\n",
		// So the script does not decide one is already running, which on a
		// machine that happens to have fuzzel open would make this test pass
		// without starting anything at all.
		"pgrep": "#!/bin/sh\nexit 1\n",
	}

	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	script := filepath.Join(home, "polyseat-launcher")
	if err := os.WriteFile(script, asset("assets/launcher.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/sh", script, "show")
	cmd.Env = []string{
		"HOME=" + home,
		"PATH=" + bin + ":/usr/bin:/bin",
		"XDG_RUNTIME_DIR=" + home,
		"WAYLAND_DISPLAY=wayland-1",
		// What Sunshine hands a prep command, which is the whole problem.
		"MANGOHUD=1",
		"LD_PRELOAD=" + mangoHudPreload,
	}

	if err := cmd.Run(); err != nil {
		t.Fatalf("the launcher script failed: %v", err)
	}

	// It detaches what it starts, so the file appears after the script has
	// already returned.
	deadline := time.Now().Add(5 * time.Second)

	var body []byte

	for {
		var err error

		if body, err = os.ReadFile(dump); err == nil && len(body) > 0 {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("the launcher started nothing, so this test checked nothing")
		}

		time.Sleep(20 * time.Millisecond)
	}

	if strings.Contains(string(body), "LD_PRELOAD") {
		t.Error("the desktop is started with MangoHud's shim preloaded, so Firefox " +
			"and anything else that is not a game will crash when it is started " +
			"from the launcher")
	}

	// The other half has to survive. It is harmless outside a game, it is what
	// caps a Vulkan game somebody starts from the desktop, and taking both away
	// would be an easy way to make this test pass and the cap disappear.
	if !strings.Contains(string(body), "MANGOHUD=1") {
		t.Error("MANGOHUD is gone as well, so a game started from the desktop runs uncapped")
	}
}

// The other end of the same problem, and the one that actually killed the
// browser: the launcher's own configuration used to start everything through
// the mangohud wrapper, which preloads the shim regardless of what the launcher
// was started with. Clearing the variable and then wrapping every command in the
// thing that sets it again would have looked fixed and changed nothing.
func TestTheLauncherDoesNotWrapWhatItStarts(t *testing.T) {
	config := string(asset("assets/fuzzel.ini"))

	for _, line := range strings.Split(config, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "launch-prefix") {
			t.Errorf("fuzzel has %q, so everything started from the desktop runs "+
				"with MangoHud's shim preloaded and Firefox crashes on sight", line)
		}
	}
}
