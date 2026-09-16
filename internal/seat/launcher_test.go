package seat

import (
	"fmt"
	"net"
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
// The script itself with a stub in place of the drawer, rather than a check that
// the file contains the word unset: what has to hold is the environment the
// started process really gets.
func TestLauncherStartsTheDesktopWithoutTheOpenGLShim(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")

	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}

	dump := filepath.Join(home, "environment")

	stubs := map[string]string{
		// Stands in for the launcher and records what it was started with.
		"nwg-drawer": "#!/bin/sh\nenv > " + dump + "\n",
		// So the script does not decide one is already running, which on a
		// machine that happens to have the drawer open would make this test
		// pass without starting anything at all.
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
// browser: fuzzel's configuration used to start everything through the mangohud
// wrapper, which preloads the shim regardless of what the launcher was started
// with. Clearing the variable and then wrapping every command in the thing that
// sets it again would have looked fixed and changed nothing.
//
// nwg-drawer has no launch prefix, so the way back in for the same mistake is
// its -wm option or a wrapper put in front of it. -wm sway starts everything
// through `swaymsg exec`, which is a shell, and the drawer is the one thing in
// the seat that has to start what it is given exactly as written.
func TestTheLauncherDoesNotWrapWhatItStarts(t *testing.T) {
	script := string(asset("assets/launcher.sh"))

	// The command that starts it, followed across its continuation lines.
	var command []string

	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)

		if len(command) == 0 && !strings.HasPrefix(trimmed, "setsid nwg-drawer") {
			continue
		}

		command = append(command, strings.TrimSuffix(trimmed, "\\"))

		if !strings.HasSuffix(trimmed, "\\") {
			break
		}
	}

	if len(command) == 0 {
		t.Fatal("found no line that starts the drawer in the launcher script, so this checked nothing")
	}

	joined := strings.Join(command, " ")

	for _, bad := range []string{"mangohud", "-wm", "LD_PRELOAD"} {
		if strings.Contains(joined, bad) {
			t.Errorf("the drawer is started as %q, which has %q in it, so what is started "+
				"from the desktop no longer gets the environment the script prepared", joined, bad)
		}
	}
}

