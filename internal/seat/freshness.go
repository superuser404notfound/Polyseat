package seat

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Freshness is what a seat is behind on.
//
// Two different kinds of behind, kept apart because they are fixed by different
// things and one of them was already here. Stale on Status means the daemon's
// provisioning recipe moved on, which is Generation, a constant in this source.
// This is the other one: the seat was built correctly and the world outside it
// has since published newer software. Nothing in Generation notices that, and
// nothing was noticing it at all before this, so a seat kept whatever Sunshine
// was current on the day it was built until an unrelated recipe change happened
// to rebuild it.
type Freshness struct {
	// Sunshine is the version in the seat, as pacman reports it, and
	// SunshineLatest is what LizardByte have published. Equal means there is
	// nothing to do; SunshineLatest empty means the lookup has not answered
	// yet or could not.
	Sunshine       string `json:"sunshine,omitempty"`
	SunshineLatest string `json:"sunshine_latest,omitempty"`

	// Packages is how many of the seat's distribution packages have a newer
	// version waiting, and PackageNames is the first few of them, for a line
	// that says which rather than only how many.
	Packages     int      `json:"packages"`
	PackageNames []string `json:"package_names,omitempty"`

	// Checked is when the seat was last asked. Zero until it has been, which
	// the interface says rather than drawing "up to date" for a seat nobody has
	// looked at: those are different claims and only one of them is earned.
	Checked time.Time `json:"checked"`

	// Problem is why the last look failed, empty when it did not. A seat that
	// is switched off cannot be asked, and that is the ordinary case rather
	// than a fault, so it lands here instead of in the seat's error.
	Problem string `json:"problem,omitempty"`
}

// Behind reports whether there is anything worth offering to install.
func (f Freshness) Behind() bool {
	return f.SunshineBehind() || f.Packages > 0
}

// SunshineBehind is the Sunshine half on its own.
//
// Both versions have to be known. An unknown latest is the offline case and an
// unknown installed is a seat that has never answered, and neither is evidence
// that the seat is behind: offering an update on the strength of a failed
// lookup is how somebody ends up rebuilding a seat that was already current.
func (f Freshness) SunshineBehind() bool {
	return f.Sunshine != "" && f.SunshineLatest != "" && f.Sunshine != f.SunshineLatest
}

// namesShown is how many package names travel to the interface. Enough to
// recognise what the update is, short of a list nobody reads: an Arch container
// left alone for a month has hundreds and the useful part of that is the count
// plus a sense of what kind of thing is in it.
const namesShown = 5

// sunshineCache holds the answer from GitHub between looks.
//
// One lookup serves every seat, because the question is about LizardByte's
// releases and not about any seat. Four seats asking separately would be four
// requests to somebody else's rate limit for one answer.
type sunshineCache struct {
	mu sync.Mutex

	// ask is the lookup itself, a field rather than the function called
	// directly so that a test can answer without reaching GitHub. Nil is the
	// real one, which is what every caller outside a test gets.
	ask func(ctx context.Context) (string, error)

	version string
	asked   time.Time
	err     error
}

// lookup is the real question, or whatever a test put in its place.
func (c *sunshineCache) lookup(ctx context.Context) (string, error) {
	if c.ask != nil {
		return c.ask(ctx)
	}

	_, version, err := sunshineRelease(ctx)

	return version, err
}

// sunshineAsk is how long an answer is reused. Sunshine releases every few
// weeks and the answer is a line in an interface nobody is waiting in front of,
// so this is set by what is polite rather than by what is fresh.
const sunshineAsk = 6 * time.Hour

// latest returns the published Sunshine version, asking GitHub at most once per
// sunshineAsk.
//
// A failure is cached alongside a success, on purpose and for the same
// interval. Without that, a machine with no route out asks GitHub once per seat
// per sweep forever, and the sweep runs every ten seconds.
func (c *sunshineCache) latest(ctx context.Context, now func() time.Time) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.asked.IsZero() && now().Sub(c.asked) < sunshineAsk {
		return c.version, c.err
	}

	version, err := c.lookup(ctx)

	c.asked = now()
	c.version = version
	c.err = err

	if err != nil {
		c.version = ""
	}

	return c.version, c.err
}

