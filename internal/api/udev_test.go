package api

import (
	"os"
	"path/filepath"
	"testing"
)

// The rule lives in a different directory depending on how Polyseat was
// installed, and looking in one of them was a real bug: every package install
// was told in the interface that the rule protecting it was missing, while it
// was in place and working.
func TestUdevRuleIsFoundWhereverAnInstallerPutsIt(t *testing.T) {
	// The two that actually happen, named for what puts them there.
	for _, where := range []struct{ why, dir string }{
		{"host/install.sh", "/etc/udev/rules.d"},
		{"the package", "/usr/lib/udev/rules.d"},
		{"a rule dropped at runtime", "/run/udev/rules.d"},
		{"a local build outside a package", "/usr/local/lib/udev/rules.d"},
	} {
		// A subtest each, so that the search path is put back between cases.
		// Written as one loop first, and every case after the first failed:
		// t.Cleanup runs at the end of the test and not the end of the
		// iteration, so each case re-rooted the paths the last one had already
		// rooted. The test was wrong and said so, which is the point of running
		// it against a case that should pass.
		t.Run(where.dir, func(t *testing.T) {
			root := t.TempDir()
			full := filepath.Join(root, where.dir)

			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(filepath.Join(full, udevRuleName), []byte("# rule\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			withRuleDirs(t, root)

			if !udevRuleInstalled() {
				t.Errorf("did not find the rule where %s puts it (%s)", where.why, where.dir)
			}
		})
	}
}

func TestUdevRuleIsMissingWhenItReallyIs(t *testing.T) {
	withRuleDirs(t, t.TempDir())

	if udevRuleInstalled() {
		t.Error("found a rule in a tree that has none")
	}
}

// A file of another name in the right directory is not the rule. The number in
// it decides whether it wins against 70-uaccess.rules, so a copy left behind at
// the old name is a file that exists and does nothing.
func TestUdevRuleIsNotAnyFileInTheDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "/etc/udev/rules.d")

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "70-polyseat-hide.rules"), []byte("# old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	withRuleDirs(t, root)

	if udevRuleInstalled() {
		t.Error("took the old 70- copy for the rule that replaced it")
	}
}

// withRuleDirs points the search at a tree a test built, and puts it back.
func withRuleDirs(t *testing.T, root string) {
	t.Helper()

	was := udevRuleDirs
	rooted := make([]string, 0, len(was))

	for _, dir := range was {
		rooted = append(rooted, filepath.Join(root, dir))
	}

	udevRuleDirs = rooted

	t.Cleanup(func() { udevRuleDirs = was })
}