// launcherStubs builds a seat's worth of fakes around the script: a drawer that
// records the environment it was started with, and a pgrep/pkill pair that agree
// with each other through a file, so that a test can say whether the launcher is
// open and the script can change that.
//
// Stateful rather than fixed, because refresh is the one verb whose whole
// behaviour is a function of that state and a stub answering the same thing
// forever would let it pass without doing anything.
func launcherStubs(t *testing.T) (home, dump, state, script string) {
	t.Helper()

	home = t.TempDir()
	bin := filepath.Join(home, "bin")

	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}

	dump = filepath.Join(home, "environment")
	state = filepath.Join(home, "open")

	// The fake drawer says it is running before it says anything else. A test
	// waits on the environment dump and then asks whether the launcher is open,
	// and with the two writes the other way round there was a moment in between
	// where the dump was there and the launcher was not: a started process that
	// had not got to its second line yet, read as a refresh that closed the
	// launcher and left nothing in its place. It failed one CI run in two on
	// 0.13.3 and never on the machine this is written on, which is what that
	// order buys.
	stubs := map[string]string{
		// The environment dump is written last, because that is the file every
		// test here waits on: written first, a test could read the arguments
		// before the stub had got round to writing them.
		"nwg-drawer": "#!/bin/sh\ntouch " + state + "\n" +
			"echo \"$@\" > " + dump + ".argv\nenv > " + dump + "\n",
		"pgrep": "#!/bin/sh\n[ -e " + state + " ]\n",
		"pkill": "#!/bin/sh\nrm -f " + state + "\n",
	}

	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	script = filepath.Join(home, "polyseat-launcher")
	if err := os.WriteFile(script, asset("assets/launcher.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	return home, dump, state, script
}

// sunshineEnv is what a prep command really gets: Sunshine's unit plus the app
// environment from apps.json. XDG_DATA_DIRS is deliberately not in it, because
// that is the case under test.
func sunshineEnv(home string) []string {
	return []string{
		"HOME=" + home,
		"PATH=" + filepath.Join(home, "bin") + ":/usr/bin:/bin",
		"XDG_RUNTIME_DIR=" + home,
		"WAYLAND_DISPLAY=wayland-1",
		"MANGOHUD=1",
		"LD_PRELOAD=" + mangoHudPreload,
	}
}

// waitFor gives a detached process time to write the file that proves it ran.
func waitFor(t *testing.T, path string) []byte {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for {
		if body, err := os.ReadFile(path); err == nil && len(body) > 0 {
			return body
		}

		if time.Now().After(deadline) {
			t.Fatalf("nothing wrote %s, so this test checked nothing", path)
		}

		time.Sleep(20 * time.Millisecond)
	}
}

// The launcher has to find the desktop entries of the player's own flatpaks
// however it was opened.
//
// polyseat-sway.service carries XDG_DATA_DIRS and Sunshine's unit does not, so a
// launcher opened by a prep command, which is what picking Desktop in Moonlight
// does, listed only what the distribution had installed. Every flatpak somebody
// added was missing from it while `flatpak run` started the same application
// from a terminal perfectly well, which makes the launcher the last place anyone
// would look.
func TestLauncherFindsFlatpakEntriesWhateverOpenedIt(t *testing.T) {
	home, dump, _, script := launcherStubs(t)

	cmd := exec.Command("/bin/sh", script, "show")
	cmd.Env = sunshineEnv(home)

	if err := cmd.Run(); err != nil {
		t.Fatalf("the launcher script failed: %v", err)
	}

	body := string(waitFor(t, dump))

	var dirs string

	for _, line := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(line, "XDG_DATA_DIRS="); ok {
			dirs = v
		}
	}

	want := home + "/.local/share/flatpak/exports/share"
	if !strings.Contains(dirs, want) {
		t.Errorf("the launcher was started with XDG_DATA_DIRS=%q, which does not "+
			"contain %q, so nothing the player installed as a flatpak appears in "+
			"the menu", dirs, want)
	}

	// Setting the variable at all is how the specification's default is lost, and
	// a launcher listing the flatpaks and none of the system applications would
	// be a different way to be broken rather than a fix.
	if !strings.Contains(dirs, "/usr/share") {
		t.Errorf("the launcher was started with XDG_DATA_DIRS=%q, so the system "+
			"applications are gone from the menu", dirs)
	}
}

