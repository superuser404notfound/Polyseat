package seat

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"time"
)

// Following Sunshine's log, so that a client that leaves is let go of at once.
//
// A client that leaves without quitting leaves its application running in
// Sunshine, and the next connection resumes it. A resume runs no prep commands,
// which is where the seat learns the size of the client: Sunshine's resume
// handler parses the new mode and hands it only to the display code of other
// platforms. So a client that came back with a different size got the old one.
// Measured in louis with a client that splits its screen: launched at
// 3840x1080 for top and bottom at 11:09:06, gone at 11:10:33, back at 11:10:47
// asking for side by side, and streamed 3840x1080 again with no polyseat-resize
// in the log. vince did the same at 11:13:05 and was back two seconds later.
//
// sessionEnded already closes the application, but only once the stream has
// been gone for sessionGrace, read on a ten second sweep. Every reconnect above
// came well inside that. Nothing in the seat can close it sooner, because the
// login to Sunshine's API lives on the host. So the daemon reads the one place
// that says the moment a client leaves, Sunshine's own log, and closes the
// application from here.
//
// Every application in a seat is detached, so closing one ends no process, and
// a client that only dropped for a moment comes back through a launch that
// sizes the seat for it. The undo of the resize is gone from sunshine.conf for
// the same reason: run on every drop it would put a game through two mode
// changes for nothing. Putting the size back is sessionEnded's, after the
// grace period.

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

// letGoWithin is how long a departed client's stream may take to close its
// sockets before the application is closed. Sunshine tears a session down in
// well under a second, so a seat still busy after this is busy with somebody,
// and the close is left to sessionEnded.
const letGoWithin = 10 * time.Second

// letGoPoll is how often the seat is asked whether the stream is gone.
const letGoPoll = 250 * time.Millisecond

// follower is one seat's log reader.
type follower struct {
	cancel context.CancelFunc
	done   chan struct{}

	mu sync.Mutex

	// connects counts CLIENT CONNECTED lines. A departure remembers the count
	// it saw and gives up when it changes, because a client that is back must
	// not have the application closed under it.
	connects uint64
}

func (f *follower) connected() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.connects++

	return f.connects
}

func (f *follower) seen() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.connects
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

	// -n 0 because what came before is somebody else's stream: a departure
	// replayed from the past would close the application under whoever is
	// playing now.
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

		out := &lineWriter{line: func(line string) { m.sunshineSaid(ctx, name, f, line) }}
		_, _ = m.client.Exec(ctx, name, argv, nil, out, nil)
	}
}

// sunshineSaid acts on one line of the log.
func (m *Manager) sunshineSaid(ctx context.Context, name string, f *follower, line string) {
	switch sunshineLine(line) {
	case sunshineConnected:
		f.connected()

		// Somebody is streaming, and the sweep may not have seen it: a stream
		// shorter than ten seconds never reached a reading. Without this it
		// never ended either, and since the undo no longer puts the size back,
		// the seat would have stayed at that client's size.
		rt := m.runtimeOf(name)

		m.mu.Lock()
		rt.streaming = true
		rt.quiet = time.Time{}
		m.mu.Unlock()

	case sunshineDisconnected:
		// On its own goroutine, because it waits for the stream to go away and
		// the log has to go on being read meanwhile: it is what says the client
		// came back.
		seen := f.seen()

		go func() {
			if letGo(ctx, func() bool { return f.seen() != seen },
				func(ctx context.Context) bool { return !m.streaming(ctx, name) },
				func(ctx context.Context) { m.closeApp(ctx, name) }) {
				m.logf(name, "a client left, so Sunshine was told the stream is over "+
					"and the next connection is sized for whoever makes it")
			}
		}()
	}
}

// letGo closes the application once the departed client's stream is gone,
// unless somebody came back first. It reports whether it closed it.
//
// Asked rather than assumed, because CLIENT DISCONNECTED is printed before
// Sunshine has taken the session down, and the check that guards everything
// else that ends a stream is the one used here: see streaming.
func letGo(ctx context.Context, back func() bool, idle func(context.Context) bool, closeApp func(context.Context)) bool {
	ctx, cancel := context.WithTimeout(ctx, letGoWithin)
	defer cancel()

	for {
		if back() {
			return false
		}

		if idle(ctx) {
			// Once more, since asking took a moment.
			if back() {
				return false
			}

			closeApp(ctx)

			return true
		}

		select {
		case <-ctx.Done():
			return false
		case <-time.After(letGoPoll):
		}
	}
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
