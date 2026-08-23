// Package prepare runs the machine half of the installation from the daemon.
//
// It runs a script rather than doing the work itself, and that is the whole
// design. host/prepare.sh is what an Arch package may not do for you: the idmap
// range, `incus admin init`, the driver check, the input group. It is tested
// against a fresh virtual machine by host/test-install.sh, it is what somebody
// at a terminal runs as polyseat-prepare, and reimplementing it here in Go
// would mean two of it. Two of it means one of them is wrong and nobody knows
// which, which is exactly the failure this project keeps a single copy of
// prepare.sh to avoid.
//
// So this is a pipe and a lookup: find the command, run it with no terminal
// attached, and hand the lines back to whoever is watching the page.
package prepare

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Name is what the script is installed as, by both installers.
const Name = "polyseat-prepare"

// Dirs are where it may be, in the order they are tried.
//
// The same two places and the same order as the input helpers in
// config.HelperDirs, and for the same reason: a checkout install puts its
// commands under /usr/local, an Arch package may only write under /usr, and one
// binary has to work under both. Local first, which is the order a shell would
// use.
var Dirs = []string{"/usr/local/bin", "/usr/bin"}

// timeout bounds a run. Generous because the slow step is pacman installing
// Incus and a container toolkit over somebody's connection, and bounded at all
// because a pacman waiting on a locked database waits forever and a button in a
// browser that never comes back is worse than one that fails.
const timeout = 30 * time.Minute

// maxLog is how many lines are kept for the page.
//
// Enough for a full run with several packages installed, and a limit at all
// because pacman's progress bars are lines too: a daemon that keeps every one
// of them holds a download's worth of text for as long as it runs.
const maxLog = 600

// Command finds the script, or says where it looked.
func Command() (string, error) {
	for _, dir := range Dirs {
		path := filepath.Join(dir, Name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}

	return "", fmt.Errorf("%s is not installed in %s, so this machine can only be prepared from a checkout: sudo ./host/prepare.sh",
		Name, strings.Join(Dirs, " or "))
}

// State is what the interface needs to draw the panel and its log.
type State struct {
	// Command is the script that would run, empty when there is none. The page
	// names it rather than saying "not available", because which of the two
	// installs this is decides where somebody should look.
	Command string `json:"command"`

	// Reason is why Command is empty.
	Reason string `json:"reason"`

	// Accounts are the human accounts on this machine, for the input group.
	// sudo answers that question by itself and a daemon started by systemd
	// cannot, so it is asked in the browser.
	Accounts []string `json:"accounts"`

	Running bool     `json:"running"`
	Log     []string `json:"log"`
	Error   string   `json:"error"`

	// Done is whether the last run finished without an error. The page uses it
	// to offer the restart, which is the step that turns a prepared machine
	// into a running one.
	Done bool `json:"done"`
}

// Runner owns the one run there may be at a time.
//
// One at a time because the work is pacman and `incus admin init`, and a second
// one would either sit on the database lock or race the first through the same
// files. The lock is here rather than in the handler so that the two servers,
// the ordinary one and the one that comes up when Incus is not there, share it.
type Runner struct {
	mu      sync.Mutex
	running bool
	log     []string
	err     string
	done    bool
}

// State reports what has happened so far. Safe while a run is going on.
func (r *Runner) State() State {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := State{
		Accounts: Accounts(),
		Running:  r.running,
		Log:      append([]string{}, r.log...),
		Error:    r.err,
		Done:     r.done,
	}

	if path, err := Command(); err == nil {
		out.Command = path
	} else {
		out.Reason = err.Error()
	}

	if out.Accounts == nil {
		out.Accounts = []string{}
	}

	return out
}

// Running says whether a run is going on right now.
//
// Asked by everything that would otherwise interrupt it. Preparing a machine
// runs pacman, and pacman killed halfway leaves a lock and a half applied
// transaction behind, which is a worse machine than the one this started with.
func (r *Runner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.running
}

// Start runs the script in the background and returns once it is under way.
//
// notify is called whenever there is something new to see, which is how the
// page finds out: the daemon already pushes a token on every change and this
// borrows the same road. It may be nil.
func (r *Runner) Start(account string, notify func()) error {
	path, err := Command()
	if err != nil {
		return err
	}

	if account != "" && !known(account) {
		return fmt.Errorf("there is no account called %q on this machine", account)
	}

	r.mu.Lock()

	if r.running {
		r.mu.Unlock()

		return errors.New("this machine is already being prepared")
	}

	r.running = true
	r.log = nil
	r.err = ""
	r.done = false
	r.mu.Unlock()

	if notify != nil {
		notify()
	}

	go r.run(path, account, notify)

	return nil
}

// run does the work. Everything it learns goes into the log rather than into a
// return value: whoever pressed the button is not waiting on this call.
func (r *Runner) run(path, account string, notify func()) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	err := Run(ctx, path, account, func(line string) {
		r.mu.Lock()

		r.log = append(r.log, line)
		if len(r.log) > maxLog {
			// The oldest go, not the newest. What went wrong is at the end.
			r.log = r.log[len(r.log)-maxLog:]
		}

		r.mu.Unlock()

		if notify != nil {
			notify()
		}
	})

	r.mu.Lock()
	r.running = false

	if err != nil {
		r.err = err.Error()
	} else {
		r.done = true
	}

	r.mu.Unlock()

	if notify != nil {
		notify()
	}
}

