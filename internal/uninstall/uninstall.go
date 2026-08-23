// Package uninstall removes Polyseat from the machine it is running on.
//
// The awkward part is not what to delete. It is that the process doing the
// deleting is the thing being deleted: `systemctl stop polyseatd` issued by
// polyseatd kills the caller mid-sentence, and `pacman -R polyseat` takes the
// binary out from under it. So nothing here does the work. It hands a script to
// systemd as a transient unit, which is not this process's child and outlives
// it, and that script is host/uninstall.sh, installed as polyseat-uninstall.
//
// The same trick the restart after an update uses, for the same reason, and the
// same script somebody at a terminal would run: one procedure, and the order it
// has to happen in written down once. Removing a container while the daemon is
// still supervising it is what leaves Incus with a "Stopping instance" task
// that never finishes.
package uninstall

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Name is what the script is installed as, by both installers.
const Name = "polyseat-uninstall"

// Dirs are where it may be, in the order they are tried. See prepare.Dirs.
var Dirs = []string{"/usr/local/bin", "/usr/bin"}

// unit is what the transient unit is called, and therefore where the record of
// what happened ends up: journalctl -u polyseat-uninstall.
//
// It matters more here than for a restart. The interface disappears halfway
// through this by design, since the daemon serving it is the first thing to
// stop, so the journal is the only place the rest of the run is written down.
const unit = "polyseat-uninstall"

// Command finds the script, or says where it looked.
func Command() (string, error) {
	for _, dir := range Dirs {
		path := filepath.Join(dir, Name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}

	return "", fmt.Errorf("%s is not installed in %s, so Polyseat can only be removed from a checkout: sudo ./host/uninstall.sh",
		Name, strings.Join(Dirs, " or "))
}

// Options is how much goes.
type Options struct {
	// Seats deletes the containers, their definitions and the interface
	// password along with the daemon. Off means an install over the top picks
	// the seats up exactly as they were.
	Seats bool `json:"seats"`

	// Library deletes the shared game pool as well, which is the expensive
	// thing: the seats' copies come back from it by reflink in a second, where
	// downloading them again does not. Only meaningful with Seats, which the
	// script also refuses without.
	Library bool `json:"library"`
}

// Start schedules the removal and returns as soon as systemd has taken it.
//
// The delay is not a safety margin, it is the answer to the browser. This
// process is stopped a moment into the run, and without it the connection would
// be cut before the reply saying "removing" had been written.
func Start(opts Options) error {
	if opts.Library && !opts.Seats {
		return fmt.Errorf("the shared library only goes with the seats that share it")
	}

	path, err := Command()
	if err != nil {
		return err
	}

	out, err := exec.Command("systemd-run", args(path, opts)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("the removal could not be scheduled: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

// args is what systemd-run is given, apart from running it.
//
// Its own function because these five words decide whether somebody's seats
// still exist afterwards, and a test can read them where it cannot read a
// process that has already started.
func args(path string, opts Options) []string {
	out := []string{
		"--collect",
		"--unit=" + unit,
		"--description=Polyseat: remove Polyseat from this machine",
		"--on-active=2s",
		path,

		// There is nobody at a terminal to answer the question the script asks
		// before it deletes seats. That asking happened in the browser, which is
		// also where the password was typed.
		"--yes",
	}

	if opts.Seats {
		out = append(out, "--seats")
	}

	if opts.Library {
		out = append(out, "--library")
	}

	return out
}
