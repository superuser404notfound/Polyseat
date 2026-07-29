package seat

import "testing"

// The extension is versioned with the runtime, so the branch has to come from
// what the seat actually has. Installing the wrong one leaves the sandbox
// without a limiter and nothing says so.
func TestPlatformBranchesTakesTheVersionsInUse(t *testing.T) {
	list := `org.freedesktop.Platform	25.08
org.freedesktop.Platform.Compat.i386	25.08
org.freedesktop.Platform.GL.default	25.08-extra
org.freedesktop.Platform.GL.nvidia-610-43-03	1.4
org.gnome.Platform	48
org.freedesktop.Platform	24.08
org.freedesktop.Platform	25.08`

	got := platformBranches(list)

	if len(got) != 2 || got[0] != "25.08" || got[1] != "24.08" {
		t.Errorf("read %v, want the two freedesktop runtimes once each", got)
	}
}

// Only the freedesktop runtime. The extension does not exist for the others,
// and asking for it is a failed install reported to somebody who installed a
// launcher and has no idea what a runtime is.
func TestPlatformBranchesIgnoresOtherRuntimes(t *testing.T) {
	list := `org.gnome.Platform	48
org.kde.Platform	6.9
org.freedesktop.Platform.GL.default	25.08`

	if got := platformBranches(list); len(got) != 0 {
		t.Errorf("read %v, want nothing", got)
	}
}
