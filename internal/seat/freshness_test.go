package seat

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The two sides write the version differently and neither is going to stop.
// pacman keeps a pkgrel that the release has no concept of, and the git tag
// carries a v that the package drops. Comparing the raw strings is how a seat
// that is exactly current gets offered an update on every single check.
func TestTheTwoWaysOfWritingAVersionCompareEqual(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want string
	}{
		{"what pacman prints", "sunshine 2025.122.4-1", "2025.122.4"},
		{"without a pkgrel", "sunshine 2025.122.4", "2025.122.4"},
		{"with the trailing newline it really has", "sunshine 2025.122.4-1\n", "2025.122.4"},
		{"an epoch stays, it is part of the version", "sunshine 1:2025.122.4-2", "1:2025.122.4"},
		{"not installed", "", ""},
		{"pacman said something else entirely", "error: package not found", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := installedSunshine(tc.out); got != tc.want {
				t.Errorf("installedSunshine(%q) = %q, want %q", tc.out, got, tc.want)
			}
		})
	}

	// The whole point of the normalising: these two are the same release.
	if installedSunshine("sunshine 2025.122.4-1") != normaliseVersion("v2025.122.4") {
		t.Error("the package version and the release tag for one release do not compare equal")
	}
}

// An unknown version is not evidence of anything, and the guard that says so is
// the one worth a test of its own: without it, a machine that cannot reach
// GitHub reports every seat as behind and offers an update that would install
// the version already there.
func TestNothingIsBehindOnAnAnswerThatNeverCame(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    Freshness
		want bool
	}{
		{"github never answered", Freshness{Sunshine: "2025.122.4"}, false},
		{"the seat never answered", Freshness{SunshineLatest: "2026.8.1"}, false},
		{"neither answered", Freshness{}, false},
		{"both answered and they match", Freshness{Sunshine: "2026.8.1", SunshineLatest: "2026.8.1"}, false},
		{"both answered and they differ", Freshness{Sunshine: "2025.122.4", SunshineLatest: "2026.8.1"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.SunshineBehind(); got != tc.want {
				t.Errorf("SunshineBehind() = %v, want %v", got, tc.want)
			}
		})
	}

	// Packages stand on their own: a seat with pending packages is behind even
	// where the Sunshine lookup failed, because those two failures are
	// unrelated and one must not swallow the other.
	behind := Freshness{Sunshine: "2026.8.1", Packages: 12}
	if !behind.Behind() {
		t.Error("a seat with packages waiting is not reported as behind")
	}
}

func TestPendingUpdatesReadsWhatPacmanPrints(t *testing.T) {
	const out = `linux 6.15.2.arch1-1 -> 6.15.3.arch1-1
mesa 25.1.3-1 -> 25.1.4-1
steam 1.0.0.81-2 -> 1.0.0.82-1
sway 1.10-2 -> 1.11-1
vulkan-icd-loader 1.4.313-1 -> 1.4.320-1
wayland 1.23.1-1 -> 1.24.0-1
`

	count, names := pendingUpdates(out)

	if count != 6 {
		t.Errorf("counted %d packages, want 6", count)
	}

	// Capped, because an Arch container left alone for a month has hundreds and
	// a card is not the place to list them.
	if len(names) != namesShown {
		t.Errorf("kept %d names, want %d", len(names), namesShown)
	}

	if names[0] != "linux" {
		t.Errorf("first name is %q, want linux", names[0])
	}

	// The ordinary answer, and the one that must not read as an error: pacman
	// exits 1 with nothing to say when there is nothing waiting.
	if count, names := pendingUpdates(""); count != 0 || names != nil {
		t.Errorf("empty output gave %d and %v, want 0 and nil", count, names)
	}

	// Blank lines are not packages. Left in, output that ends with one is what
	// a shell heredoc really produces.
	if count, _ := pendingUpdates("linux 1 -> 2\n\n\n"); count != 1 {
		t.Errorf("blank lines counted as packages: got %d, want 1", count)
	}
}

