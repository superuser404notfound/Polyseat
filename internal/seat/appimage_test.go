package seat

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// header is the first bytes of a real AppImage, read off one with od rather than
// copied out of a specification: 7f 45 4c 46 02 01 01 00 41 49 02, an ELF header
// with AI and the type where the format puts them.
var header = []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 'A', 'I', 2}

// TestSafeNameAgreesWithTheScan is the one that matters for removal working.
//
// The rule exists twice, in Go for what the daemon downloads and in Python for
// what the seat adopts, and they run on different machines. If they disagree the
// interface lists a file under one name and deletes nothing under the other, or
// the scan writes a name the daemon's own validation then refuses, which is a
// row with a Remove button that answers 400 for ever.
func TestSafeNameAgreesWithTheScan(t *testing.T) {
	names := []string{
		"Ryujinx-1.2.78-x64.AppImage",
		"citron nightly.AppImage",
		"Sudachi-v1.0.9-linux_amd64.AppImage",
		"../../etc/passwd",
		"-rf",
		".hidden.AppImage",
		"",
		"no-extension",
		"UPPER.APPIMAGE",
		"quote'and\"space and;semicolon.AppImage",
		"Ünïcödé-Emulator.AppImage",
		strings.Repeat("long", 60) + ".AppImage",
	}

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("SKIPPED: no python3, so the two halves of the rule are unchecked here")
	}

	query, err := json.Marshal(names)
	if err != nil {
		t.Fatal(err)
	}

	driver := `
import json, sys

source = open(sys.argv[1], encoding="utf-8").read()
scope = {"__name__": "polyseat_scan_under_test"}
exec(compile(source, "scan", "exec"), scope)

print(json.dumps([scope["safe_name"](n) for n in json.loads(sys.argv[2])]))
`

	script := filepath.Join(t.TempDir(), "scan.py")
	if err := os.WriteFile(script, []byte(appImageScan), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(python, "-c", driver, script, string(query))
	cmd.Env = append(os.Environ(), "POLYSEAT_HOME="+t.TempDir())

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the scan could not be loaded: %v\n%s", err, out)
	}

	var theirs []string
	if err := json.Unmarshal(out, &theirs); err != nil {
		t.Fatalf("the driver printed something unreadable: %v\n%s", err, out)
	}

	if len(theirs) != len(names) {
		t.Fatalf("got %d names back, want %d", len(theirs), len(names))
	}

	for i, name := range names {
		mine := safeAppImageName(name)

		if mine != theirs[i] {
			t.Errorf("%q: the daemon says %q and the seat says %q", name, mine, theirs[i])
		}

		// Whatever either of them produces has to be a name the daemon will
		// accept back from the interface, or the file cannot be removed.
		if err := ValidateAppImageFile(theirs[i]); err != nil {
			t.Errorf("the scan would produce %q, which the daemon then rejects: %v", theirs[i], err)
		}
	}
}

// runAppImageScan runs the embedded scan against a home directory built for the
// test, the same way the seat runs it.
func runAppImageScan(t *testing.T, home string) []map[string]any {
	t.Helper()

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("SKIPPED: no python3 to run the scan with, so its behaviour is unverified here")
	}

	cmd := exec.Command(python, "-c", appImageScan)
	cmd.Env = append(os.Environ(), "POLYSEAT_HOME="+home)

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the scan failed: %v", err)
	}

	var found []map[string]any
	if err := json.Unmarshal(out, &found); err != nil {
		t.Fatalf("the scan printed something that is not a list: %v\n%s", err, out)
	}

	return found
}

// drop writes a file into a seat's home and dates it, since the scan waits for a
// download to stop moving.
func drop(t *testing.T, dir, name string, body []byte, age time.Duration) string {
	t.Helper()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, name)

	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}

	return path
}

// The point of the whole seat side: somebody downloads an emulator with Firefox
// inside their seat and it becomes something Moonlight offers, without them
// being told where the file has to go.
func TestScanAdoptsADownloadAndListsIt(t *testing.T) {
	home := t.TempDir()

	drop(t, filepath.Join(home, "Downloads"), "Citron nightly.AppImage",
		append(header, []byte("not really an emulator")...), time.Minute)

	found := runAppImageScan(t, home)

	if len(found) != 1 {
		t.Fatalf("the scan found %d AppImages, want 1: %+v", len(found), found)
	}

	if got := found[0]["file"]; got != "Citron-nightly.AppImage" {
		t.Errorf("adopted as %q, want the name with the space taken out", got)
	}

	// The name it will wear in Moonlight. Reading it out of the file means
	// running the file, which cannot work for a stub, so this is the fallback
	// and the fallback has to be a name rather than a file name.
	if got := found[0]["name"]; got != "Citron-nightly" {
		t.Errorf("named %q, want the file name without its extension", got)
	}

	moved := filepath.Join(home, "Applications", "Citron-nightly.AppImage")

	info, err := os.Stat(moved)
	if err != nil {
		t.Fatalf("the download was not moved into Applications: %v", err)
	}

	if info.Mode()&0o100 == 0 {
		t.Error("the AppImage was not made executable, so nothing can start it")
	}

	if _, err := os.Stat(filepath.Join(home, "Downloads", "Citron nightly.AppImage")); err == nil {
		t.Error("the download is still in Downloads as well, so it would be adopted again")
	}
}

