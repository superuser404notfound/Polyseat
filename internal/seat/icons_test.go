package seat

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Everything the drivers below share: the helper under test, and a way to write
// the one file nothing else in this project can produce.
//
// appinfo.vdf is Valve's, it is binary, and the address of a title's icon is in
// it and nowhere else. A test that used a copy of a real one would be a
// megabyte and a half of somebody's library in the repository, so the shape is
// built here instead: the version with a table of key names, which is the one
// every current Steam writes.
const iconsPrelude = `
import importlib.util, json, os, struct, sys

spec = importlib.util.spec_from_file_location("icons", sys.argv[1])
icons = importlib.util.module_from_spec(spec)
spec.loader.exec_module(icons)

from PIL import Image


def build_appinfo(path, apps):
    keys = ["appinfo", "common", "clienticon", "linuxclienticon", "icon"]
    index = {name: i for i, name in enumerate(keys)}

    body = b""

    for appid, common in apps.items():
        tree = b"\x00" + struct.pack("<I", index["appinfo"])
        tree += b"\x00" + struct.pack("<I", index["common"])

        for key, value in common.items():
            tree += b"\x01" + struct.pack("<I", index[key]) + value.encode() + b"\x00"

        tree += b"\x08" * 3

        record = b"\x00" * (4 + 4 + 8 + 20 + 4 + 20) + tree
        body += struct.pack("<II", int(appid), len(record)) + record

    body += struct.pack("<I", 0)

    table = struct.pack("<I", len(keys))
    table += b"".join(name.encode() + b"\x00" for name in keys)

    head = struct.pack("<II", 0x07564429, 1) + struct.pack("<q", 16 + len(body))

    with open(path, "wb") as fh:
        fh.write(head + body + table)


def workspace(root):
    """Point the helper at a seat built for the test rather than at a real one."""
    icons.OUT = os.path.join(root, "icons")
    icons.APPINFO = os.path.join(root, "appinfo.vdf")
    icons.LIBRARY = os.path.join(root, "librarycache")
    icons.ICON_ROOTS = [os.path.join(root, "theme")]

    os.makedirs(icons.OUT, exist_ok=True)
    os.makedirs(icons.LIBRARY, exist_ok=True)
    os.makedirs(icons.ICON_ROOTS[0], exist_ok=True)
`

// The icon a title publishes is behind a hash that only appinfo.vdf carries, so
// this is the whole path: read the hash, ask for it as the .ico it is published
// as, and keep the largest picture in it.
const iconsClientDriver = iconsPrelude + `
root = sys.argv[2]
workspace(root)

build_appinfo(icons.APPINFO, {"1562430": {
    "clienticon": "581c0734616454644f2d1a7f7f47ec56cbecade8",
    "icon": "c8b352e87c28aa9ffe3a4641b230beb5064a49e6",
}})

# What the address answers with: several sizes in one file, as every one of
# these is.
source = os.path.join(root, "icon.ico")
Image.new("RGBA", (256, 256), (10, 120, 200, 255)).save(
    source, format="ICO", sizes=[(16, 16), (32, 32), (256, 256)])

calls = []


def fake(url):
    calls.append(url)

    return open(source, "rb").read()


icons.get = fake

info = icons.AppInfo({"1562430"})
first = icons.steam_icon("1562430", info, {"left": 2})

# And again, which is what the minute timer does: the file is already there, so
# nothing is asked for a second time.
again = icons.steam_icon("1562430", icons.AppInfo({"1562430"}), {"left": 2})

print(json.dumps({
    "path": first,
    "same": first == again,
    "calls": calls,
    "size": Image.open(first).size if first else None,
}))
`

func TestIconsFetchTheOneSteamsOwnShortcutWouldWear(t *testing.T) {
	python, dir := iconsHelper(t)

	out, err := exec.Command(python, "-c", iconsClientDriver,
		filepath.Join(dir, "icons.py"), dir).CombinedOutput()
	if err != nil {
		t.Fatalf("the helper failed: %v\n%s", err, out)
	}

	var result struct {
		Path  string   `json:"path"`
		Same  bool     `json:"same"`
		Calls []string `json:"calls"`
		Size  []int    `json:"size"`
	}

	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("the driver printed %q: %v", out, err)
	}

	if result.Path == "" {
		t.Fatal("no icon came back, so the game would still wear its cover in the grid")
	}

	want := "https://cdn.cloudflare.steamstatic.com/steamcommunity/public/images/apps/" +
		"1562430/581c0734616454644f2d1a7f7f47ec56cbecade8.ico"

	if len(result.Calls) != 1 || result.Calls[0] != want {
		t.Fatalf("asked for\n  %v\nwant one request for\n  %s", result.Calls, want)
	}

	// The largest picture in the file, because this is drawn at ninety six
	// pixels and at double that on a 4K screen.
	if len(result.Size) != 2 || result.Size[0] != 256 {
		t.Errorf("kept a %v icon, want the 256 pixel one out of the file", result.Size)
	}

	if !result.Same {
		t.Error("the second pass produced a different file, so the entry would be rewritten every minute")
	}
}

