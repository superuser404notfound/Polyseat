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
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
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

	// stopping is closed when a Stop in progress has finished, and nil when
	// none is. See Start.
	stopping chan struct{}
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
//
// Starting one that is still being stopped waits for the stop to finish first.
// Stop gives up the handle before it waits, so without this a Start in that
// window found nothing running and launched a second child beside the one
// still shutting down, and the Stop then finished by marking the new one
// Stopped while it ran. Two brokers for one seat, and a state that lied.
func (p *Process) Start() {
	for {
		p.mu.Lock()

		if p.cancel != nil {
			p.mu.Unlock()

			return
		}

		if stopping := p.stopping; stopping != nil {
			p.mu.Unlock()
			<-stopping

			continue
		}

		ctx, cancel := context.WithCancel(context.Background())
		p.cancel = cancel
		p.done = make(chan struct{})
		p.backoff = 0
		done := p.done
		p.mu.Unlock()

		go p.supervise(ctx, done)

		return
	}
}

// Stop terminates the process and waits for it to be gone.
//
// The wait is the point. A broker that is still polling `incus exec` while its
// container is being stopped once left the Incus daemon hung in "Stopping
// instance" with the container already dead, so the caller has to be able to
// rely on this having finished. That holds for a second Stop arriving while a
// first is still waiting too: it waits for the same thing rather than
// returning early because the first one already took the handle.
func (p *Process) Stop() {
	p.mu.Lock()
	cancel := p.cancel
	done := p.done

	if cancel == nil {
		stopping := p.stopping
		p.mu.Unlock()

		if stopping != nil {
			<-stopping
		}

		return
	}

	p.cancel = nil
	stopping := make(chan struct{})
	p.stopping = stopping
	p.mu.Unlock()

	cancel()
	<-done
	p.setState(Stopped)

	p.mu.Lock()
	p.stopping = nil
	p.mu.Unlock()
	close(stopping)
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

// outputGrace is how long Wait keeps reading output after the child has exited.
//
// Output normally ends with the child, and then Wait returns as soon as the
// last line is read. It does not end when something the child started is
// still alive and still holds the other end: a bpftrace outliving its
// observer would otherwise keep this waiting for as long as it lives.
const outputGrace = 2 * time.Second

func (p *Process) runOnce(ctx context.Context) error {
	cmd := exec.Command(p.Argv[0], p.Argv[1:]...)

	// Own process group, so a stop reaches anything the helper spawned. The
	// uhid observer runs bpftrace as a child, and a bpftrace left behind keeps
	// a kprobe attached.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Writers rather than StdoutPipe, and the difference is the last lines.
	// With a pipe the reading was ours and the Wait was exec's, running side by
	// side, and Wait closes the read end as soon as the child has exited, so
	// whatever was still in the pipe then was thrown away. That is exactly the
	// part that says why a helper died. Given a writer, exec does the copying
	// itself and Wait returns only once it has reached the end.
	stdout := &lines{emit: p.OnOutput}
	stderr := &lines{emit: p.OnOutput}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = outputGrace

	if err := cmd.Start(); err != nil {
		return err
	}

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	var err error

	select {
	case err = <-waited:
	case <-ctx.Done():
		pgid := cmd.Process.Pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM)

		select {
		case <-waited:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			<-waited
		}
	}

	// After Wait, so that nothing is writing any more.
	stdout.flush()
	stderr.flush()

	if ctx.Err() != nil {
		return nil
	}

	// Output held open past the child by something it started is not a
	// reason to treat a clean exit as a failure; the exit itself was clean.
	if errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}

	return err
}

// maxLine is where a line with no end is cut and passed on anyway. A helper
// that writes a megabyte without a newline should not grow this without bound,
// and cutting it is better than stopping reading, which is what a Scanner does
// and which leaves the child blocked on a full pipe.
const maxLine = 256 * 1024

// lines turns a stream of writes into whole lines for OnOutput.
//
// Only exec's copying goroutine for one stream writes to it, and flush is only
// called after Wait, so it needs no lock of its own.
type lines struct {
	emit func(string)
	buf  []byte
}

func (l *lines) Write(b []byte) (int, error) {
	n := len(b)

	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			l.buf = append(l.buf, b...)

			if len(l.buf) >= maxLine {
				l.line(l.buf[:maxLine])
				l.buf = append(l.buf[:0], l.buf[maxLine:]...)
			}

			break
		}

		l.buf = append(l.buf, b[:i]...)
		l.line(l.buf)
		l.buf = l.buf[:0]
		b = b[i+1:]
	}

	return n, nil
}

// flush passes on a last line that had no newline after it.
func (l *lines) flush() {
	if len(l.buf) > 0 {
		l.line(l.buf)
		l.buf = l.buf[:0]
	}
}

func (l *lines) line(b []byte) {
	if l.emit != nil {
		l.emit(strings.TrimSuffix(string(b), "\r"))
	}
}
