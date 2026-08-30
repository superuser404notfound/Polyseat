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

# A directory of this run's own, and not the one fixed name this used to have.
#
# Two things start this script and neither knows about the other: the six hour
# pass, and somebody pressing Check for updates. With one path between them, the
# rm -rf at the top of the second run deletes the sync database the first run is
# in the middle of writing, pacman stops on a file that vanished under it, and
# the seat reports that the mirrors could not be reached. The mirrors were fine.
# That is what makes it worth a comment: the failure lands on the network, which
# is the one thing here nobody can reproduce by hand afterwards, because running
# the command yourself never collides with anything.
db=$(mktemp -d "${TMPDIR:-/tmp}/polyseat-checkupdates.XXXXXX")
trap 'rm -rf "$db"' EXIT

# mktemp makes it 0700 and pacman cannot work in that.
#
# Since 6.1 the download itself runs as another user, alpm, in a sandbox, and
# Arch ships DownloadUser set. That user has to reach the file pacman opened for
# it underneath this directory, and a root owned 0700 keeps it out:
#
#     error: could not open file .../sync/download-XXXX/core.db.part: Permission denied
#     error: failed to setup a download payload for core.db
#
# 0755 is what pacman's own /var/lib/pacman/sync has, for the same reason. There
# is nothing private in here: it is the public package databases, in a container
# whose only other account is the player.
chmod 0755 "$db"

ln -s /var/lib/pacman/local "$db/local"

status=0
err=$(timeout ` + syncPatience + ` pacman -Sy --dbpath "$db" --logfile /dev/null 2>&1 >/dev/null) || status=$?

if [ "$status" -eq 124 ]; then
	echo "no mirror answered within ` + syncPatience + ` seconds"
	exit 3
fi

if [ "$status" -ne 0 ]; then
	# The first line about a file, and only the summary when there is none.
	#
	# pacman ends a failed sync with "failed to synchronize all databases
	# (failed to retrieve some files)", which is the last line and says nothing
	# anybody can act on. What it was about is above it, one line per file:
	# which database, which mirror, and what the transfer did. Reporting the
	# tail of this was measured to be useless on the first machine it ran on.
	reason=$(echo "$err" | grep -m1 "failed retrieving" || true)

	if [ -z "$reason" ]; then
		reason=$(echo "$err" | grep -v "^[[:space:]]*$" | tail -1)
	fi

	echo "$reason"
	exit 3
fi