// A game somebody has already asked Steam for a shortcut for has the right icon
// in the seat's icon theme, put there by Steam. Nothing may be fetched for it,
// and it is the file to use: it is the same picture, and using it is what makes
// our entries look like Steam's own.
const iconsThemeDriver = iconsPrelude + `
root = sys.argv[2]
workspace(root)

build_appinfo(icons.APPINFO, {"1562430": {"clienticon": "0123456789abcdef"}})

shortcut = os.path.join(icons.ICON_ROOTS[0], "hicolor/256x256/apps")
os.makedirs(shortcut)

path = os.path.join(shortcut, "steam_icon_1562430.png")
Image.new("RGBA", (256, 256), (200, 30, 30, 255)).save(path)

calls = []


def fake(url):
    calls.append(url)

    return None


icons.get = fake

got = icons.steam_icon("1562430", icons.AppInfo({"1562430"}), {"left": 2})

print(json.dumps({"path": got, "want": path, "calls": calls}))
`

func TestIconsUseWhatSteamAlreadyPutInTheTheme(t *testing.T) {
	python, dir := iconsHelper(t)

	out, err := exec.Command(python, "-c", iconsThemeDriver,
		filepath.Join(dir, "icons.py"), dir).CombinedOutput()
	if err != nil {
		t.Fatalf("the helper failed: %v\n%s", err, out)
	}

	var result struct {
		Path  string   `json:"path"`
		Want  string   `json:"want"`
		Calls []string `json:"calls"`
	}

	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("the driver printed %q: %v", out, err)
	}

	if result.Path != result.Want {
		t.Errorf("used %q, want the icon Steam wrote at %q", result.Path, result.Want)
	}

	if len(result.Calls) != 0 {
		t.Errorf("fetched %v for an icon that was already in the seat", result.Calls)
	}
}

// A seat with no network, or one that has spent this pass's budget, still has
// the small icon Steam downloaded for its own library list. Thirty two pixels
// is a poor thing to draw at ninety six and it is still the game's own icon,
// which is the point: the grid stays a grid of icons.
const iconsCachedDriver = iconsPrelude + `
root = sys.argv[2]
workspace(root)

build_appinfo(icons.APPINFO, {"1562430": {
    "clienticon": "581c0734616454644f2d1a7f7f47ec56cbecade8",
    "icon": "c8b352e87c28aa9ffe3a4641b230beb5064a49e6",
}})

cached = os.path.join(icons.LIBRARY, "1562430")
os.makedirs(cached)

Image.new("RGB", (32, 32), (30, 200, 90)).save(
    os.path.join(cached, "c8b352e87c28aa9ffe3a4641b230beb5064a49e6.jpg"))

calls = []


def fake(url):
    calls.append(url)

    return None


icons.get = fake

# No budget left, which is what a seat that has just been handed forty games
# looks like on its first pass.
got = icons.steam_icon("1562430", icons.AppInfo({"1562430"}), {"left": 0})

print(json.dumps({
    "path": got,
    "calls": calls,
    "size": Image.open(got).size if got else None,
    "inside": bool(got) and got.startswith(icons.OUT),
}))
`

func TestIconsFallBackToWhatSteamCachedForItself(t *testing.T) {
	python, dir := iconsHelper(t)

	out, err := exec.Command(python, "-c", iconsCachedDriver,
		filepath.Join(dir, "icons.py"), dir).CombinedOutput()
	if err != nil {
		t.Fatalf("the helper failed: %v\n%s", err, out)
	}

	var result struct {
		Path   string   `json:"path"`
		Calls  []string `json:"calls"`
		Size   []int    `json:"size"`
		Inside bool     `json:"inside"`
	}

	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("the driver printed %q: %v", out, err)
	}

	if result.Path == "" {
		t.Fatal("nothing came back, so a seat with no network shows covers in the grid")
	}

	if len(result.Calls) != 0 {
		t.Errorf("made %v with no budget left to make it with", result.Calls)
	}

	// A PNG of ours rather than Steam's JPEG: the launcher reads what GTK can
	// draw, and the sweep only ever removes files this wrote.
	if !result.Inside || !strings.HasSuffix(result.Path, ".png") {
		t.Errorf("kept the icon at %q, want a PNG in the helper's own directory", result.Path)
	}

	if len(result.Size) != 2 || result.Size[0] != 32 {
		t.Errorf("the cached icon came out %v, want the 32 pixels it really is", result.Size)
	}
}

// iconsHelper puts the real helper somewhere a driver can import it.
func iconsHelper(t *testing.T) (string, string) {
	t.Helper()

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("SKIPPED: no python3 to run the helper with, so its behaviour is unverified here")
	}

	if err := exec.Command(python, "-c", "import PIL").Run(); err != nil {
		t.Skip("SKIPPED: no Pillow here, which the helper draws with, so it is unverified")
	}

	script, err := assets.ReadFile("assets/icons.py")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "icons.py"), script, 0o644); err != nil {
		t.Fatal(err)
	}

	return python, dir
}