// refresh exists because the drawer reads the entries once, when it starts, and
// the session leaves one open. Something installed afterwards is missing from a menu
// that is already on screen until somebody dismisses it, and show does nothing
// while one is running, so the daemon needs a way to say "read it again".
func TestLauncherRefreshRestartsAnOpenLauncher(t *testing.T) {
	home, dump, state, script := launcherStubs(t)

	// A launcher that is already open, and no record of one having been started.
	if err := os.WriteFile(state, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/sh", script, "refresh")
	cmd.Env = sunshineEnv(home)

	if err := cmd.Run(); err != nil {
		t.Fatalf("the launcher script failed: %v", err)
	}

	waitFor(t, dump)

	if _, err := os.Stat(state); err != nil {
		t.Error("refresh closed the launcher and did not open it again, which " +
			"leaves the seat with no menu at all")
	}
}

// The other half, and the reason refresh is not simply hide plus show: an
// install finishing while somebody is playing must not put a launcher on top of
// their game.
func TestLauncherRefreshLeavesAClosedLauncherClosed(t *testing.T) {
	home, dump, _, script := launcherStubs(t)

	cmd := exec.Command("/bin/sh", script, "refresh")
	cmd.Env = sunshineEnv(home)

	if err := cmd.Run(); err != nil {
		t.Fatalf("the launcher script failed: %v", err)
	}

	// Long enough that a launcher started detached would have written the file.
	time.Sleep(500 * time.Millisecond)

	if _, err := os.Stat(dump); err == nil {
		t.Error("refresh opened a launcher that was not open, so an install " +
			"finishing during a game puts a menu over it")
	}
}

// The grid follows the size of the screen, because the screen is whatever the
// client asked for: icons that fill a phone are a strip of stamps on a 4K
// television across a room.
//
// Both halves are checked, and the second is the one that was missed first
// time round. GDK_SCALE looks like the whole answer and GTK on Wayland ignores
// it, measured in a seat at 3840x2160, so the size has to arrive as the icon
// flag and the text as GDK_DPI_SCALE. A test that only counted the icons would
// pass against a drawer with captions nobody can read.
func TestLauncherDrawsTheGridForTheScreenItIsOn(t *testing.T) {
	for name, tc := range map[string]struct {
		height    string
		icons     string
		textScale string
	}{
		"a 1080p client":     {height: "1080", icons: "-is 96", textScale: ""},
		"a 1440p client":     {height: "1440", icons: "-is 96", textScale: ""},
		"a 4K television":    {height: "2160", icons: "-is 192", textScale: "GDK_DPI_SCALE=2"},
		"sway cannot answer": {height: "", icons: "-is 96", textScale: ""},
	} {
		t.Run(name, func(t *testing.T) {
			home, dump, _, script := launcherStubs(t)

			// Stands in for sway. An empty height is the compositor answering
			// nothing at all, which is every call made before the session is
			// up and any call where the socket has gone.
			answer := "[]"
			if tc.height != "" {
				answer = `[{"name":"HEADLESS-1","rect":{"width":1920,"height":` + tc.height + `}}]`
			}

			swaymsg := "#!/bin/sh\ncat <<'JSON'\n" + answer + "\nJSON\n"
			if err := os.WriteFile(filepath.Join(home, "bin", "swaymsg"), []byte(swaymsg), 0o755); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command("/bin/sh", script, "show")
			cmd.Env = sunshineEnv(home)

			if err := cmd.Run(); err != nil {
				t.Fatalf("the launcher script failed: %v", err)
			}

			environment := string(waitFor(t, dump))

			argv, err := os.ReadFile(dump + ".argv")
			if err != nil {
				t.Fatalf("the drawer recorded no arguments: %v", err)
			}

			if !strings.Contains(string(argv), tc.icons) {
				t.Errorf("the drawer was started with %q, want %q in it", strings.TrimSpace(string(argv)), tc.icons)
			}

			scaled := strings.Contains(environment, "GDK_DPI_SCALE=")

			switch {
			case tc.textScale == "" && scaled:
				t.Error("the text is scaled on a screen that does not need it, so the labels are too large")
			case tc.textScale != "" && !strings.Contains(environment, tc.textScale):
				t.Errorf("the drawer was started without %s, so a 4K client gets large icons with captions it cannot read", tc.textScale)
			}
		})
	}
}

// The launcher has to find the session's own Wayland socket when nothing told
// it which one that is, which is every call that did not come from sway.
//
// Newest wins, because a socket left behind by a previous sway is still lying in
// the runtime directory after a restart and there is nothing about it that says
// so. The lock file beside each socket is not one, which is the other half: it
// sorts after the socket it belongs to and would be picked in its place.
func TestLauncherPicksTheNewestWaylandSocket(t *testing.T) {
	home, dump, _, script := launcherStubs(t)

	var newest string

	for i, age := range []time.Duration{2 * time.Hour, 0} {
		path := filepath.Join(home, fmt.Sprintf("wayland-%d", i))

		l, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}

		defer l.Close()

		// A real session has one of these beside every socket, and it is an
		// ordinary file rather than a socket.
		if err := os.WriteFile(path+".lock", nil, 0o644); err != nil {
			t.Fatal(err)
		}

		when := time.Now().Add(-age)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}

		if age == 0 {
			newest = filepath.Base(path)
		}
	}

	// Everything a prep command has, minus the one variable under test.
	var env []string

	for _, kv := range sunshineEnv(home) {
		if !strings.HasPrefix(kv, "WAYLAND_DISPLAY=") {
			env = append(env, kv)
		}
	}

	cmd := exec.Command("/bin/sh", script, "show")
	cmd.Env = env

	if err := cmd.Run(); err != nil {
		t.Fatalf("the launcher script failed: %v", err)
	}

	body := string(waitFor(t, dump))

	if !strings.Contains(body, "WAYLAND_DISPLAY="+newest+"\n") {
		t.Errorf("the launcher was not started on %s, so it either drew on a dead "+
			"session or on a lock file", newest)
	}
}