// forget drops the cached answer so that the next look asks again. For the
// button that means "check now", where waiting up to six hours for the thing
// somebody just asked for would read as the button not working.
func (c *sunshineCache) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.asked = time.Time{}
}

// installedSunshine reads the version pacman has recorded in the seat.
//
// pacman -Q prints "sunshine 2025.122.4-1", and the release names itself
// "v2025.122.4". Compared on the middle field with the decorations taken off
// both sides, because the two strings are written by different projects and
// neither is going to start matching the other.
func installedSunshine(out string) string {
	fields := strings.Fields(strings.TrimSpace(out))

	// The name is checked rather than assumed. pacman writes its errors to the
	// same place, and "error: package not found" has a second field too: taken
	// on position alone it yields the version "package", which compares unequal
	// to every real release and so reports a seat as behind forever.
	if len(fields) < 2 || fields[0] != "sunshine" {
		return ""
	}

	return normaliseVersion(fields[1])
}

// normaliseVersion strips what the two sides disagree about: a leading v from
// the git tag and a trailing -1 pkgrel from the package.
func normaliseVersion(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")

	if i := strings.LastIndex(s, "-"); i > 0 {
		s = s[:i]
	}

	return s
}

// pendingUpdates parses what pacman -Qu printed into a count and the first few
// names.
//
// Empty output is the ordinary answer for a seat with nothing waiting, and
// pacman exits 1 for it, which is why the caller reads this rather than the
// exit status.
func pendingUpdates(out string) (int, []string) {
	var names []string

	count := 0

	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		count++

		if len(names) < namesShown {
			names = append(names, fields[0])
		}
	}

	return count, names
}

// checkUpdatesScript counts pending packages without touching the seat's own
// package database.
//
// This is what pacman-contrib's checkupdates does, written out rather than
// installed, because adding a package to every seat to ask a question about
// packages is a poor trade. The whole point is the separate --dbpath: a plain
// `pacman -Sy` here would leave the seat with a sync database newer than its
// installed packages, which is the partial upgrade state that breaks an Arch
// system the next time anything is installed into it. The local database is
// linked rather than copied so that -Qu compares against what is really there,
// and the sync half is the only thing that gets refreshed.
//
// fakeroot is not needed the way it is for checkupdates run by a user: this
// runs as root inside the container.
const checkUpdatesScript = `
set -eu
db=/tmp/polyseat-checkupdates
rm -rf "$db"
mkdir -p "$db"
ln -s /var/lib/pacman/local "$db/local"
pacman -Sy --dbpath "$db" --logfile /dev/null >/dev/null 2>&1 || exit 3
pacman -Qu --dbpath "$db" 2>/dev/null || true
`

// freshInterval is how often a seat is asked what it is behind on.
//
// Slow on purpose. The answer changes when somebody else publishes something,
// which happens every few weeks, and the cost of asking is a pacman -Sy against
// the mirrors from inside every seat. Six hours matches what the daemon already
// does for its own releases, so a machine that is switched on for an evening
// asks once.
const freshInterval = 6 * time.Hour

