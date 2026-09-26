package seat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// homeSeat is a seat whose home is a directory of the test's, and which runs
// only what is run as the player.
//
// Anything else is refused rather than run, because everything the daemon does
// in the player's home has to be done as the player: that is the property these
// tests are about, and a command that reached here as root is the bug. What is
// run as the player runs here as whoever runs the test, with the home's path
// swapped for the directory, which is exactly as far as the player could reach.
type homeSeat struct {
	root string
	ran  int
}

func (s *homeSeat) Exec(ctx context.Context, _ string, argv []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(argv) < 4 || argv[0] != "sudo" || argv[1] != "-u" || argv[2] != Player {
		return -1, fmt.Errorf("run as root in the player's home: %q", argv)
	}

	args := make([]string, 0, len(argv)-3)
	for _, arg := range argv[3:] {
		args = append(args, strings.ReplaceAll(arg, playerHome, s.root))
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	s.ran++

	var exit *exec.ExitError

	switch err := cmd.Run(); {
	case err == nil:
		return 0, nil
	case errors.As(err, &exit):
		return exit.ExitCode(), nil
	default:
		return -1, err
	}
}

// homeProvisioner is a provisioner with nothing but a home, so that a call that
// still went to Incus would stop the test on a nil client.
func homeProvisioner(t *testing.T) (*Provisioner, *homeSeat) {
	t.Helper()

	seat := &homeSeat{root: t.TempDir()}

	return &Provisioner{
		Seat: Seat{Name: "living-room", Resolution: "1920x1080"},
		Log:  func(string, ...any) {},
		uid:  1000,
		home: seat,
	}, seat
}

// inHome is where a path in the player's home is in the test's directory.
func (s *homeSeat) inHome(path string) string {
	return strings.Replace(path, playerHome, s.root, 1)
}

// lure stands a link at a path in the home, pointing at a file outside it, the
// way a player who wants root would: /etc/ld.so.preload, written or handed
// over by a daemon that follows the link, is root in the seat. It answers the
// file the link points at.
func (s *homeSeat) lure(t *testing.T, path string) string {
	t.Helper()

	target := filepath.Join(t.TempDir(), "ld.so.preload")
	if err := os.WriteFile(target, []byte("root's\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	at := s.inHome(path)
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(target, at); err != nil {
		t.Fatal(err)
	}

	return target
}

// untouched checks that the link's target is as it was and that a file of the
// daemon's now stands where the link was.
func (s *homeSeat) untouched(t *testing.T, path, target string) {
	t.Helper()

	if got, err := os.ReadFile(target); err != nil || string(got) != "root's\n" {
		t.Errorf("the file behind the link at %s was written through: %q, %v", path, got, err)
	}

	info, err := os.Lstat(s.inHome(path))
	if err != nil {
		t.Fatal(err)
	}

	if !info.Mode().IsRegular() {
		t.Errorf("%s is a %v rather than the daemon's file", path, info.Mode().Type())
	}
}

func TestThePointerConfigReplacesALinkRatherThanWritingThroughIt(t *testing.T) {
	p, home := homeProvisioner(t)
	target := home.lure(t, PointerConfigPath)

	if err := p.WritePointerConfig(context.Background()); err != nil {
		t.Fatal(err)
	}

	home.untouched(t, PointerConfigPath, target)

	if got, _ := os.ReadFile(home.inHome(PointerConfigPath)); !strings.Contains(string(got), "speed=0.90") {
		t.Errorf("the pointer config reads %q", got)
	}
}

func TestTheSunshineConfigReplacesALinkRatherThanWritingThroughIt(t *testing.T) {
	p, home := homeProvisioner(t)
	target := home.lure(t, SunshineConfigPath)

	if _, err := p.writeSunshineConfig(context.Background(), map[string][]string{
		"eth1": {"192.168.1.40"},
	}); err != nil {
		t.Fatal(err)
	}

	home.untouched(t, SunshineConfigPath, target)

	if got, _ := os.ReadFile(home.inHome(SunshineConfigPath)); !strings.Contains(string(got), "192.168.1.40") {
		t.Errorf("the Sunshine config does not carry the address: %q", got)
	}
}

// Steam's configuration is read before it is written, and the read is the
// player's as well: a link there shows the daemon what the player could read
// anyway, and the write that follows replaces the link.
func TestSteamsDefaultToolReplacesALinkRatherThanWritingThroughIt(t *testing.T) {
	p, home := homeProvisioner(t)
	target := home.lure(t, steamConfigPath)

	if err := os.WriteFile(target, steamConfig(t, "config-without-mapping.vdf"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := p.setCompatTool(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got, _ := os.ReadFile(target); mappedTool(t, got) != "" {
		t.Error("the file behind the link was given the default")
	}

	written, err := os.ReadFile(home.inHome(steamConfigPath))
	if err != nil {
		t.Fatal(err)
	}

	if info, _ := os.Lstat(home.inHome(steamConfigPath)); !info.Mode().IsRegular() {
		t.Errorf("the link is still there: %v", info.Mode().Type())
	}

	if got := mappedTool(t, written); got != protonName {
		t.Errorf("the default reads %q", got)
	}
}

// The read used to swallow every error and write a fresh file with nothing but
// the default in it, over whatever Steam had. Something there that cannot be
// read is left alone now.
func TestSteamsConfigurationIsLeftAloneWhenItCannotBeRead(t *testing.T) {
	p, home := homeProvisioner(t)

	if err := os.MkdirAll(home.inHome(steamConfigPath), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := p.setCompatTool(context.Background()); err != nil {
		t.Fatal(err)
	}

	if info, err := os.Lstat(home.inHome(steamConfigPath)); err != nil || !info.IsDir() {
		t.Errorf("something unreadable was replaced: %v, %v", info, err)
	}
}

// A seat whose Steam has never run has no file, and gets one.
func TestSteamsDefaultToolGoesIntoASeatWithNoSteamYet(t *testing.T) {
	p, home := homeProvisioner(t)

	if err := p.setCompatTool(context.Background()); err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(home.inHome(steamConfigPath))
	if err != nil {
		t.Fatal(err)
	}

	if got := mappedTool(t, written); got != protonName {
		t.Errorf("the default reads %q", got)
	}
}

func TestTheOldLibraryEntryIsTakenOutWithoutWritingThroughALink(t *testing.T) {
	p, home := homeProvisioner(t)

	folders, err := os.ReadFile("../library/testdata/libraryfolders-two.vdf")
	if err != nil {
		t.Fatal(err)
	}

	path := steamRoot + "/config/libraryfolders.vdf"
	target := home.lure(t, path)

	if err := os.WriteFile(target, folders, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := p.unregisterOldLibrary(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got, _ := os.ReadFile(target); string(got) != string(folders) {
		t.Error("the file behind the link was changed")
	}

	info, err := os.Lstat(home.inHome(path))
	if err != nil {
		t.Fatal(err)
	}

	if !info.Mode().IsRegular() {
		t.Errorf("the link is still there: %v", info.Mode().Type())
	}

	if got, _ := os.ReadFile(home.inHome(path)); strings.Contains(string(got), LibraryMount) {
		t.Errorf("the entry is still in the file:\n%s", got)
	}
}

func TestTheSharedDirectorysNoteReplacesALinkRatherThanWritingThroughIt(t *testing.T) {
	p, home := homeProvisioner(t)
	path := LibraryMount + "/shared/README.txt"
	target := home.lure(t, path)

	if err := p.shareLibrary(context.Background()); err != nil {
		t.Fatal(err)
	}

	home.untouched(t, path, target)

	if info, err := os.Stat(home.inHome(LibraryMount + "/steamapps")); err != nil || !info.IsDir() {
		t.Errorf("the Steam half of the shared directory was not made: %v", err)
	}
}

// The session's files and the drop-ins beside the units, one link each.
func TestTheSessionFilesReplaceLinksRatherThanWritingThroughThem(t *testing.T) {
	for _, path := range []string{
		playerHome + "/.config/sway/config",
		playerHome + "/.config/waybar/style.css",
		playerHome + "/.config/systemd/user/polyseat-sway.service",
		playerHome + "/.config/systemd/user/polyseat-sunshine.service.d/10-seat.conf",
		playerHome + "/.config/systemd/user/polyseat-sway.service.d/20-gpu.conf",
	} {
		t.Run(filepath.Base(filepath.Dir(path))+"/"+filepath.Base(path), func(t *testing.T) {
			p, home := homeProvisioner(t)
			target := home.lure(t, path)

			if err := p.sessionHome(context.Background()); err != nil {
				t.Fatal(err)
			}

			home.untouched(t, path, target)
		})
	}
}

// The directories come from the player as well: the file API makes them one
// component at a time as root and follows a link at any of them, so ~/.config
// made a link to /etc gave the player directories of their own in there.
// homeSeat refuses whatever is not run as the player, so a directory made the
// old way does not appear here.
func TestTheSessionDirectoriesAreMadeByThePlayer(t *testing.T) {
	p, home := homeProvisioner(t)

	if err := p.sessionHome(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{"Applications", "Downloads", ".config/nwg-drawer", ".config/sunshine"} {
		if info, err := os.Stat(filepath.Join(home.root, dir)); err != nil || !info.IsDir() {
			t.Errorf("%s was not made: %v", dir, err)
		}
	}
}

func TestHiddenLauncherEntriesReplaceALinkRatherThanWritingThroughIt(t *testing.T) {
	p, home := homeProvisioner(t)
	path := playerHome + "/.local/share/applications/" + clutter[0] + ".desktop"
	target := home.lure(t, path)

	if err := p.tidyLauncher(context.Background()); err != nil {
		t.Fatal(err)
	}

	home.untouched(t, path, target)
}

// runGiveBack runs giveBackHome the way takeHomeBack does, over a home of the
// test's own, for a player whose uid is the one given.
func runGiveBack(t *testing.T, home string, uid int, dirs ...string) (string, error) {
	t.Helper()

	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}

	args := []string{"-I", "-c", giveBackHome, home, strconv.Itoa(uid)}
	for _, dir := range dirs {
		args = append(args, strings.Replace(dir, playerHome, home, 1))
	}

	out, err := exec.Command("python3", args...).CombinedOutput()

	return string(out), err
}

// The repair runs as root and changes ownership, so a link on the way has to
// end it. It used to be chown on each path, and chown follows links: ~/.local
// made a link to /etc handed /etc to the player on the next run.
//
// The test is not root and cannot give anything away, so the uid it asks for
// is one it does not have: a directory the script really reached would fail on
// permission rather than be skipped, and it has to be skipped. Only the
// deepest directory is asked for, so that the walk to it crosses the link
// wherever it stands and nothing before the link is handed over on its own.
func TestGivingTheHomeBackFollowsNoLink(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("as root the handover succeeds, and the link's target would be given away for real")
	}

	dirs := playerDirs()
	deepest := dirs[len(dirs)-1]

	for _, link := range dirs {
		t.Run(strings.TrimPrefix(link, playerHome+"/"), func(t *testing.T) {
			home := t.TempDir()

			// A tree like the one the link would lead to, so that following it
			// finds the directory the repair asks for.
			elsewhere := filepath.Join(t.TempDir(), "etc")
			if err := os.MkdirAll(filepath.Join(elsewhere, strings.TrimPrefix(deepest, link)), 0o755); err != nil {
				t.Fatal(err)
			}

			at := strings.Replace(link, playerHome, home, 1)
			if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
				t.Fatal(err)
			}

			if err := os.Symlink(elsewhere, at); err != nil {
				t.Fatal(err)
			}

			if out, err := runGiveBack(t, home, os.Getuid()+1, deepest); err != nil || out != "" {
				t.Errorf("the link at %s was followed: %q, %v", link, out, err)
			}
		})
	}
}

// And a directory that really is in the home is reached: asked to give it to a
// uid that is not the test's, the script tries, which as anybody but root fails
// on permission. The same directory already the player's is left as it is.
func TestGivingTheHomeBackReachesWhatIsReallyThere(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("as root the handover succeeds and this cannot tell it from a skip")
	}

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".local/share/Steam/config"), 0o755); err != nil {
		t.Fatal(err)
	}

	if out, err := runGiveBack(t, home, os.Getuid(), playerDirs()...); err != nil || out != "" {
		t.Errorf("directories that are already the player's were handed over: %q, %v", out, err)
	}

	for _, dir := range playerDirs() {
		if _, err := runGiveBack(t, home, os.Getuid()+1, dir); err == nil {
			t.Errorf("%s is not the player's and was not even tried", dir)
		}
	}
}

// Nothing in provisioning reaches the player's home through the Incus file
// API, which writes and reads as root and follows a link on the way. The tests
// above cover each place that used to; this is for the next one, which would
// otherwise look like every other PushFile in the file. It reads names, so a
// path handed over in a variable gets past it, as several of the old places
// would have, and those are what the tests above are for.
func TestProvisioningDoesNotUseTheFileAPIInThePlayersHome(t *testing.T) {
	source, err := os.ReadFile("provision.go")
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(string(source), "\n")

	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}

		if !strings.Contains(line, "Client.PushFile(") && !strings.Contains(line, "Client.PushStream(") &&
			!strings.Contains(line, "Client.MakeDir(") && !strings.Contains(line, "Client.ReadFile(") {
			continue
		}

		// A call can carry its path on the line after.
		call := line
		if i+1 < len(lines) {
			call += lines[i+1]
		}

		for _, home := range []string{
			"playerHome", `"/home/"`, "home+", "home +", "LibraryMount", "steamRoot", "steamApps",
			"steamConfigPath", "SunshineConfigPath", "PointerConfigPath", "SessionPath",
			"AppsPath", "entryDir", "DropDir", "AppImageDir",
		} {
			if strings.Contains(call, home) {
				t.Errorf("provision.go:%d reaches the player's home through the file API: %s",
					i+1, strings.TrimSpace(line))
			}
		}
	}
}
