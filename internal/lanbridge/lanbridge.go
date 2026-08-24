// Package lanbridge runs host/lan-bridge.sh from the daemon.
//
// The same shape as internal/prepare, and for the same reason: the script is
// the one copy of the procedure. It is what somebody at a terminal runs as
// polyseat-lan-bridge, it is where the rollback lives and where the reasoning
// about macvlan, EBUSY and NetworkManager's invented profiles is written down,
// and a second implementation of it in Go would mean one of the two is wrong
// and nobody knows which.
//
// So this is a pipe and a lookup, with one addition prepare has no use for:
// putting the seats back.
//
// lan-bridge.sh stops every seat before it touches the interface, because the
// kernel refuses to make an interface a bridge port while a macvlan hangs off
// it, and a seat's macvlan counts even from inside its own network namespace.
// It then deliberately does not start them again: starting a seat is more than
// `incus start`, and the script says so and prints the names for somebody to
// start from the interface. From in here that stops being a limitation and
// becomes the reason this is worth a button at all. The daemon is the thing
// that knows the real start path, so the seats come back by themselves.
package lanbridge

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
const Name = "polyseat-lan-bridge"

// Dirs are where it may be, in the order they are tried. The same two places
// and the same order as prepare.Dirs: a checkout install writes under
// /usr/local, an Arch package may only write under /usr, and one binary has to
// find it under both.
var Dirs = []string{"/usr/local/bin", "/usr/bin"}

// timeout bounds a run.
//
// Generous against the two slow steps, which are both waits rather than work:
// every running seat is stopped with a 90 second limit of its own, and the
// bridge is then given 30 seconds to be handed an address before the run is
// called a failure and put back. Bounded at all because this is reached from a
// browser, and a button that never comes back is worse than one that fails.
const timeout = 10 * time.Minute

// maxLog is how many lines are kept for the page. Small next to prepare's,
// because this script prints a step per thing it does and no progress bars.
const maxLog = 200

// Command finds the script, or says where it looked.
func Command() (string, error) {
	for _, dir := range Dirs {
		path := filepath.Join(dir, Name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}

	return "", fmt.Errorf("%s is not installed in %s, so the uplink can only be bridged from a checkout: sudo ./host/lan-bridge.sh",
		Name, strings.Join(Dirs, " or "))
}

// State is what the interface needs to draw the panel and its log.
type State struct {
	// Command is the script that would run, empty when there is none, and
	// Reason is why. Named rather than hidden, for the same reason prepare
	// names it: which of the two installs this is decides where to look.
	Command string `json:"command"`
	Reason  string `json:"reason"`

	// Undo is the direction of the run being reported. Both directions stop the
	// seats and both take a moment with no network, so the page has to be able
	// to say which one it is watching rather than "running".
	Undo bool `json:"undo"`

	Running bool     `json:"running"`
	Log     []string `json:"log"`
	Error   string   `json:"error"`
	Done    bool     `json:"done"`
}

// Runner owns the one run there may be at a time.
//
// One at a time because both directions take the machine's address off one
// interface and put it on another, and two of those at once is a machine with
// its address on neither.
type Runner struct {
	mu      sync.Mutex
	running bool
	undo    bool
	log     []string
	err     string
	done    bool
}

// State reports what has happened so far. Safe while a run is going on.
func (r *Runner) State() State {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := State{
		Undo:    r.undo,
		Running: r.running,
		Log:     append([]string{}, r.log...),
		Error:   r.err,
		Done:    r.done,
	}

	if path, err := Command(); err == nil {
		out.Command = path
	} else {
		out.Reason = err.Error()
	}

	return out
}

// Running says whether a run is going on right now.
func (r *Runner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.running
}

// Start runs the script in the background and returns once it is under way.
//
// resume is called when the script has finished, with the same sink its own
// lines went to. Two things follow from where it is called. It runs whether the
// script succeeded or not, because a run that failed put the network back and
// left the seats stopped exactly as a run that succeeded did, and seats left
// down after a failure is the worst of the outcomes rather than the safe one.
// And its lines land in the log somebody is already watching, so the seats
// coming back is part of what they can see rather than something in the journal.
//
// notify is called whenever there is something new to see, which is how the
// page finds out: the daemon already pushes a token on every change and this
// borrows the same road. Both may be nil.
func (r *Runner) Start(undo bool, notify func(), resume func(log func(string))) error {
	path, err := Command()
	if err != nil {
		return err
	}

	r.mu.Lock()

	if r.running {
		r.mu.Unlock()

		return errors.New("the uplink is already being changed")
	}

	r.running = true
	r.undo = undo
	r.log = nil
	r.err = ""
	r.done = false
	r.mu.Unlock()

	if notify != nil {
		notify()
	}

	go r.run(path, undo, notify, resume)

	return nil
}

func (r *Runner) run(path string, undo bool, notify func(), resume func(log func(string))) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	write := func(line string) {
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
	}

	err := Run(ctx, path, undo, write)

	if resume != nil {
		resume(write)
	}

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
// Nothing is attached to standard input, and the script asks nothing: every
// decision it makes is read off the machine. What it does need is a PATH, since
// it calls nmcli, incus, ip and ping, and a unit inherits whatever it was
// configured with.
func Run(ctx context.Context, path string, undo bool, progress func(string)) error {
	var args []string
	if undo {
		args = append(args, "--undo")
	}

	cmd := exec.CommandContext(ctx, path, args...)

	cmd.Env = append(os.Environ(),
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",

		// No colour, because these lines end up as text in a browser.
		"NO_COLOR=1",
		"POLYSEAT_FROM_DAEMON=1",
	)

	// One pipe for both, so the lines arrive interleaved the way they would on
	// a terminal. Reading two would put every warning at the end.
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

	// Closed here as soon as the child has its copy, or the scan below would
	// never see an end of file and this would hang after the script finished.
	_ = write.Close()

	done := make(chan struct{})

	go func() {
		defer close(done)

		scanner := bufio.NewScanner(read)

		for scanner.Scan() {
			if progress != nil {
				progress(clean(scanner.Text()))
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

// clean takes the terminal out of a line.
//
// NO_COLOR is honoured by the script's own ok/bad/warn helpers and not by
// anything it calls, so a colour sequence can still arrive from nmcli or incus.
// In a browser those are not invisible, they are printed as text.
func clean(line string) string {
	var out strings.Builder

	for i := 0; i < len(line); i++ {
		if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '[' {
			i += 2
			for i < len(line) && (line[i] < '@' || line[i] > '~') {
				i++
			}

			continue
		}

		out.WriteByte(line[i])
	}

	return strings.TrimRight(out.String(), " \t")
}