// Freshness asks one seat what it is behind on.
//
// Read rather than remembered: the seat is the authority on what is installed
// in it, and a value cached here would go wrong the moment somebody updated
// something from inside the seat's own desktop, which they can.
func (m *Manager) Freshness(ctx context.Context, name string) Freshness {
	var f Freshness

	f.Checked = time.Now()

	// A seat that is not running cannot be asked. Said plainly rather than
	// reported as an error, because switched off is the normal state of a seat
	// nobody is playing in and an interface that shows a fault for it teaches
	// people to ignore the place faults appear.
	//
	// Read under the lock the way every other reader of this field does. It is
	// written by the sweep from a goroutine of its own.
	rt := m.runtimeOf(name)

	m.mu.Lock()
	state := rt.state
	m.mu.Unlock()

	if state != StateRunning {
		f.Problem = "the seat is not running, so what is installed in it cannot be read"

		return f
	}

	out, code, err := m.client.Try(ctx, name, "pacman", "-Q", "sunshine")
	switch {
	case err != nil:
		f.Problem = fmt.Sprintf("the seat could not be asked: %v", err)

		return f
	case code == 0:
		f.Sunshine = installedSunshine(out)
	}

	// The lookup and the seat are separate failures. GitHub being unreachable
	// says nothing about the packages waiting inside the container, so it must
	// not take the package half of the answer down with it.
	if latest, err := m.sunshine.latest(ctx, time.Now); err != nil {
		m.log.Debug("the published Sunshine version could not be looked up", "err", err)
	} else {
		f.SunshineLatest = latest
	}

	out, code, err = m.client.Try(ctx, name, "sh", "-c", checkUpdatesScript)

	switch {
	case err != nil:
		f.Problem = fmt.Sprintf("the seat could not be asked: %v", err)
	case code == 3:
		// The mirrors did not answer. Not the seat's fault and not worth a
		// fault on its card: it means the count is unknown for now, and the
		// count being unknown is what an empty Packages with a Problem says.
		f.Problem = "the package mirrors could not be reached from this seat"
	case code != 0:
		f.Problem = fmt.Sprintf("pacman answered %d when asked what is waiting", code)
	default:
		f.Packages, f.PackageNames = pendingUpdates(out)
	}

	return f
}

// Summary is the one line the interface shows for a seat that is behind.
func (f Freshness) Summary() string {
	var parts []string

	if f.SunshineBehind() {
		parts = append(parts, "Sunshine "+f.Sunshine+" to "+f.SunshineLatest)
	}

	if f.Packages == 1 {
		parts = append(parts, "1 package")
	} else if f.Packages > 1 {
		parts = append(parts, strconv.Itoa(f.Packages)+" packages")
	}

	return strings.Join(parts, ", ")
}

// ErrStreaming refuses work that would end somebody's game.
//
// A refusal rather than a wait, unlike the app list, which quietly does itself
// later. This one is a button somebody pressed: telling them "not now, somebody
// is playing" is an answer they can act on, and silently deferring an update
// they asked for by minutes or hours is not.
var ErrStreaming = errors.New("somebody is streaming from this seat")

// UpdateSoftware brings one seat's software up to what is published.
//
// The two halves of being behind, in the order that works. Packages first and
// Sunshine second, matching Steps(): Sunshine arrives as a downloaded package
// file and a system upgrade underneath it afterwards could pull its
// dependencies out from under the version just installed.
//
// These are the provisioning steps themselves rather than a second copy of what
// they do. A seat updated here therefore lands in the state a freshly
// provisioned seat would be in, and the NVIDIA collision that a plain pacman
// -Syu causes on an already built seat stays fixed in one place: see
// driverFlags, which stepPackages already carries and which was paid for the
// first time somebody provisioned a seat that existed.
func (m *Manager) UpdateSoftware(name string) error {
	return m.operate(name, "updating the seat's software", func(ctx context.Context) error {
		for _, busy := range m.Streaming() {
			if busy == name {
				return ErrStreaming
			}
		}

		rt := m.runtimeOf(name)

		m.mu.Lock()
		state := rt.state
		m.mu.Unlock()

		// pacman runs inside the container, so there has to be one running to
		// run it in. Starting the seat here instead would mean a button that
		// updates the software also switches a seat on, which is two things.
		if state != StateRunning {
			return fmt.Errorf("the seat is not running, so there is nothing to update software in")
		}

		seat, err := m.store.Get(name)
		if err != nil {
			return err
		}

		// The broker has nothing to do while the session is being restarted
		// underneath it, and startSession below brings it back.
		m.stopBroker(name)

		p := &Provisioner{
			GPU:    m.gpu,
			Client: m.client,
			Seat:   seat,
			Image:  m.cfg.Image,
			Log:    func(f string, a ...any) { m.logf(name, f, a...) },
		}

		if err := p.stepPackages(ctx); err != nil {
			return fmt.Errorf("packages: %w", err)
		}

		if err := p.stepSunshine(ctx); err != nil {
			return fmt.Errorf("sunshine: %w", err)
		}

		// The seat is now carrying a Sunshine, and possibly a compositor, that
		// the running session is not. Restarting is what makes the update the
		// person asked for actually be the thing that is running, and it is the
		// same path a provisioning run ends on.
		m.sunshine.forget()

		return m.startSession(ctx, name)
	})
}

