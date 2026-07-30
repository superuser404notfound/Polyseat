package seat

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The driver runs the real helper with its one network call replaced, so what
// is under test is the address it builds and the asset it picks, not Valve's
// availability. The recorded answer beside it is a real one, trimmed.
const boxartDriver = `
import importlib.util, json, sys

spec = importlib.util.spec_from_file_location("boxart", sys.argv[1])
boxart = importlib.util.module_from_spec(spec)
spec.loader.exec_module(boxart)

answer = open(sys.argv[2], "rb").read()
calls = []


def fake(url):
    calls.append(url)

    if "GetItems" in url:
        return answer

    return b"the picture itself"


boxart.get = fake

data = boxart.hashed_cover("3751950")

print(json.dumps({"got": data.decode() if data else None, "calls": calls}))
`

// A title whose portrait cover is published only under a hashed directory. The
// plain address answers 404 for every filename, which is how Assassin's Creed
// Black Flag Resynced came out as a card with a name on it while the picture
// sat one request away.
func TestBoxartFindsACoverPublishedUnderAHash(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("SKIPPED: no python3 to run the helper with, so its behaviour is unverified here")
	}

	script, err := assets.ReadFile("assets/boxart.py")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()

	path := filepath.Join(dir, "boxart.py")
	if err := os.WriteFile(path, script, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(python, "-c", boxartDriver, path,
		"testdata/storeitems-3751950.json").CombinedOutput()
	if err != nil {
		t.Fatalf("the helper failed: %v\n%s", err, out)
	}

	var result struct {
		Got   string   `json:"got"`
		Calls []string `json:"calls"`
	}

	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("the driver printed %q: %v", out, err)
	}

	if result.Got != "the picture itself" {
		t.Errorf("no cover came back, so the title would still have no card")
	}

	if len(result.Calls) != 2 {
		t.Fatalf("made %d requests, want the lookup and then the picture: %v",
			len(result.Calls), result.Calls)
	}

	if !strings.Contains(result.Calls[0], "GetItems") {
		t.Errorf("the first request was not the lookup: %s", result.Calls[0])
	}

	// The hash is the whole point: it is the one Steam itself had cached in a
	// seat where the picture did show, and nothing in a manifest contains it.
	want := "https://shared.cloudflare.steamstatic.com/store_item_assets/steam/apps/3751950/" +
		"36a1644b03afce1a648ab90b232196609e827539/library_capsule_2x.jpg?t=1783617053"

	if result.Calls[1] != want {
		t.Errorf("fetched\n  %s\nwant\n  %s", result.Calls[1], want)
	}
}

// And a title the service knows nothing about must not become a card with an
// error page on it. Returning nothing is the right answer: the caller then
// remembers the miss and draws the name instead.
func TestBoxartGivesUpQuietlyWhenThereIsNoAnswer(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("SKIPPED: no python3 to run the helper with, so its behaviour is unverified here")
	}

	script, err := assets.ReadFile("assets/boxart.py")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()

	path := filepath.Join(dir, "boxart.py")
	if err := os.WriteFile(path, script, 0o644); err != nil {
		t.Fatal(err)
	}

	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`{"response":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(python, "-c", boxartDriver, path, empty).CombinedOutput()
	if err != nil {
		t.Fatalf("the helper failed on an answer it does not understand: %v\n%s", err, out)
	}

	var result struct {
		Got   string   `json:"got"`
		Calls []string `json:"calls"`
	}

	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("the driver printed %q: %v", out, err)
	}

	if result.Got != "" {
		t.Errorf("something came back from an answer with no assets in it: %q", result.Got)
	}

	if len(result.Calls) != 1 {
		t.Errorf("made %d requests, want only the lookup: %v", len(result.Calls), result.Calls)
	}
}

// A card has to change its name when the picture behind it changes, or nothing
// downstream can tell that it did.
//
// This is the sequence that left a game wearing the Steam logo on a client with
// the right cover sitting on disk: artwork arrives late, the card was rewritten
// under the same name, the app list Sunshine serves was therefore unchanged, so
// it was never told to reload and the client kept what it had cached.
const boxartRedrawDriver = `
import importlib.util, json, os, sys, time

spec = importlib.util.spec_from_file_location("boxart", sys.argv[1])
boxart = importlib.util.module_from_spec(spec)
spec.loader.exec_module(boxart)

boxart.OUT = sys.argv[2]
os.makedirs(boxart.OUT, exist_ok=True)

from PIL import Image

source = os.path.join(sys.argv[3], "artwork.png")

# What a title looks like before anything has cached its cover: a square
# launcher icon, which the helper draws onto a card with the name under it.
Image.new("RGB", (64, 64), (200, 30, 30)).save(source)

item = {"key": "A Game", "label": "A Game", "source": source}
first = boxart.build(item, {"left": 0})

# And the real cover arriving later, in the same place, which is what Steam
# does the first time somebody opens their library.
Image.new("RGB", (600, 900), (30, 140, 60)).save(source)
later = time.time() + 30
os.utime(source, (later, later))

second = boxart.build(item, {"left": 0})

boxart.sweep({second})

print(json.dumps({
    "first": first,
    "second": second,
    "first_survived": os.path.exists(first),
    "left": sorted(os.listdir(boxart.OUT)),
}))
`

func TestBoxartRenamesACardWhenTheArtworkChanges(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("SKIPPED: no python3 to run the helper with, so its behaviour is unverified here")
	}

	script, err := assets.ReadFile("assets/boxart.py")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()

	path := filepath.Join(dir, "boxart.py")
	if err := os.WriteFile(path, script, 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "art")
	sources := filepath.Join(dir, "sources")

	if err := os.MkdirAll(sources, 0o755); err != nil {
		t.Fatal(err)
	}

	raw, err := exec.Command(python, "-c", boxartRedrawDriver, path, out, sources).CombinedOutput()
	if err != nil {
		t.Skipf("SKIPPED: the helper could not run here, most likely no Pillow: %v\n%s", err, raw)
	}

	var result struct {
		First         string   `json:"first"`
		Second        string   `json:"second"`
		FirstSurvived bool     `json:"first_survived"`
		Left          []string `json:"left"`
	}

	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("the driver printed %q: %v", raw, err)
	}

	if result.First == "" || result.Second == "" {
		t.Fatalf("a card was not drawn at all: %+v", result)
	}

	if result.First == result.Second {
		t.Errorf("both cards are %s, so the app list would look unchanged and the "+
			"client would keep showing the old picture", result.First)
	}

	// And the one nothing points at any more is gone, or a seat accumulates one
	// card per title per change for as long as it exists.
	if result.FirstSurvived {
		t.Errorf("the replaced card is still there: %v", result.Left)
	}

	if len(result.Left) != 1 {
		t.Errorf("%d cards left, want only the current one: %v", len(result.Left), result.Left)
	}
}
