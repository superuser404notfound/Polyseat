package report

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/superuser404notfound/Polyseat/internal/config"
)

// scratch is a configuration pointed entirely at directories this test owns, so
// that running the suite never reads or writes the real installation.
func scratch(t *testing.T) config.Config {
	t.Helper()

	cfg := config.Default()
	cfg.StateDir = filepath.Join(t.TempDir(), "state")
	cfg.LibraryDir = filepath.Join(t.TempDir(), "library")

	return cfg
}

func generate(t *testing.T, cfg config.Config) string {
	t.Helper()

	var buf bytes.Buffer

	Write(&buf, cfg, "v9.9.9", time.Date(2026, 7, 31, 22, 0, 0, 0, time.UTC))

	return buf.String()
}

// The report is one thing somebody pastes into an issue, so every section has
// to be in it even on a machine where most of them have nothing to say. A
// report missing a heading reads as a machine missing the thing.
func TestWriteDescribesEverySection(t *testing.T) {
	text := generate(t, scratch(t))

	for _, want := range []string{
		"# Polyseat report",
		"## Polyseat", "## Machine", "## Graphics", "## Incus",
		"## Storage", "## Network", "## Configuration", "## Seats", "## Journal",
		"v9.9.9",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the report has no %q in it", want)
		}
	}

	// The warning is the reason it is safe to tell people to paste this.
	if !strings.Contains(text, "Read this before pasting it anywhere") {
		t.Error("the report does not say what is in it")
	}
}

// A report describes a machine. It does not build one.
//
// This is a regression: the seats section used to call OpenStore, which creates
// the directory it lists, so asking a fresh installation to describe itself
// created part of the installation. Without root it failed with "mkdir:
// permission denied", which reads as though describing a machine required
// writing to it.
func TestWriteCreatesNothing(t *testing.T) {
	cfg := scratch(t)

	for _, dir := range []string{cfg.StateDir, cfg.LibraryDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	text := generate(t, cfg)

	// The library directory is the harder half: the reflink probe deliberately
	// writes a block into it and clones that block, so this also holds the
	// probe to cleaning up after itself.
	for _, dir := range []string{cfg.StateDir, cfg.LibraryDir} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}

		if len(entries) != 0 {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}

			t.Errorf("%s is not empty after a report: %s", dir, strings.Join(names, " "))
		}
	}

	if !strings.Contains(text, "none has ever been created") {
		t.Error("a state directory without seats was not reported as having none")
	}
}

// The report is meant to be pasted in public, so the one thing it must never do
// is read the files that hold secrets. Checked by putting a marker in each of
// them and looking for it, rather than by reading the code and believing it.
func TestWriteNeverPrintsSecrets(t *testing.T) {
	cfg := scratch(t)

	const marker = "MARKER-e3f1a9c4-this-must-never-appear"

	if err := os.MkdirAll(filepath.Join(cfg.StateDir, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(cfg.StateDir, "tls"), 0o700); err != nil {
		t.Fatal(err)
	}

	files := []string{
		filepath.Join(cfg.StateDir, "credentials.json"),
		filepath.Join(cfg.StateDir, "secrets", "sunshine"),
		filepath.Join(cfg.StateDir, "tls", "key.pem"),
	}

	for _, path := range files {
		if err := os.WriteFile(path, []byte(marker), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if text := generate(t, cfg); strings.Contains(text, marker) {
		t.Error("the report printed the contents of a file holding secrets")
	}
}

// A seat record is not a secret and belongs in a report, which is the other
// half of the test above: it would also pass if the seats section printed
// nothing at all.
func TestWriteDescribesSeats(t *testing.T) {
	cfg := scratch(t)

	dir := filepath.Join(cfg.StateDir, "seats")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	record := `{"name":"lounge","label":"Lounge","autostart":true,` +
		`"resolution":"3840x2160@60Hz","library":true,"provisioned":1}`

	if err := os.WriteFile(filepath.Join(dir, "lounge.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}

	text := generate(t, cfg)

	for _, want := range []string{"lounge", "3840x2160@60Hz"} {
		if !strings.Contains(text, want) {
			t.Errorf("the report says nothing about %q", want)
		}
	}

	// Generation 1 is behind any build that has one at all, and a seat left
	// behind by an update is a thing the report has to name rather than imply.
	if !strings.Contains(text, "needs provisioning again") {
		t.Error("a seat built by an older recipe was not reported as behind")
	}
}

func TestSystemdSurvivesAUnitThatDoesNotExist(t *testing.T) {
	got := systemd("polyseat-definitely-not-a-unit.service")

	if got == "" {
		t.Error("an unknown unit produced an empty line rather than an answer")
	}
}

func TestFirstField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meminfo")

	body := "MemTotal:       32680020 kB\nMemFree:         1000 kB\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := firstField(path, "MemTotal"); got != "32680020 kB" {
		t.Errorf("MemTotal read as %q", got)
	}

	if got := firstField(path, "Nothing"); !strings.Contains(got, "no Nothing") {
		t.Errorf("a missing key answered %q", got)
	}

	if got := firstField(filepath.Join(t.TempDir(), "absent"), "MemTotal"); !strings.Contains(got, "could not be read") {
		t.Errorf("a missing file answered %q", got)
	}
}
