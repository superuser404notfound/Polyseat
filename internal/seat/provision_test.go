package seat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

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

// ------------------------------------------------------------- package runs

// The output that started this, from a seat that failed to build because a
// mirror went quiet halfway through the session packages.
const stalledMirror = `error: failed retrieving file 'speexdsp-1.2.1-2-x86_64.pkg.tar.zst' from mirrors.kernel.org : Operation too slow. Less than 1 bytes/sec transferred the last 10 seconds
warning: too many errors from mirrors.kernel.org, skipping for the remainder of this transaction
warning: failed to retrieve some files
error: failed to commit transaction (failed to retrieve some files)`

func TestTransientDownloadKnowsAMirrorFromAConflict(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want bool
	}{
		{name: "the mirror that stalled", text: stalledMirror, want: true},
		{name: "no name resolution", text: "error: Temporary failure in name resolution", want: true},
		{name: "the connection dropped", text: "error: Connection reset by peer", want: true},

		// The ones that must not be retried: three attempts would take three
		// times as long to say the same thing, behind two lines claiming it
		// was worth trying again.
		{name: "a package that does not exist", text: "error: target not found: sway-wrong", want: false},
		{name: "a file another package owns", text: "error: failed to commit transaction (conflicting files)\n/usr/bin/x exists in filesystem", want: false},
		{name: "a broken signature", text: "error: sway: signature from \"x\" is unknown trust", want: false},
		{name: "nothing at all", text: "", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := transientDownload(tc.text); got != tc.want {
				t.Errorf("transientDownload() = %v, wanted %v", got, tc.want)
			}
		})
	}
}

// A stall on one attempt costs a pause and nothing else. Without this the whole
// provisioning run is thrown away and somebody presses the button again.
func TestRetryPackagesTriesAgainAfterAStalledMirror(t *testing.T) {
	old := packageRetryPause
	packageRetryPause = time.Millisecond

	defer func() { packageRetryPause = old }()

	calls := 0
	said := []string{}

	out, err := retryPackages(context.Background(),
		func(format string, args ...any) { said = append(said, fmt.Sprintf(format, args...)) },
		func() (string, error) {
			calls++
			if calls == 1 {
				return stalledMirror, errors.New("exit 1")
			}

			return "there is nothing to do", nil
		})
	if err != nil {
		t.Fatalf("gave up on a mirror stall: %v", err)
	}

	if calls != 2 {
		t.Errorf("ran %d times, wanted 2", calls)
	}

	if out != "there is nothing to do" {
		t.Errorf("handed back %q, wanted the output of the attempt that worked", out)
	}

	if len(said) != 1 || !strings.Contains(said[0], "trying again") {
		t.Errorf("said %v, wanted one line about trying again", said)
	}
}

func TestRetryPackagesGivesUpAfterTheLastAttempt(t *testing.T) {
	old := packageRetryPause
	packageRetryPause = time.Millisecond

	defer func() { packageRetryPause = old }()

	calls := 0

	if _, err := retryPackages(context.Background(), nil, func() (string, error) {
		calls++

		return stalledMirror, errors.New("exit 1")
	}); err == nil {
		t.Fatal("a mirror that never came back was reported as a success")
	}

	if calls != packageAttempts {
		t.Errorf("ran %d times, wanted %d", calls, packageAttempts)
	}
}

// A conflict is not the network, and running it twice more would only delay
// the answer.
func TestRetryPackagesDoesNotRepeatAConflict(t *testing.T) {
	calls := 0

	if _, err := retryPackages(context.Background(), nil, func() (string, error) {
		calls++

		return "error: target not found: sway-wrong", errors.New("exit 1")
	}); err == nil {
		t.Fatal("a missing package was reported as a success")
	}

	if calls != 1 {
		t.Errorf("ran %d times, wanted 1", calls)
	}
}