pacman -Qu --dbpath "$db" 2>/dev/null || true
`

// syncPatience bounds the sync inside the seat.
//
// Because there is no bound in pacman worth relying on here. It tries every
// mirror in the list in turn and each connection has its own timeout, so a seat
// whose way out is broken rather than absent spends minutes finding that out,
// and the button in the interface waits for all of it: pressing Check for
// updates on such a seat looked like a button that had hung.
//
// Forty five seconds because a working sync of the four databases is seconds,
// on a slow link still well under half of this, and because the whole look is
// bounded at two minutes and the Sunshine half has to fit in it as well.
const syncPatience = "45"

// record stores what a look found, and is the only thing that does.
//
// One place because there are three moments that take a look — the six hour
// pass, the button, and the end of an update — and the third was added last and
// forgotten. The seat finished updating, the session came back, and the card
// went on listing the versions that had just been installed as waiting, because
// the reading it draws from was the one taken before the work.
//
// Anything that asks a seat has to come through here, so that the answer and
// the time it was taken are written together and neither can be left behind.
func (m *Manager) record(name string, f Freshness) Freshness {
	rt := m.runtimeOf(name)

	m.mu.Lock()
	was := rt.fresh
	rt.fresh = f
	rt.freshChecked = time.Now()
	m.mu.Unlock()

	// Said once when it becomes true rather than on every look, so that a seat
	// nobody has updated does not write the same line into its log four times a
	// day for as long as it stays behind.
	if f.Behind() && f.Summary() != was.Summary() {
		m.logf(name, "this seat is behind: %s", f.Summary())
	}

	return f
}

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
// ErrNotRunning is the refusal for a question only a running seat can answer.
//
// An error rather than a Problem written into the reading, and the difference
// is the whole of a bug this had. "The seat is not running" is a fact about the
// seat's state right now, not something a look found out, and a fact about the
// present goes out of date the moment the present moves: stored, it survived
// the seat being started and the card went on saying the seat was off while it
// was running. What describes the current state has to be read from the current
// state, so this never becomes a stored answer at all.
var ErrNotRunning = errors.New("the seat is not running, so what is installed in it cannot be read")

// running reports whether the seat is up enough to be asked anything.
func (m *Manager) running(name string) bool {
	rt := m.runtimeOf(name)

	// Read under the lock the way every other reader of this field does. It is
	// written by the sweep from a goroutine of its own.
	m.mu.Lock()
	defer m.mu.Unlock()

	return rt.state == StateRunning
}

func (m *Manager) Freshness(ctx context.Context, name string) Freshness {
	var f Freshness

	// Set here rather than on the way out, so that a look which ends in a
	// Problem still says when it was taken. Only an answer is ever recorded,
	// and this is what makes it one.
	f.Checked = time.Now()

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
		// The sync failed. Not the seat's fault and not worth a fault on its
		// card: it means the count is unknown for now, and the count being
		// unknown is what an empty Packages with a Problem says.
		f.Problem = syncProblem(out)
	case code != 0:
		f.Problem = fmt.Sprintf("pacman answered %d when asked what is waiting", code)
	default:
		f.Packages, f.PackageNames = pendingUpdates(out)
	}

	return f
}

// syncProblem is what the seat said, rather than what it was assumed to mean.
//
// This line used to be the sentence "the package mirrors could not be reached
// from this seat", written whatever had happened, because the script sent
// pacman's own complaint to /dev/null. Everything that can go wrong there came
// out as one guess about the network: a mirror really being unreachable, but
// also a name that does not resolve, a signature that will not check, a full
// disk, or /tmp being read only. It read as a measurement and was not one, and
// on a machine where it appeared on every seat it said nothing about which of
// those to go and look at.
func syncProblem(out string) string {
	said := strings.TrimSpace(lastLines(out, 1))

	// Keeping the old sentence for the case it was always right about: pacman
	// said nothing at all, so there is nothing to report but the assumption.
	if said == "" {
		return "the package mirrors could not be reached from this seat"
	}

	return "the package list could not be refreshed: " + said
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

		if err := m.startSession(ctx, name); err != nil {
			return err
		}

		// And read the seat again, because nothing else was going to.
		//
		// rt.fresh is what the card shows, and it was written by whichever look
		// happened last. Without this that is the look from *before* the
		// update: the seat finishes, the session comes back, and the row still
		// lists the versions that were just installed as waiting, until the six
		// hour timer or somebody pressing Check for updates replaces it. The
		// work was done and the interface went on saying it was not.
		//
		// Read rather than assumed. Writing an empty Freshness here would be
		// quicker and would be this code claiming the update did what it set
		// out to do; asking the seat is how a package that did not upgrade, or
		// one published in the minutes the update took, still gets reported.
		//
		// Freshness does not consult busy, which is what makes this possible at
		// all: this still runs inside operate, so CheckFreshness would refuse
		// itself here.
		m.record(name, m.Freshness(ctx, name))

		return nil
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
		checked := rt.freshChecked
		m.mu.Unlock()

		// A seat that is off keeps whatever was last found in it. That reading
		// is old rather than wrong, and the card says how old; replacing it
		// with a note about the seat being off would be storing a fact about
		// the present, which is exactly what goes stale.
		if busy || !m.running(s.Name) {
			continue
		}

		// Never asked, or asked long enough ago. The first is what makes a seat
		// that has just come up get looked at once rather than waiting out the
		// rest of a six hour interval it was switched off for.
		if !checked.IsZero() && time.Since(checked) < freshInterval {
			continue
		}

		seatCtx, cancel := context.WithTimeout(ctx, freshPatience)
		m.record(s.Name, m.Freshness(seatCtx, s.Name))

		cancel()
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

	// Answered now rather than written down. Somebody who presses this on a
	// seat that is switched off gets told so, and nothing is stored that would
	// still be saying it after they start the seat.
	if !m.running(name) {
		return Freshness{}, ErrNotRunning
	}

	ctx, cancel := context.WithTimeout(context.Background(), freshPatience)
	defer cancel()

	m.sunshine.forget()

	f := m.record(name, m.Freshness(ctx, name))

	m.notify()

	return f, nil
}

// freshenSoon runs a pass in the background, and at most one at a time.
//
// The sweep is what notices that a seat has come up without ever having been
// asked, and the sweep runs every ten seconds and must not do network work.
// This is the join between the two: it hands the pass to a goroutine and
// refuses to start a second, so a seat that stays unasked because it is being
// streamed from does not accumulate one pass per sweep.
//
// The pass decides for itself which seats are due, which is what makes calling
// this often harmless: a seat with a current reading is skipped without
// anything being asked of it.
func (m *Manager) freshenSoon(ctx context.Context) {
	m.mu.Lock()

	if m.freshening {
		m.mu.Unlock()

		return
	}

	m.freshening = true
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			m.freshening = false
			m.mu.Unlock()
		}()

		m.updateFreshness(ctx)
	}()
}
