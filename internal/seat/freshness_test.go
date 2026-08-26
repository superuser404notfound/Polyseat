package seat

import (
	"context"
	"errors"
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

// A seat that is switched off cannot be asked, and that is an ordinary answer
// rather than a fault: switched off is the normal state of a seat nobody is
// playing in, and a card that shows an error for it teaches people to ignore
// the place errors appear.
func TestASwitchedOffSeatSaysSoRatherThanFailing(t *testing.T) {
	m := &Manager{rt: map[string]*runtime{}, sunshine: &sunshineCache{}}

	m.runtimeOf("seat1").state = StateStopped

	f := m.Freshness(context.Background(), "seat1")

	if f.Problem == "" {
		t.Error("a stopped seat gave no reason for having no answer")
	}

	if f.Behind() {
		t.Error("a seat that could not be asked is reported as behind")
	}

	if f.Checked.IsZero() {
		t.Error("the seat was asked, so the time it was asked should be set")
	}
}