// The cache exists to keep four seats from making four requests for one answer,
// and to keep a machine with no route out from asking once per seat per sweep
// forever. The second is the one that is easy to leave out, so both are here.
func TestTheLookupIsMadeOncePerInterval(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	calls := 0
	c := &sunshineCache{ask: func(context.Context) (string, error) {
		calls++

		return "2026.8.1", nil
	}}

	for range 4 {
		version, err := c.latest(context.Background(), clock)
		if err != nil || version != "2026.8.1" {
			t.Fatalf("latest() = %q, %v", version, err)
		}
	}

	if calls != 1 {
		t.Errorf("four seats asking made %d requests, want 1", calls)
	}

	now = now.Add(sunshineAsk + time.Minute)

	if _, err := c.latest(context.Background(), clock); err != nil {
		t.Fatal(err)
	}

	if calls != 2 {
		t.Errorf("after the interval the answer was not asked for again: %d requests", calls)
	}
}

// A failure is cached for the same interval as a success. Without this, the
// offline case is a request to GitHub from every seat on every sweep, which is
// every ten seconds, forever.
func TestAFailedLookupIsAlsoRemembered(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	offline := errors.New("no route to host")
	calls := 0
	c := &sunshineCache{ask: func(context.Context) (string, error) {
		calls++

		return "", offline
	}}

	for range 3 {
		if _, err := c.latest(context.Background(), clock); !errors.Is(err, offline) {
			t.Fatalf("latest() error = %v, want %v", err, offline)
		}
	}

	if calls != 1 {
		t.Errorf("an offline machine asked %d times, want 1", calls)
	}

	// And a version left over from an earlier success does not survive a
	// failure. Reporting the old answer as though it were current would have a
	// seat compared against a release that may no longer be the newest.
	c = &sunshineCache{ask: func(context.Context) (string, error) { return "2026.8.1", nil }}

	if _, err := c.latest(context.Background(), clock); err != nil {
		t.Fatal(err)
	}

	c.ask = func(context.Context) (string, error) { return "", offline }
	now = now.Add(sunshineAsk + time.Minute)

	version, err := c.latest(context.Background(), clock)
	if version != "" || !errors.Is(err, offline) {
		t.Errorf("after a failed lookup: %q, %v, want empty and the error", version, err)
	}
}

// forget is what the button that means "check now" needs. Without it, somebody
// who has just updated Sunshine by hand waits up to six hours for the interface
// to stop offering them an update they have already done.
func TestForgetMakesTheNextLookAskAgain(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC) }

	calls := 0
	c := &sunshineCache{ask: func(context.Context) (string, error) {
		calls++

		return "2026.8.1", nil
	}}

	if _, err := c.latest(context.Background(), clock); err != nil {
		t.Fatal(err)
	}

	c.forget()

	if _, err := c.latest(context.Background(), clock); err != nil {
		t.Fatal(err)
	}

	if calls != 2 {
		t.Errorf("forget did not make the next look ask: %d requests, want 2", calls)
	}
}