// The scan runs a file to read its name and icon out of it. Anything without the
// AppImage marker is therefore not run and not adopted, because a shell script
// somebody renamed is otherwise a program the daemon starts once a minute.
//
// Written so that failing means the payload ran: the marker file is what a
// missing magic check produces.
func TestScanDoesNotRunWhatIsNotAnAppImage(t *testing.T) {
	home := t.TempDir()
	marker := filepath.Join(home, "it-ran")

	drop(t, filepath.Join(home, "Downloads"), "trap.AppImage",
		[]byte("#!/bin/sh\ntouch "+marker+"\n"), time.Minute)

	// And in the directory the scan lists as well, not only in the one it
	// adopts from: the two are separate paths to the same exec.
	drop(t, filepath.Join(home, "Applications"), "trap2.AppImage",
		[]byte("#!/bin/sh\ntouch "+marker+"\n"), time.Minute)

	found := runAppImageScan(t, home)

	if len(found) != 0 {
		t.Errorf("the scan offered %d files that are not AppImages: %+v", len(found), found)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the scan executed a file that is not an AppImage")
	}

	if _, err := os.Stat(filepath.Join(home, "Applications", "trap.AppImage")); err == nil {
		t.Error("a file that is not an AppImage was adopted into Applications")
	}
}

// A browser writes into ~/Downloads while it is downloading. Adopting a file
// that is still being written moves it out from under whatever is writing it.
func TestScanLeavesAFreshFileAlone(t *testing.T) {
	home := t.TempDir()

	drop(t, filepath.Join(home, "Downloads"), "Half.AppImage", header, 0)

	if found := runAppImageScan(t, home); len(found) != 0 {
		t.Errorf("a file written a moment ago was adopted: %+v", found)
	}

	if _, err := os.Stat(filepath.Join(home, "Downloads", "Half.AppImage")); err != nil {
		t.Errorf("the fresh download was moved anyway: %v", err)
	}
}

// A newer build of the same emulator has the same name, and two files differing
// by a suffix would be two entries in Moonlight, one of them stale.
func TestScanReplacesAnOlderBuildOfTheSameName(t *testing.T) {
	home := t.TempDir()

	drop(t, filepath.Join(home, "Applications"), "Emu.AppImage", header, time.Hour)
	drop(t, filepath.Join(home, "Downloads"), "Emu.AppImage",
		append(header, []byte("newer")...), time.Minute)

	found := runAppImageScan(t, home)

	if len(found) != 1 {
		t.Fatalf("the scan found %d AppImages, want 1: %+v", len(found), found)
	}

	body, err := os.ReadFile(filepath.Join(home, "Applications", "Emu.AppImage"))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasSuffix(string(body), "newer") {
		t.Error("the older build survived, so the seat would go on offering it")
	}
}

// Real output, captured from curl in a seat with no terminal at all, which is
// how the daemon runs it. The bar is redrawn over one line with carriage
// returns, so what arrives is a stream of overwrites rather than lines.
const curlProgress = "\r############                              17.5%" +
	"\r#################################         46.7%" +
	"\r######################################### 100.0%"

func TestLastPercentReadsCurlsBar(t *testing.T) {
	value, ok := lastPercent(curlProgress)

	if !ok {
		t.Fatal("no figure was read out of curl's progress bar")
	}

	if value != 100 {
		t.Errorf("read %d%%, want the newest figure in the chunk", value)
	}

	if _, ok := lastPercent("no figures here at all"); ok {
		t.Error("something was read out of output with no percentage in it")
	}

	// The bar is drawn with hashes, and a chunk can arrive split anywhere.
	if value, ok := lastPercent("###   17.5%\r####  2"); !ok || value != 17 {
		t.Errorf("read %d, %v from a chunk cut mid figure, want 17", value, ok)
	}
}

func TestValidateAppImageURL(t *testing.T) {
	cases := []struct {
		address string
		file    string
	}{
		{"https://github.com/x/y/releases/download/v1/Ryujinx-1.2.78-x64.AppImage",
			"Ryujinx-1.2.78-x64.AppImage"},
		{"https://example.com/downloads/Citron.AppImage?token=abc", "Citron.AppImage"},
		{"https://example.com/emulator", "emulator.AppImage"},
		{"https://example.com/", "download.AppImage"},
	}

	for _, c := range cases {
		file, err := ValidateAppImageURL(c.address)
		if err != nil {
			t.Errorf("%s was refused: %v", c.address, err)

			continue
		}

		if file != c.file {
			t.Errorf("%s would be saved as %q, want %q", c.address, file, c.file)
		}
	}

	// http is refused rather than upgraded. What arrives is executed as the
	// player, so a download anybody on the way can rewrite is not the same
	// proposition as a flatpak out of a signed repository.
	for _, bad := range []string{
		"http://example.com/Thing.AppImage",
		"file:///etc/passwd",
		"ftp://example.com/Thing.AppImage",
		"",
		"   ",
		"https://",
	} {
		if _, err := ValidateAppImageURL(bad); err == nil {
			t.Errorf("%q was accepted as a download address", bad)
		}
	}
}

// The file name arrives in a URL path and leaves as part of an rm command.
func TestValidateAppImageFileRefusesTheAwkwardOnes(t *testing.T) {
	for _, bad := range []string{
		"", "..", "../../etc/passwd", "a/b.AppImage", "-rf", ".hidden",
		"with space.AppImage", "semi;colon.AppImage", "quote'.AppImage",
		strings.Repeat("x", 200),
	} {
		if err := ValidateAppImageFile(bad); err == nil {
			t.Errorf("%q was accepted as the name of an AppImage", bad)
		}
	}

	for _, good := range []string{
		"Ryujinx-1.2.78-x64.AppImage", "Citron.AppImage", "emu_v2+1.AppImage",
	} {
		if err := ValidateAppImageFile(good); err != nil {
			t.Errorf("%q was refused: %v", good, err)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:             "",
		-1:            "",
		12_300:        "12.3 kB",
		412_300_000:   "412.3 MB",
		2_100_000_000: "2.1 GB",
	}

	for size, want := range cases {
		if got := humanBytes(size); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", size, got, want)
		}
	}
}
