package update

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
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

	release, err := fetch(context.Background(), server.URL)
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

		if _, err := fetch(context.Background(), server.URL); err == nil {
			t.Errorf("%s=true was accepted", field)
		}
	}
}

func TestFetchRefusesAReleaseWithoutATag(t *testing.T) {
	server := serve(t, func(body map[string]any) { body["tag_name"] = "" })

	if _, err := fetch(context.Background(), server.URL); err == nil {
		t.Error("a release with no tag was accepted")
	}
}

func TestFetchRefusesAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	if _, err := fetch(context.Background(), server.URL); err == nil {
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