func TestSummarySaysBothHalves(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    Freshness
		want string
	}{
		{
			"both",
			Freshness{Sunshine: "2025.122.4", SunshineLatest: "2026.8.1", Packages: 12},
			"Sunshine 2025.122.4 to 2026.8.1, 12 packages",
		},
		{"sunshine alone", Freshness{Sunshine: "2025.122.4", SunshineLatest: "2026.8.1"}, "Sunshine 2025.122.4 to 2026.8.1"},
		{"one package reads as one", Freshness{Packages: 1}, "1 package"},
		{"nothing", Freshness{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.Summary(); got != tc.want {
				t.Errorf("Summary() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A seat in the middle of being built must not be asked what it has installed.
// The answer would be whatever the half finished container happened to say, and
// it would then be written down as this seat's state and shown on its card.
func TestCheckingIsRefusedWhileASeatIsBusy(t *testing.T) {
	m := &Manager{rt: map[string]*runtime{}}

	rt := m.runtimeOf("seat1")
	rt.busy = "provisioning"

	if _, err := m.CheckFreshness("seat1"); !errors.Is(err, ErrBusy) {
		t.Errorf("CheckFreshness on a busy seat = %v, want ErrBusy", err)
	}

	// And the refusal leaves what was already known alone rather than
	// overwriting it with the empty answer it did not get.
	rt.fresh = Freshness{Sunshine: "2025.122.4", SunshineLatest: "2026.8.1"}

	if _, err := m.CheckFreshness("seat1"); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}

	if !rt.fresh.Behind() {
		t.Error("a refused check cleared what the seat was known to be behind on")
	}
}

// "The seat is not running" is a fact about now, and storing a fact about now
// is how the card came to say a seat was off while it was running: the reading
// was taken while it was stopped, the seat was started, and nothing replaced
// what had been written down. So it is refused rather than recorded.
func TestAStoppedSeatIsRefusedRatherThanDescribed(t *testing.T) {
	m := &Manager{rt: map[string]*runtime{}, sunshine: &sunshineCache{}}

	rt := m.runtimeOf("seat1")
	rt.state = StateStopped

	// Something worth keeping, from when the seat was last up.
	known := Freshness{Sunshine: "2026.8.1", SunshineLatest: "2026.8.1"}
	m.record("seat1", known)

	if _, err := m.CheckFreshness("seat1"); !errors.Is(err, ErrNotRunning) {
		t.Errorf("CheckFreshness on a stopped seat = %v, want ErrNotRunning", err)
	}

	// And the refusal changed nothing. What was found while the seat was up is
	// old, which the card says, and old is not the same as wrong.
	if rt.fresh.Sunshine != known.Sunshine || rt.freshChecked.IsZero() {
		t.Error("the refusal overwrote what was known from when the seat was up")
	}

	if rt.fresh.Problem != "" {
		t.Errorf("a reason about the seat's current state was stored: %q", rt.fresh.Problem)
	}

	// Started, and the same call now goes through to the seat rather than
	// being turned away by the state.
	rt.state = StateRunning

	if m.running("seat1") != true {
		t.Error("a running seat is not reported as running")
	}
}

// The bug this exists for: a seat was updated, the work finished, and the card
// went on listing the versions that had just been installed as waiting. The
// reading it draws from was the one taken before the update, and nothing
// replaced it until the six hour pass came round or somebody pressed the
// button. Three moments take a look and only two of them wrote it down.
func TestALookIsWrittenDownWhereverItWasTaken(t *testing.T) {
	m := &Manager{rt: map[string]*runtime{}}

	behind := Freshness{Sunshine: "2025.122.4", SunshineLatest: "2026.8.1", Packages: 12}
	m.record("seat1", behind)

	rt := m.runtimeOf("seat1")

	if !rt.fresh.Behind() {
		t.Fatal("the first look was not stored")
	}

	if rt.freshChecked.IsZero() {
		t.Error("the reading was stored without the time it was taken")
	}

	// What an update does when it has finished: ask again and write down what
	// came back. The card has to stop saying the old thing.
	m.record("seat1", Freshness{Sunshine: "2026.8.1", SunshineLatest: "2026.8.1"})

	if rt.fresh.Behind() {
		t.Error("after a second look found nothing waiting, the seat is still reported as behind")
	}

	if rt.fresh.Summary() != "" {
		t.Errorf("the old summary survived the new look: %q", rt.fresh.Summary())
	}
}

// The log line is worth one appearance, not four a day for as long as a seat
// stays behind. It says something when the answer changes and nothing when it
// repeats.
func TestTheSameAnswerIsNotLoggedTwice(t *testing.T) {
	m := &Manager{rt: map[string]*runtime{}}

	behind := Freshness{Sunshine: "2025.122.4", SunshineLatest: "2026.8.1"}

	lines := func() int { return len(m.runtimeOf("seat1").log.Lines()) }

	m.record("seat1", behind)

	after := lines()
	if after == 0 {
		t.Fatal("a seat found to be behind said nothing about it")
	}

	m.record("seat1", behind)
	m.record("seat1", behind)

	if lines() != after {
		t.Errorf("the same answer was logged again: %d lines, want %d", lines(), after)
	}

	// A different answer is news again.
	m.record("seat1", Freshness{Sunshine: "2025.122.4", SunshineLatest: "2026.9.1", Packages: 3})

	if lines() == after {
		t.Error("a changed answer was not logged")
	}
}

// runCheckUpdates runs the real script against stubbed commands.
//
// The script is what runs inside every seat as root and the only thing that
// ever reports why the package list could not be refreshed, so it is worth
// running rather than reading. pacman and timeout are both stubs: the seconds
// this bounds are real ones, and a test that waits out a real timeout is a test
// nobody runs twice.
func runCheckUpdates(t *testing.T, tmp, pacman, timeout string) (string, int) {
	t.Helper()

	dir := t.TempDir()

	for name, body := range map[string]string{"pacman": pacman, "timeout": timeout} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// A local database to link, since the script insists on one being there.
	local := filepath.Join(dir, "root", "var", "lib", "pacman", "local")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}

	script := strings.ReplaceAll(checkUpdatesScript, "/var/lib/pacman/local", local)

	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(),
		"PATH="+dir+":"+os.Getenv("PATH"),
		// Where the script puts its database. A parameter rather than this
		// run's own directory, because the one test that matters most here
		// needs two runs to share it: given a directory each, two runs cannot
		// collide whatever the script does, and the test would be measuring
		// its own scaffolding.
		"TMPDIR="+tmp)

	out, err := cmd.CombinedOutput()

	code := 0
	if exit := (&exec.ExitError{}); errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}

	return string(out), code
}

// The ordinary answer: the sync worked and what is waiting is listed.
func TestTheUpdateCheckListsWhatIsWaiting(t *testing.T) {
	out, code := runCheckUpdates(t, t.TempDir(),
		`case "$1" in -Sy) exit 0;; -Qu) echo "linux 1 -> 2"; echo "mesa 3 -> 4";; esac`,
		`shift; exec "$@"`)

	if code != 0 {
		t.Fatalf("a working sync answered %d: %s", code, out)
	}

	if count, _ := pendingUpdates(out); count != 2 {
		t.Errorf("counted %d packages in %q, want 2", count, out)
	}
}

// What the seat says when the sync fails is now pacman's own last word rather
// than an assumption about the network. Everything from a name that will not
// resolve to a full disk used to arrive as one sentence about mirrors.
func TestAFailedSyncCarriesWhatPacmanSaid(t *testing.T) {
	out, code := runCheckUpdates(t, t.TempDir(),
		`case "$1" in -Sy) echo "error: failed retrieving file 'core.db': Could not resolve host" >&2; exit 1;; esac`,
		`shift; exec "$@"`)

	if code != 3 {
		t.Fatalf("a failed sync answered %d, want 3: %s", code, out)
	}

	problem := syncProblem(out)

	if !strings.Contains(problem, "Could not resolve host") {
		t.Errorf("the seat reports %q, which does not say what pacman said", problem)
	}
}

// What a real failed sync looks like, taken from the machine this was reported
// from: one line per file that did not arrive, and a summary underneath that
// says nothing anybody can act on. The summary is the last line, which is what
// the first version of this reported, and it left the card saying "failed to
// retrieve some files" with no mention of which file, which mirror, or why.
func TestAFailedSyncNamesTheFileAndNotTheSummary(t *testing.T) {
	out, code := runCheckUpdates(t, t.TempDir(), `
case "$1" in
-Sy)
	echo "error: failed retrieving file 'core.db' from geo.mirror.pkgbuild.com : Connection timed out after 10001 milliseconds" >&2
	echo "error: failed to synchronize all databases (failed to retrieve some files)" >&2
	exit 1
	;;
esac`, `shift; exec "$@"`)

	if code != 3 {
		t.Fatalf("a failed sync answered %d, want 3: %s", code, out)
	}

	problem := syncProblem(out)

	if !strings.Contains(problem, "Connection timed out") {
		t.Errorf("the seat reports %q, which is the summary and not the reason", problem)
	}

	if !strings.Contains(problem, "geo.mirror.pkgbuild.com") {
		t.Errorf("the seat reports %q, which does not say which mirror it was", problem)
	}
}

// The directory the script makes has to be one the download user can get into.
//
// pacman downloads as alpm since 6.1 and Arch ships DownloadUser set, so a root
// owned directory nobody else may enter stops the sync with "could not open
// file ... Permission denied" before a single byte is fetched. mktemp makes one
// of exactly that kind, which is how this arrived: the fix for two runs sharing
// a directory shipped a directory neither run could download into.
//
// The stub asks the only question that matters, which is whether somebody who
// is not root can enter and read it. pacman creates what it needs underneath
// itself.
func TestTheDatabaseDirectoryLetsTheDownloadUserIn(t *testing.T) {
	out, code := runCheckUpdates(t, t.TempDir(), `
case "$1" in
-Sy)
	mode=$(stat -c %a "$3")
	case "$mode" in
	*5|*7) : ;;
	*)
		echo "error: could not open file $3/sync/core.db.part: Permission denied" >&2
		echo "mode is $mode" >&2
		exit 1
		;;
	esac
	;;
-Qu)
	echo "linux 1 -> 2"
	;;
esac`, `shift; exec "$@"`)

	if code != 0 {
		t.Errorf("the sync could not use the directory the script made: %s", out)
	}
}

// A sync that never returns is bounded, and says so in the words of the bound
// rather than pacman's, because pacman was killed and said nothing.
func TestASyncThatHangsIsBoundedAndSaysSo(t *testing.T) {
	out, code := runCheckUpdates(t, t.TempDir(),
		`exit 0`,
		`exit 124`)

	if code != 3 {
		t.Fatalf("a bounded sync answered %d, want 3: %s", code, out)
	}

	problem := syncProblem(out)

	if !strings.Contains(problem, syncPatience) {
		t.Errorf("the seat reports %q, which does not say how long it waited", problem)
	}
}

// And when pacman fails with nothing to say, the old sentence is still the best
// there is. It was the only sentence before, which is the bug; it is the
// fallback now.
func TestASilentFailureKeepsTheOldSentence(t *testing.T) {
	out, code := runCheckUpdates(t, t.TempDir(),
		`case "$1" in -Sy) exit 1;; esac`,
		`shift; exec "$@"`)

	if code != 3 {
		t.Fatalf("a failed sync answered %d, want 3: %s", code, out)
	}

	if problem := syncProblem(out); problem != "the package mirrors could not be reached from this seat" {
		t.Errorf("a silent failure reports %q", problem)
	}
}

// Two runs at once must not touch each other's database.
//
// This is the bug the whole thing was reported for. The pass on its six hour
// timer and somebody pressing Check for updates are two callers that know
// nothing of one another, and with one fixed directory between them the second
// one's rm -rf takes the sync database out from under the first. What the seat
// then reports is that the mirrors could not be reached, which is a sentence
// about the network and was never true, and running the same command by hand
// afterwards works every time because nothing collides with it.
//
// The stub is what measures it: it writes a file into the database directory,
// waits long enough for the other run to get to its own setup, and fails if
// what it wrote is gone.
func TestTwoUpdateChecksAtOnceLeaveEachOtherAlone(t *testing.T) {
	const pacman = `
case "$1" in
-Sy)
	dbpath=$3
	# Named after this run and not "marker". The first version of this stub
	# wrote one name for both runs and passed against the very directory it was
	# written to catch: the second run deletes the first one's file and then
	# writes a file of the same name, so the check found somebody else's and
	# called it its own.
	echo mine > "$dbpath/marker.$$"
	sleep 0.4
	[ -f "$dbpath/marker.$$" ] || { echo "another run deleted this database" >&2; exit 1; }
	;;
-Qu)
	echo "linux 1 -> 2"
	;;
esac
`

	type answer struct {
		out  string
		code int
	}

	done := make(chan answer, 2)

	// One directory between them, which is what a seat really has: TMPDIR is
	// not set inside a container, so both runs of the real script land in the
	// same /tmp and only the name mktemp picks keeps them apart.
	tmp := t.TempDir()

	// Staggered, because that is the shape of it: the pass is in the middle of
	// its sync when somebody presses the button. Two runs started at the same
	// instant do their setup together and never notice each other, which is
	// what made the first version of this test pass against the fixed
	// directory it was written to catch.
	for i := range 2 {
		if i > 0 {
			time.Sleep(150 * time.Millisecond)
		}

		go func() {
			out, code := runCheckUpdates(t, tmp, pacman, `shift; exec "$@"`)
			done <- answer{out, code}
		}()
	}

	for i := range 2 {
		got := <-done

		if got.code != 0 {
			t.Errorf("run %d answered %d: %s", i+1, got.code, got.out)
		}
	}
}