// Run executes the script once, calling progress with every line it writes.
//
// Nothing is attached to standard input, which is not an oversight: prepare.sh
// asks before installing a driver only when it has a terminal to ask at, and a
// daemon that answered that question on somebody's behalf would be installing
// kernel modules from a web page. Without a terminal it says what is missing
// and stops, which is the right answer to give a browser.
func Run(ctx context.Context, path, account string, progress func(string)) error {
	cmd := exec.CommandContext(ctx, path)

	// A PATH of its own, because systemd gives a unit whatever it was
	// configured with and this script calls pacman, incus, systemctl, findmnt,
	// ip, modprobe and usermod. A missing PATH entry here would surface as a
	// step that silently did nothing.
	cmd.Env = append(os.Environ(),
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",

		// No colour, because these lines end up as text in a browser, and no
		// closing "now start it", because it is already running.
		"NO_COLOR=1",
		"POLYSEAT_FROM_DAEMON=1",
	)

	if account != "" {
		cmd.Env = append(cmd.Env, "POLYSEAT_INPUT_USER="+account)
	}

	// One pipe for both, so that the lines arrive interleaved the way they
	// would on a terminal. Reading two would put every warning at the end.
	read, write, err := os.Pipe()
	if err != nil {
		return err
	}

	cmd.Stdout = write
	cmd.Stderr = write

	if err := cmd.Start(); err != nil {
		_ = read.Close()
		_ = write.Close()

		return err
	}

	// Closed in this process as soon as the child has its copy, or the scanner
	// below would never see an end of file and this would hang after the script
	// had finished.
	_ = write.Close()

	done := make(chan struct{})

	go func() {
		defer close(done)

		scanner := bufio.NewScanner(read)

		// pacman draws progress bars, and a bar is one very long line. The
		// default 64k limit would end the scan in the middle of an install and
		// leave the rest of the run unreported.
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for scanner.Scan() {
			if progress != nil {
				progress(lastFrame(scanner.Text()))
			}
		}
	}()

	waitErr := cmd.Wait()

	<-done

	_ = read.Close()

	if waitErr != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), waitErr)
	}

	return nil
}

// lastFrame keeps what a terminal would have shown of a line.
//
// pacman redraws a progress bar by returning the carriage and writing over
// itself, so one line of output can carry fifty versions of the same bar. On a
// terminal only the last one is visible; without this the page shows all fifty
// end to end.
func lastFrame(line string) string {
	if i := strings.LastIndexByte(line, '\r'); i >= 0 {
		line = line[i+1:]
	}

	return strings.TrimRight(line, " \t")
}

// Accounts lists the human accounts on this machine.
//
// For one question only: whose account goes in the input group. prepare.sh
// takes that from SUDO_USER, which exists because a person ran it; the daemon
// was started by systemd at boot and has nobody to ask, so the page asks and
// this is the list it offers.
//
// Read from /etc/passwd rather than from a name service, because the account
// has to be local for usermod to change it anyway. The range is the one
// useradd uses by default on Arch, and nobody is here: it is the account
// systemd hands to processes that must not have one.
func Accounts() []string {
	return accountsFrom(passwdFile)
}

// passwdFile is a variable so that the parsing above can be tested against a
// file this machine does not have to be.
var passwdFile = "/etc/passwd"

func accountsFrom(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var out []string

	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 7 {
			continue
		}

		name, shell := fields[0], fields[6]

		uid := 0
		if _, err := fmt.Sscanf(fields[2], "%d", &uid); err != nil {
			continue
		}

		if uid < 1000 || uid >= 65534 {
			continue
		}

		// An account that cannot log in is not somebody who will ever plug a
		// gamepad into this machine.
		if strings.HasSuffix(shell, "/nologin") || strings.HasSuffix(shell, "/false") {
			continue
		}

		out = append(out, name)
	}

	return out
}

// known says whether Accounts offers this name.
func known(account string) bool {
	for _, name := range Accounts() {
		if name == account {
			return true
		}
	}

	return false
}
