package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The same binary is installed two ways and has to find its helpers under
// either. A checkout install puts them under /usr/local, an Arch package may
// not write there at all and uses /usr.
func TestFindHelperDir(t *testing.T) {
	local := filepath.Join(t.TempDir(), "local")
	system := filepath.Join(t.TempDir(), "system")

	candidates := []string{local, system}

	// Nothing anywhere: the answer names the place a checkout would have used,
	// because that is where whoever reads the error will look first.
	if got := FindHelperDir(candidates); got != local {
		t.Errorf("with nothing installed the answer is %q, want %q", got, local)
	}

	// An empty directory is not an installation. Uninstalls leave those behind,
	// and picking one turns "no helper here" into "the broker will not start",
	// which is the same fault reported as a harder one.
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(system, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(system, "broker.py"), []byte("#\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := FindHelperDir(candidates); got != system {
		t.Errorf("an empty local directory won over a real installation: %q", got)
	}

	// Both present: the local one wins, the same order a shell uses for the
	// binary, so somebody testing a build from a checkout gets what they built.
	if err := os.WriteFile(filepath.Join(local, "broker.py"), []byte("#\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := FindHelperDir(candidates); got != local {
		t.Errorf("with both installed the answer is %q, want %q", got, local)
	}
}

// A file that says nothing about the helpers must not leave the daemon with no
// answer at all, which is what an empty default would mean.
func TestLoadFillsInTheHelperDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyseatd.json")

	if err := os.WriteFile(path, []byte(`{"listen":"127.0.0.1:47800"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.HelperDir == "" {
		t.Error("a configuration that does not mention the helpers left the daemon without a directory")
	}

	// And a file that does name one is left alone, or the setting would exist
	// in name only.
	if err := os.WriteFile(path, []byte(`{"helper_dir":"/opt/somewhere"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.HelperDir != "/opt/somewhere" {
		t.Errorf("a configured helper directory was overwritten with %q", cfg.HelperDir)
	}
}

// No file at all is the normal state of a fresh install, and it has to come out
// the same way.
func TestLoadWithoutAFile(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.HelperDir == "" {
		t.Error("no configuration file left the daemon without a helper directory")
	}

	if !cfg.UpdateCheck {
		t.Error("update checking is meant to be on by default")
	}
}
