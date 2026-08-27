package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/superuser404notfound/Polyseat/internal/hostpkg"
)

// serveFile answers with the recorded release that has a package attached.
//
// The same reasoning as serve() above it: recorded from what GitHub really
// answered for v0.3.2, so that the asset parser has to find its four fields
// among the fourteen an asset really carries rather than among the four a
// fixture author would have written.
func serveWithAsset(t *testing.T, edit func(asset map[string]any)) *httptest.Server {
	t.Helper()

	raw, err := os.ReadFile("testdata/latest-with-asset.json")
	if err != nil {
		t.Fatal(err)
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}

	if edit != nil {
		assets, ok := body["assets"].([]any)
		if !ok || len(assets) == 0 {
			t.Fatal("the recording has no assets, which is what this file is for")
		}

		edit(assets[0].(map[string]any))
	}

	out, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	}))

	t.Cleanup(server.Close)

	return server
}

func TestFetchFindsThePackage(t *testing.T) {
	rel, err := fetch(t.Context(), serveWithAsset(t, nil).URL, hostpkg.Arch.Asset())
	if err != nil {
		t.Fatal(err)
	}

	if rel.Package == nil {
		t.Fatal("no package was found in a release that has one")
	}

	// The name the workflow publishes under, which carries no version: the
	// permanent download link is only permanent while the name is. Held against
	// the constant rather than against a string typed here, because the two
	// drifting apart is exactly what made this button fail on every machine for
	// five releases. See TestThePublishedNameIsTheNameThisLooksFor.
	if rel.Package.Name != hostpkg.Arch.Asset() {
		t.Errorf("name is %q", rel.Package.Name)
	}

	// A release whose asset the daemon would refuse to fetch is a release with
	// no package, for all the difference it makes on the machine.
	if err := allowed(rel.Package.URL); err != nil {
		t.Errorf("the recorded asset would not be downloaded: %v", err)
	}

	if !strings.HasPrefix(rel.Package.Digest, "sha256:") {
		t.Errorf("digest is %q, want one that says how it was made", rel.Package.Digest)
	}

	if rel.Package.Size == 0 {
		t.Error("size is zero, which no package is")
	}
}

// A release with no assets is a real answer, not a fault: every release before
// 0.3.2 is one, because the workflow that builds a package did not exist yet.
func TestFetchIsFineWithAReleaseThatHasNoPackage(t *testing.T) {
	rel, err := fetch(t.Context(), serve(t, nil).URL, hostpkg.Arch.Asset())
	if err != nil {
		t.Fatal(err)
	}

	if rel.Package != nil {
		t.Errorf("found a package in a release with none: %+v", rel.Package)
	}
}

// The debug package sits beside the real one, has the same content type and is
// not the thing to install. It is told apart by name, so the name is what this
// checks.
func TestFetchIgnoresTheDebugPackage(t *testing.T) {
	server := serveWithAsset(t, func(a map[string]any) {
		a["name"] = "polyseat-debug-0.3.2-1-x86_64.pkg.tar.zst"
	})

	rel, err := fetch(t.Context(), server.URL, hostpkg.Arch.Asset())
	if err != nil {
		t.Fatal(err)
	}

	if rel.Package != nil {
		t.Errorf("took the debug package as the one to install: %s", rel.Package.Name)
	}
}

// A file left on the release from another version must not be taken as this
// one's, because installing it would be a downgrade nobody asked for.
func TestFetchIgnoresAPackageForAnotherVersion(t *testing.T) {
	server := serveWithAsset(t, func(a map[string]any) {
		a["name"] = "polyseat-0.2.0-1-x86_64.pkg.tar.zst"
	})

	rel, err := fetch(t.Context(), server.URL, hostpkg.Arch.Asset())
	if err != nil {
		t.Fatal(err)
	}

	if rel.Package != nil {
		t.Errorf("took a package for %s as the package for v0.3.2", rel.Package.Name)
	}
}

// An asset still arriving is not one to install.
func TestFetchIgnoresAnAssetThatIsNotUploadedYet(t *testing.T) {
	server := serveWithAsset(t, func(a map[string]any) { a["state"] = "starter" })

	rel, err := fetch(t.Context(), server.URL, hostpkg.Arch.Asset())
	if err != nil {
		t.Fatal(err)
	}

	if rel.Package != nil {
		t.Error("took an asset that has not finished uploading")
	}
}

