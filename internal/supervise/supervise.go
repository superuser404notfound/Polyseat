// Package supervise runs the daemon's helper processes and keeps them running.
//
// Polyseat has two of them: one uhid observer for the whole host, and one input
// broker per running seat. They used to be systemd units, a template unit
// instantiated per seat, which worked but put the seat lifecycle in two places
// at once. systemd knew when a broker should run, the daemon knew when a seat
// was up, and neither could see the other. Now the daemon starts a broker
// exactly when its seat is running and stops it before the container goes
// down, which is the ordering that keeps Incus from wedging.
package supervise

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// State is what a supervised process is doing.
type State string

const (
	// Stopped means it is not meant to be running.
	Stopped State = "stopped"
	// Running means it is up.
	Running State = "running"
	// Restarting means it exited and is being started again.
	Restarting State = "restarting"
	// Failed means it kept exiting and has been given up on.
	Failed State = "failed"
)

// maxBackoff caps the wait between restarts. A broker that cannot start, say
// because Python is missing, should keep saying so in the log without spinning.
const maxBackoff = 30 * time.Second

// Process is one supervised child.
type Process struct {
	// Argv is the command. Always a list, never a shell string: the broker
	// consumes device names that come from inside a container, and none of
	// that may ever reach a shell.
	Argv []string

	// OnOutput receives every line the process writes, tagged by stream.
	OnOutput func(line string)

	// OnState is called whenever the state changes.
	OnState func(State)

	// Fatal decides whether an exit code means another attempt is pointless.
	//
	// Nil retries everything, which is what a broker wants: most ways one dies
	// are ways it might not die next time. The uhid observer has one that is
	// not. A kernel with no uhid_dev_create2 in it will still have none in
	// thirty seconds, so retrying writes six lines of the same bpftrace error
	// into the journal twice a minute until the machine is rebooted, and the
	// interface shows "restarting" forever for something that has in fact
	// settled. Failed is the honest answer and the broker's fallback covers it.
	Fatal func(code int) bool

	mu      sync.Mutex
	state   State
	cancel  context.CancelFunc
	done    chan struct{}
	backoff time.Duration
}

// New prepares a process without starting it.
func New(argv []string) *Process {
	return &Process{Argv: argv, state: Stopped}
}

// State reports the current state.
func (p *Process) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.state
}

func (p *Process) setState(s State) {
	p.mu.Lock()
	changed := p.state != s
	p.state = s
	cb := p.OnState
	p.mu.Unlock()

	if changed && cb != nil {
		cb(s)
	}
}

// Start begins supervising. Starting an already running process does nothing.
func (p *Process) Start() {
	p.mu.Lock()

	if p.cancel != nil {
		p.mu.Unlock()

		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	p.backoff = 0
	done := p.done
	p.mu.Unlock()

	go p.supervise(ctx, done)
}

// Stop terminates the process and waits for it to be gone.
//
// The wait is the point. A broker that is still polling `incus exec` while its
// container is being stopped once left the Incus daemon hung in "Stopping
// instance" with the container already dead, so the caller has to be able to
// rely on this having finished.
func (p *Process) Stop() {
	p.mu.Lock()
	cancel := p.cancel
	done := p.done
	p.cancel = nil
	p.mu.Unlock()

	if cancel == nil {
		return
	}

	cancel()
	<-done
	p.setState(Stopped)
}

func (p *Process) supervise(ctx context.Context, done chan struct{}) {
	defer close(done)

	for {
		if ctx.Err() != nil {
			return
		}

		p.setState(Running)
		start := time.Now()
		err := p.runOnce(ctx)

		if ctx.Err() != nil {
			return
		}

		// Asked before the backoff, because the point is not to wait at all.
		// The code comes from the child rather than from matching its output:
		// the helper already knows which of its failures this is, and the
		// daemon should not have to read English to find out.
		var exit *exec.ExitError
		if p.Fatal != nil && errors.As(err, &exit) && p.Fatal(exit.ExitCode()) {
			if p.OnOutput != nil {
				p.OnOutput("giving up: " + err.Error())
			}

			p.setState(Failed)

			return
		}

		// A process that stayed up for a while and then died is worth
		// restarting straight away; one that dies immediately is not.
		if time.Since(start) > time.Minute {
			p.backoff = 0
		} else if p.backoff == 0 {
			p.backoff = time.Second
		} else if p.backoff < maxBackoff {
			p.backoff *= 2
		}

		if err != nil && p.OnOutput != nil {
			p.OnOutput("exited: " + err.Error())
		}

		p.setState(Restarting)

		select {
		case <-ctx.Done():
			return
		case <-time.After(p.backoff):
		}
	}
}

func (p *Process) runOnce(ctx context.Context) error {
	cmd := exec.Command(p.Argv[0], p.Argv[1:]...)

	// Own process group, so a stop reaches anything the helper spawned. The
	// uhid observer runs bpftrace as a child, and a bpftrace left behind keeps
	// a kprobe attached.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	var wg sync.WaitGroup

	wg.Add(2)

	go func() { defer wg.Done(); p.pump(stdout) }()
	go func() { defer wg.Done(); p.pump(stderr) }()

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	select {
	case err := <-waited:
		wg.Wait()

		return err
	case <-ctx.Done():
		pgid := cmd.Process.Pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM)

		select {
		case <-waited:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			<-waited
		}

		wg.Wait()

		return nil
	}
}

func (p *Process) pump(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	for scanner.Scan() {
		if p.OnOutput != nil {
			p.OnOutput(scanner.Text())
		}
	}
}
