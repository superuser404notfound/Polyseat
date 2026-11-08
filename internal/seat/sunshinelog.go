package seat

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"time"
)

// Following Sunshine's log, so that a stream is known about the moment it starts.
//
// This used to close the application the moment a client left, and that was
// the wrong half of a trade. A resume runs no prep commands, which is where the
// seat learns the size of the client, so a client that came back with a
// different size got the old one (louis, 2026-10-03, a client that splits its
// screen). Closing turned every return into a launch that sized the seat, and
// it also turned every return into a launch: the game's own command ran again
// and the game started a second time over the one still running. Reported on
// 2026-11-08 as "fortsetzen öffnet das Spiel erneut".
//
// The size is now Sunshine's to pass on. Its package in a seat carries a patch,
// packaging/sunshine/resume-prep-cmds.patch, that runs the global prep commands
// again on a resume with the new client's size, framerate and name, so the
// application stays where the client left it and is only closed when somebody
// quits it in Moonlight.
//
// What is left here is the arrival: a stream shorter than the sweep's ten
// seconds would otherwise never be seen, and so never end either.

// The two lines this reads, as Sunshine prints them after its timestamp.
const (
	sunshineConnected    = "CLIENT CONNECTED"
	sunshineDisconnected = "CLIENT DISCONNECTED"
)

// followRetry is how long to wait before following the log again after the
// command ended. It ends when Sunshine's journal does, or when the seat is on
// its way down, and an exec into a seat that is stopping is what once hung the
// Incus daemon, see incusx.Exec. Five seconds is enough for the lifecycle event
// to arrive and stop this first.
const followRetry = 5 * time.Second

// follower is one seat's log reader.
type follower struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// sunshineLine is what a line of Sunshine's output says about clients: one of
// the two constants above, or nothing.
//
// The end of the line rather than anywhere in it, because a line Sunshine
// prints about anything else may quote a name, and a client called "CLIENT
// DISCONNECTED" is somebody's idea of a joke.
func sunshineLine(line string) string {
	line = strings.TrimSpace(line)

	switch {
	case strings.HasSuffix(line, "Info: "+sunshineDisconnected):
		return sunshineDisconnected
	case strings.HasSuffix(line, "Info: "+sunshineConnected):
		return sunshineConnected
	}

	return ""
}

// startFollowing begins reading a seat's Sunshine log, once.
func (m *Manager) startFollowing(name string) {
	// A Manager with no Incus behind it, which only the tests make, has no
	// seat to read.
	if m.client == nil {
		return
	}

	rt := m.runtimeOf(name)

	m.mu.Lock()

	if rt.follow != nil {
		m.mu.Unlock()

		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	f := &follower{cancel: cancel, done: make(chan struct{})}
	rt.follow = f
	m.mu.Unlock()

	go m.follow(ctx, name, f)
}

// stopFollowing ends it, and waits for the exec to let go of the seat so that
// a stop that follows does not find it still there.
func (m *Manager) stopFollowing(name string) {
	m.mu.Lock()
	rt, ok := m.rt[name]

	var f *follower
	if ok {
		f = rt.follow
		rt.follow = nil
	}

	m.mu.Unlock()

	if f == nil {
		return
	}

	f.cancel()

	select {
	case <-f.done:
	case <-time.After(followRetry):
	}
}

func (m *Manager) follow(ctx context.Context, name string, f *follower) {
	defer close(f.done)

	// -n 0 because what came before is somebody else's stream: an arrival
	// replayed from the past would mark a seat busy that nobody is using.
	argv := m.asPlayer(name, "journalctl", "--user", "-u", "polyseat-sunshine.service",
		"--follow", "--lines", "0", "--output", "cat", "--no-pager")

	var wait time.Duration

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		wait = followRetry

		if status, err := m.client.Status(name); err != nil || status != "Running" {
			continue
		}

		out := &lineWriter{line: func(line string) { m.sunshineSaid(name, line) }}
		_, _ = m.client.Exec(ctx, name, argv, nil, out, nil)
	}
}

// sunshineSaid acts on one line of the log.
//
// A departure needs nothing from here. The sweep sees the stream go and
// sessionEnded puts the seat back once it has stayed gone.
func (m *Manager) sunshineSaid(name, line string) {
	if sunshineLine(line) != sunshineConnected {
		return
	}

	// Somebody is streaming, and the sweep may not have seen it: a stream
	// shorter than ten seconds never reached a reading. Without this it never
	// ended either, and the seat would have stayed at that client's size.
	rt := m.runtimeOf(name)

	m.mu.Lock()
	rt.streaming = true
	rt.quiet = time.Time{}
	m.mu.Unlock()
}

// lineWriter hands what is written to it on one line at a time.
type lineWriter struct {
	mu   sync.Mutex
	buf  []byte
	line func(string)
}

func (w *lineWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf = append(w.buf, b...)

	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}

		w.line(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}

	// A line that never ends is not a line Sunshine printed.
	if len(w.buf) > 64<<10 {
		w.buf = w.buf[:0]
	}

	return len(b), nil
}