// The control that has to hold when everything else has failed: whatever the
// API answered, the daemon only ever begins a download at this project's own
// releases on github.com.
func TestAllowedRefusesAnythingButThisProjectsDownloads(t *testing.T) {
	good := "https://github.com/superuser404notfound/Polyseat/releases/download/v0.3.2/polyseat-0.3.2-1-x86_64.pkg.tar.zst"
	if err := allowed(good); err != nil {
		t.Fatalf("refused the real asset URL: %v", err)
	}

	for _, bad := range []string{
		"http://github.com/superuser404notfound/Polyseat/releases/download/v0.3.2/x.pkg.tar.zst",
		"https://evil.example/superuser404notfound/Polyseat/releases/download/v0.3.2/x.pkg.tar.zst",
		"https://github.com.evil.example/superuser404notfound/Polyseat/releases/download/v0.3.2/x.pkg.tar.zst",
		"https://github.com/someone-else/Polyseat/releases/download/v0.3.2/x.pkg.tar.zst",
		"https://github.com/superuser404notfound/Polyseat/archive/refs/heads/main.tar.gz",
		"file:///etc/passwd",
		"",
	} {
		if err := allowed(bad); err == nil {
			t.Errorf("allowed %q", bad)
		}
	}
}

func TestVerifyAcceptsWhatTheReleaseStates(t *testing.T) {
	body := []byte("a package, as far as this test is concerned")
	sum := sha256.Sum256(body)
	hexsum := hex.EncodeToString(sum[:])

	a := &Asset{Name: "p.pkg.tar.zst", Digest: "sha256:" + hexsum, Size: int64(len(body))}
	if err := verify(a, hexsum, int64(len(body))); err != nil {
		t.Fatalf("refused a file that matches: %v", err)
	}

	// GitHub states it lower case and a comparison that only works in one case
	// is one that breaks the day it changes.
	if err := verify(a, strings.ToUpper(hexsum), int64(len(body))); err != nil {
		t.Errorf("refused the same digest in upper case: %v", err)
	}
}

func TestVerifyRefusesWhatDoesNotMatch(t *testing.T) {
	body := []byte("a package, as far as this test is concerned")
	sum := sha256.Sum256(body)
	hexsum := hex.EncodeToString(sum[:])
	size := int64(len(body))

	other := sha256.Sum256([]byte("something else entirely"))

	cases := []struct {
		why   string
		asset *Asset
		sum   string
		n     int64
	}{
		{
			"a different file",
			&Asset{Digest: "sha256:" + hexsum, Size: size},
			hex.EncodeToString(other[:]), size,
		},
		{
			"no digest at all, which is the case worth refusing loudest",
			&Asset{Digest: "", Size: size},
			hexsum, size,
		},
		{
			"a digest of some other kind, which this cannot check",
			&Asset{Digest: "md5:" + hexsum, Size: size},
			hexsum, size,
		},
		{
			"a digest of the right shape and the wrong length",
			&Asset{Digest: "sha256:abcdef", Size: size},
			hexsum, size,
		},
		{
			"a length that disagrees with what was stated",
			&Asset{Digest: "sha256:" + hexsum, Size: size + 1},
			hexsum, size,
		},
	}

	for _, c := range cases {
		if err := verify(c.asset, c.sum, c.n); err == nil {
			t.Errorf("accepted %s", c.why)
		}
	}
}

// The shape guard in verify refuses nothing the digest comparison would not
// also refuse, which was found by breaking it and watching every test stay
// green. What it is actually for is the sentence: "the release states no usable
// checksum" sends somebody to look at the release, and "does not match" sends
// them to look at their network. So the sentence is what is checked, or the
// guard is a second check that measures nothing.
func TestVerifySaysWhichKindOfWrongItIs(t *testing.T) {
	body := []byte("a package, as far as this test is concerned")
	sum := sha256.Sum256(body)
	hexsum := hex.EncodeToString(sum[:])
	size := int64(len(body))

	other := sha256.Sum256([]byte("something else entirely"))

	unusable := []struct {
		why    string
		digest string
	}{
		{"no digest", ""},
		{"a digest of another kind", "md5:" + hexsum},
		{"a digest of the wrong length", "sha256:abcdef"},
	}

	for _, c := range unusable {
		err := verify(&Asset{Digest: c.digest, Size: size}, hexsum, size)
		if err == nil {
			t.Errorf("accepted %s", c.why)

			continue
		}

		if !strings.Contains(err.Error(), "no usable checksum") {
			t.Errorf("for %s the error is %q, which points at the wrong thing", c.why, err)
		}
	}

	err := verify(&Asset{Digest: "sha256:" + hexsum, Size: size},
		hex.EncodeToString(other[:]), size)
	if err == nil {
		t.Fatal("accepted a different file")
	}

	if !strings.Contains(err.Error(), "does not match") {
		t.Errorf("a mismatch says %q, which points at the wrong thing", err)
	}
}

// Apply must refuse before it reaches the network when there is nothing it
// could install, so that a broken release cannot become a pacman invocation.
func TestApplyRefusesWithoutAPackage(t *testing.T) {
	for _, c := range []struct {
		why string
		rel *Release
	}{
		{"no release at all", nil},
		{"a release with no package", &Release{Version: "v9.9.9"}},
	} {
		if err := Apply(t.Context(), c.rel, nil); err == nil {
			t.Errorf("accepted %s", c.why)
		}
	}
}
