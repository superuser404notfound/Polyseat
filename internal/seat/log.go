package seat

import (
	"sync"
	"time"
)

// Log is a per seat ring of recent lines.
//
// Kept in memory rather than written anywhere. This is progress for somebody
// watching the interface, not a record: everything worth keeping already goes
// to the daemon's own log through slog, and the seats keep their journals
// inside their containers.
type Log struct {
	mu    sync.Mutex
	lines []string
	max   int
}

// NewLog returns a ring holding at most max lines.
func NewLog(max int) *Log {
	return &Log{max: max}
}

// Add appends a line, stamped with the time it arrived.
func (l *Log) Add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, time.Now().Format("15:04:05")+"  "+line)
	if len(l.lines) > l.max {
		l.lines = l.lines[len(l.lines)-l.max:]
	}
}

// Lines returns a copy of what is in the ring.
func (l *Log) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]string, len(l.lines))
	copy(out, l.lines)

	return out
}
