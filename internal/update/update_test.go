package update

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/superuser404notfound/Polyseat/internal/hostpkg"
)

// discard keeps the test output to the failures.
func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// serve answers like GitHub does, from a recording of what GitHub really
// answered for this project's own release.
//
// Recorded rather than written by hand on purpose. A handwritten fixture agrees
// with whatever the parser expects, which is exactly the mistake it is supposed
// to catch: this file is 4 KB of fields nobody here thought about, and the
// parser has to find its four in among them.
func serve(t *testing.T, edit func(map[string]any)) *httptest.Server {
	t.Helper()

	raw, err := os.ReadFile("testdata/latest.json")
	if err != nil {
		t.Fatal(err)
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}

	if edit != nil {
		edit(body)
	}

	out, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept header is %q", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	}))

	t.Cleanup(server.Close)

	return server
}

func TestFetchReadsARecordedRelease(t *testing.T) {
	server := serve(t, nil)

	release, err := fetch(context.Background(), server.URL, hostpkg.Arch.Asset())
	if err != nil {
		t.Fatal(err)
	}

	if release.Version != "v0.1.0" {
		t.Errorf("version is %q, want v0.1.0", release.Version)
	}

	if release.URL != "https://github.com/superuser404notfound/Polyseat/releases/tag/v0.1.0" {
		t.Errorf("url is %q", release.URL)
	}

	if release.Published.IsZero() {
		t.Error("published_at did not survive")
	}
}

// A prerelease is not something to be told about. releases/latest is documented
// not to return one, so this covers the day that changes rather than today.
func TestFetchRefusesAPrerelease(t *testing.T) {
	for _, field := range []string{"prerelease", "draft"} {
		server := serve(t, func(body map[string]any) { body[field] = true })

		if _, err := fetch(context.Background(), server.URL, hostpkg.Arch.Asset()); err == nil {
			t.Errorf("%s=true was accepted", field)
		}
	}
}

func TestFetchRefusesAReleaseWithoutATag(t *testing.T) {
	server := serve(t, func(body map[string]any) { body["tag_name"] = "" })

	if _, err := fetch(context.Background(), server.URL, hostpkg.Arch.Asset()); err == nil {
		t.Error("a release with no tag was accepted")
	}
}

func TestFetchRefusesAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	if _, err := fetch(context.Background(), server.URL, hostpkg.Arch.Asset()); err == nil {
		t.Error("404 was accepted")
	}
}

// The whole point of the package: it says something only when there is
// something to say, and running the current release is the case that has to
// stay quiet, because it is everybody's case almost all of the time.
func TestCheckSpeaksOnlyAboutSomethingNewer(t *testing.T) {
	server := serve(t, nil) // publishes v0.1.0

	for _, tc := range []struct {
		running string
		want    string
	}{
		{"v0.0.9", "v0.1.0"},
		{"v0.1.0", ""},
		{"v0.2.0", ""},
		{"dev", ""},
		{"v0.1.0-4-gabc1234-dirty", ""},
	} {
		checker := New(tc.running, true, discard())
		checker.api = server.URL
		checker.check(context.Background())

		got := ""
		if available := checker.Available(); available != nil {
			got = available.Version
		}

		if got != tc.want {
			t.Errorf("running %s: reported %q, want %q", tc.running, got, tc.want)
		}
	}
}

// A release that is withdrawn, or a daemon that was updated underneath itself,
// has to clear what it said before. Kept because the first version of check
// only ever assigned when there was something newer, so a banner once shown
// stayed until the daemon was restarted.
func TestCheckForgetsWhenThereIsNothingLeftToSay(t *testing.T) {
	server := serve(t, nil)

	checker := New("v0.0.1", true, discard())
	checker.api = server.URL
	checker.check(context.Background())

	if checker.Available() == nil {
		t.Fatal("v0.1.0 was not reported to a v0.0.1 build")
	}

	checker.current = "v0.1.0"
	checker.check(context.Background())

	if got := checker.Available(); got != nil {
		t.Errorf("still reporting %v after catching up", got)
	}
}

func TestParseTag(t *testing.T) {
	for _, tc := range []struct {
		tag  string
		want bool
	}{
		{"v0.1.0", true},
		{"0.1.0", true},
		{"v10.20.30", true},

		// Everything git describe answers with when the build is not sitting
		// exactly on a tag, plus what an unstamped build says.
		{"v0.1.0-dirty", false},
		{"v0.1.0-4-gabc1234", false},
		{"v0.1.0-4-gabc1234-dirty", false},
		{"50f439c", false},
		{"dev", false},
		{"unknown", false},
		{"", false},

		{"v1.2", false},
		{"v1.2.3.4", false},
		{"v1.2.x", false},
		{"v0.1.-2", false},
		{"v0.1.+2", false},
		{"v 1.2.3", false},
	} {
		if _, ok := parseTag(tc.tag); ok != tc.want {
			t.Errorf("parseTag(%q) = %v, want %v", tc.tag, ok, tc.want)
		}
	}
}

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		running, published string
		want               bool
	}{
		{"v0.1.0", "v0.2.0", true},
		{"v0.1.0", "v0.1.1", true},
		{"v0.9.9", "v1.0.0", true},

		{"v0.1.0", "v0.1.0", false},
		{"v0.2.0", "v0.1.0", false},
		{"v1.0.0", "v0.9.9", false},

		// The numbers are numbers and not text. Sorting these as strings puts
		// v0.10.0 before v0.9.0, which would leave a machine one version behind
		// and quietly certain it was up to date.
		{"v0.9.0", "v0.10.0", true},
		{"v0.10.0", "v0.9.0", false},

		// Neither side gets the benefit of the doubt.
		{"dev", "v9.9.9", false},
		{"v0.1.0", "nightly", false},
	} {
		if got := newer(tc.running, tc.published); got != tc.want {
			t.Errorf("newer(%q, %q) = %v, want %v", tc.running, tc.published, got, tc.want)
		}
	}
}