// freshPatience bounds one seat's turn.
//
// A pacman -Sy that hangs on an unreachable mirror is the case this exists for.
// Without a bound it holds the whole pass, and the pass is what the interface's
// "Updates" rows come from, so one bad mirror would leave every other seat
// showing nothing with no reason given.
const freshPatience = 2 * time.Minute

// updateFreshness asks every running seat what it is behind on, one at a time.
//
// One at a time because each one pulls the mirror databases, and doing that in
// four containers at once on a machine meant to be playing games is a spike
// nobody asked for. It is on a six hour timer, so the pass having no hurry in
// it costs nothing.
//
// Seats that are busy or being played in are skipped rather than waited for.
// Unlike the app list there is nothing to catch up on afterwards: the next pass
// asks again, and a seat whose answer is six hours old is not a problem, it is
// the design.
func (m *Manager) updateFreshness(ctx context.Context) {
	seats, err := m.store.List()
	if err != nil {
		return
	}

	streaming := map[string]bool{}
	for _, name := range m.Streaming() {
		streaming[name] = true
	}

	for _, s := range seats {
		if err := ctx.Err(); err != nil {
			return
		}

		if streaming[s.Name] {
			continue
		}

		rt := m.runtimeOf(s.Name)

		m.mu.Lock()
		busy := rt.busy != ""
		m.mu.Unlock()

		if busy {
			continue
		}

		seatCtx, cancel := context.WithTimeout(ctx, freshPatience)
		f := m.Freshness(seatCtx, s.Name)

		cancel()

		m.mu.Lock()
		was := rt.fresh
		rt.fresh = f
		rt.freshChecked = time.Now()
		m.mu.Unlock()

		// Said once when it becomes true rather than on every pass, so that a
		// seat nobody has updated does not write the same line into its log
		// four times a day for as long as it stays behind.
		if f.Behind() && f.Summary() != was.Summary() {
			m.logf(s.Name, "this seat is behind: %s", f.Summary())
		}
	}

	m.notify()
}

// CheckFreshness asks one seat now, instead of waiting for the six hour timer.
//
// The timer is right for noticing and wrong for answering a question somebody
// is currently asking. Without this the only way to find out whether a seat has
// anything waiting is to wait up to six hours for the pass, and somebody who
// has just seen a Sunshine release announced has no way to act on it.
//
// The cached GitHub answer is dropped first, because "now" has to mean the
// whole question. Reusing an answer from five hours ago would make the button
// re-read the seat and then compare it against a version that may itself be
// stale, which is the one case where pressing it twice gives two different
// results for reasons nothing on the page explains.
//
// Not wrapped in operate, unlike updating: this changes nothing. It reads the
// seat's package database through a copy and its Sunshine version through
// pacman -Q, so there is nothing here that another operation could interleave
// badly with. What it must not do is run against a seat in the middle of being
// built, and that is what the refusal below is for.
func (m *Manager) CheckFreshness(name string) (Freshness, error) {
	rt := m.runtimeOf(name)

	m.mu.Lock()
	busy := rt.busy
	m.mu.Unlock()

	if busy != "" {
		return Freshness{}, ErrBusy
	}

	ctx, cancel := context.WithTimeout(context.Background(), freshPatience)
	defer cancel()

	m.sunshine.forget()

	f := m.Freshness(ctx, name)

	m.mu.Lock()
	rt.fresh = f
	rt.freshChecked = time.Now()
	m.mu.Unlock()

	m.notify()

	return f, nil
}