// The button in the interface, which is the same request the loop makes with
// one difference: somebody is waiting for this one, so what goes wrong is
// returned rather than logged and dropped.
func TestCheckNowAnswersStraightAway(t *testing.T) {
	server := serve(t, nil) // publishes v0.1.0

	checker := New("v0.0.9", true, discard())
	checker.api = server.URL

	release, err := checker.CheckNow(context.Background())
	if err != nil {
		t.Fatalf("refused: %v", err)
	}

	if release == nil || release.Version != "v0.1.0" {
		t.Fatalf("answered with %v", release)
	}

	if checker.LastCheck().IsZero() {
		t.Error("it did not record having asked")
	}
}

// Nothing newer is an answer and not a failure, and the page says so out loud.
// A button that reports nothing when it worked is one nobody presses twice.
func TestCheckNowSaysNothingNewerWithoutAnError(t *testing.T) {
	server := serve(t, nil)

	checker := New("v0.1.0", true, discard())
	checker.api = server.URL

	release, err := checker.CheckNow(context.Background())
	if err != nil {
		t.Fatalf("refused: %v", err)
	}

	if release != nil {
		t.Errorf("offered %v to the version that publishes it", release)
	}

	if checker.LastCheck().IsZero() {
		t.Error("it did not record having asked")
	}
}

// The difference from the background check, and the reason this exists at all:
// somebody pressed a button and is owed an answer, including "GitHub did not
// answer". The loop logs that and moves on, because nobody is watching it.
func TestCheckNowReportsWhatWentWrong(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	checker := New("v0.0.9", true, discard())
	checker.api = server.URL

	if _, err := checker.CheckNow(context.Background()); err == nil {
		t.Fatal("a server that answered 500 was reported as a successful check")
	}

	if !checker.LastCheck().IsZero() {
		t.Error("a failed check was recorded as having asked")
	}
}

// Two refusals that would otherwise arrive as "nothing newer", which is the one
// answer they do not mean.
func TestCheckNowRefusesRatherThanShrugging(t *testing.T) {
	server := serve(t, nil)

	off := New("v0.0.9", false, discard())
	off.api = server.URL

	if _, err := off.CheckNow(context.Background()); err == nil {
		t.Error("a checker with the setting off answered as though it had asked")
	}

	development := New("v0.1.0-4-gabc1234-dirty", true, discard())
	development.api = server.URL

	if _, err := development.CheckNow(context.Background()); err == nil {
		t.Error("a build that cannot name itself as a release was compared with one")
	}
}

// The bug this package did not catch for five releases, and the test that would
// have.
//
// The interface's install button never worked once, on any machine. It said
// "that release has no package attached to it", because the workflow publishes
// the package under a version-less name — releases/latest/download/ is a
// permanent link only while the name is — and the matcher here was written one
// release earlier, against makepkg's own versioned name.
//
// The tests that existed did not notice, because the recording they used was
// made before that rename: they agreed with the parser about a shape that had
// stopped existing. A recording proves what GitHub said once, and only reading
// the workflow proves what it says now. The daemon looks for a name, a workflow
// in another language uploads one, and nothing in between made them agree until
// this.
func TestThePublishedNamesAreTheNamesThisLooksFor(t *testing.T) {
	workflow, err := os.ReadFile("../../.github/workflows/package.yml")
	if err != nil {
		t.Fatal(err)
	}

	// Three assets and two jobs now: makepkg builds the Arch one under a
	// versioned name and the workflow copies it to the published one, and nfpm
	// writes the other two under the published names directly. So this looks
	// for each name anywhere in the file rather than for one assignment, which
	// is weaker than the test it replaces and is as strong as one test can be
	// across two jobs that name their files differently.
	//
	// What it still catches is the failure that actually happened: a rename on
	// one side and not the other. That made the update button do nothing at all
	// on every machine for five releases, and the interface reported it as a
	// fact about the release rather than as a fault here.
	for _, name := range PublishedAssets {
		if name == "" {
			t.Error("a family published an empty asset name, so it would match every release and install none")

			continue
		}

		if !bytes.Contains(workflow, []byte(name)) {
			t.Errorf("the daemon looks for %q and package.yml never mentions it, so a host of that family would be told the release has no package", name)
		}
	}
}

func TestEveryFamilyHasItsOwnAssetName(t *testing.T) {
	// Two families sharing a name would mean one of them installing the other's
	// package, which fails in a way that reads like a corrupt download rather
	// than like a packaging mistake.
	seen := map[string]bool{}

	for _, name := range PublishedAssets {
		if seen[name] {
			t.Errorf("%q is published for more than one package manager", name)
		}

		seen[name] = true
	}
}
