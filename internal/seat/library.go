package seat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/superuser404notfound/Polyseat/internal/library"
)

// syncInterval is how often the daemon looks for newly installed games.
//
// Slow on purpose. Installing a game takes minutes and the check is a directory
// scan, so nothing is gained by looking more often, while every pass writes
// into libraries that people may be playing out of. A minute is short enough
// that a game appears in the other seats while somebody is still deciding what
// to play.
const syncInterval = time.Minute

// ErrNoLibrary is returned by the library operations when the pool could not be
// opened, which on a filesystem without reflinks is the normal answer.
var ErrNoLibrary = errors.New("the shared library is not available")

// LibraryStatus is what the interface shows about the pool as a whole.
type LibraryStatus struct {
	// Available is false when the pool could not be opened, with Problem
	// saying why. The interface shows the reason rather than hiding the
	// feature, because "my games are not being shared" is otherwise a silent
	// failure.
	Available bool   `json:"available"`
	Problem   string `json:"problem,omitempty"`

	Root string `json:"root,omitempty"`

	// Candidates are Steam libraries found on the host that are not the pool
	// and not already tracked, offered so that adding one is a click rather
	// than a path somebody has to remember and type correctly.
	Candidates []string `json:"candidates"`

	// Sources are the libraries outside the seats that the pool takes games
	// from on every pass.
	Sources []string `json:"sources"`

	// Outside names the seats that exist and are not taking part.
	//
	// Reported rather than left out, because taking part is a per seat setting
	// and the absence of a seat from the pool otherwise looks exactly like the
	// pool being broken. It cost somebody a provisioning run to find that out
	// once already.
	Outside []string `json:"outside"`

	library.Inventory
}

// steamLibraries looks for Steam libraries belonging to people with accounts on
// the host.
//
// Only the two standard locations under each home directory. Searching the
// whole disk for a directory called steamapps would find the seats' own
// libraries and every backup anybody ever made, and offering those as import
// candidates would be worse than offering nothing.
func steamLibraries(exclude string, tracked []string) []string {
	homes, err := filepath.Glob("/home/*")
	if err != nil {
		return nil
	}

	homes = append(homes, "/root")

	var out []string

	for _, home := range homes {
		for _, suffix := range []string{
			".local/share/Steam/steamapps",
			".steam/steam/steamapps",
		} {
			path := filepath.Join(home, suffix)

			if strings.HasPrefix(path, exclude) {
				continue
			}

			// The common directory rather than the manifests, because a library
			// Steam has registered but never downloaded into has manifests and
			// nothing worth importing.
			if _, err := os.Stat(filepath.Join(path, "common")); err != nil {
				continue
			}

			// The two locations are usually the same directory reached through a
			// symlink, so the resolved path is what decides whether it is a
			// duplicate.
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				resolved = path
			}

			// A library already being tracked is not a candidate: offering it
			// again would suggest there is something left to do.
			if !slices.Contains(out, resolved) && !slices.Contains(tracked, resolved) {
				out = append(out, resolved)
			}
		}
	}

	sort.Strings(out)

	return out
}

// openLibrary prepares the pool, treating an unusable filesystem as a missing
// feature rather than as a reason not to start.
//
// A daemon that refused to come up because the games directory is on ext4 would
// take every seat down with it over something none of them need in order to
// stream.
func (m *Manager) openLibrary() {
	pool, err := library.Open(m.cfg.LibraryDir)
	if err != nil {
		m.libraryErr = err.Error()

		if errors.Is(err, library.ErrNoReflink) {
			m.log.Warn("the shared library is off: this filesystem cannot share "+
				"blocks between files, so pooling games would copy every byte per seat",
				"dir", m.cfg.LibraryDir, "err", err)
		} else {
			m.log.Warn("the shared library could not be opened",
				"dir", m.cfg.LibraryDir, "err", err)
		}

		return
	}

	m.pool = pool

	m.log.Info("shared library ready", "dir", m.cfg.LibraryDir)
}

// members lists the seats taking part, with the host ownership their files need.
//
// Works on seats that are switched off, which is the point of doing all of this
// on the host filesystem: a game installed in one seat reaches the others
// whether or not anybody is sitting at them. A seat with no container yet is
// skipped, since there is no mapping to read and nothing to share.
func (m *Manager) members() []library.Member {
	seats, err := m.store.List()
	if err != nil {
		return nil
	}

	var out []library.Member

	for _, s := range seats {
		if !s.Library {
			continue
		}

		// No uid recorded means the seat has not been provisioned by a
		// generation that records one, and it therefore has no mount either.
		// Provisioning is what enrols a seat, and the interface already marks
		// such a seat as out of date.
		//
		// This deliberately does not fall back to the runtime's uid. That field
		// defaults to 1000 rather than to zero, so a check for zero against it
		// never fires and every seat would silently be assumed to use uid 1000.
		// It happens to be right on every seat here, which is exactly what
		// makes it the kind of assumption worth removing rather than keeping.
		if s.PlayerUID == 0 {
			continue
		}

		hostUID, hostGID, err := m.client.MapID(s.Name, s.PlayerUID, s.PlayerUID)
		if err != nil {
			continue
		}

		out = append(out, library.Member{
			Name:      s.Name,
			Owner:     library.Owner{UID: int(hostUID), GID: int(hostGID)},
			Updatable: m.libraryIdle(s.Name),
		})
	}

	return out
}

// idleProbe answers whether anything inside the seat is using the shared
// library right now.
//
// Written as a shell fragment rather than as a command, because the tools that
// would answer this directly, lsof or fuser, are not in a seat and installing
// them for one question is not worth it. /proc is, and it is authoritative:
// a game that is running has its executable and its data mapped, and a Steam
// that is verifying or patching has the files open.
//
// Exits 0 when nothing is using it, which is the answer that permits an update.
// Both paths, because the pool is mounted twice: once as Steam's own library
// folder, which is where a running game has its files open, and once as the
// launcher agnostic directory. A probe that only knew the second one would call
// a seat idle while a Steam game was running out of it and replace the files
// underneath it.
//
// Three commands rather than a loop over /proc, and that is the whole point of
// the shape below. The loop this replaces forked a grep per process and a
// readlink per open file: on an idle seat with 58 processes and 1369 open files
// that was around 1500 forks and a measured 990 ms of the seat's own CPU, once
// a minute, for a question that is almost always answered "no". A seat with a
// game running has far more of both, and it spent that second while somebody
// was streaming out of it. The form here asks the same three questions in three
// processes: 14 ms, measured on the same seat.
//
// grep takes every maps file in one go and -q stops it at the first hit. find
// matches the target of a symlink with -lname without following it and without
// a process per link, and -quit stops at the first one, which is why the two
// patterns are asked separately rather than joined.
const idleProbe = `
grep -qE '` + LibraryMount + `|` + steamApps + `' /proc/[0-9]*/maps 2>/dev/null && exit 1
for p in '` + LibraryMount + `/*' '` + steamApps + `/*'; do
	[ -n "$(find /proc/[0-9]*/fd /proc/[0-9]*/cwd -lname "$p" -print -quit 2>/dev/null)" ] && exit 1
done
exit 0
`

// libraryIdle reports whether the seat's copies may be replaced right now.
//
// The rule is deliberately about the files rather than about the seat. A seat
// with somebody sitting in it browsing a store page is perfectly safe to update;
// a seat with a game running out of the shared library is not, and replacing
// those files would corrupt an install rather than improve one.
//
// A container that is not running is trivially safe, and that is also the only
// case this can answer without asking the container, so it is checked first.
// Anything uncertain answers no: leaving a seat one patch behind for another
// minute costs nothing, and the interface says an update is waiting.
func (m *Manager) libraryIdle(name string) bool {
	status, err := m.client.Status(name)
	if err != nil {
		return false
	}

	if status == "Stopped" {
		return true
	}

	if status != "Running" {
		// Starting or stopping. Nothing may talk to a container in those
		// states, see the warning on incusx.Exec.
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, code, err := m.client.Try(ctx, name, "sh", "-c", idleProbe)
	if err != nil {
		return false
	}

	return code == 0
}

// applyLibrary attaches or detaches a seat's share of the library without a
// full provisioning run.
//
// The same step the provisioner runs, reached from the other side. A seat with
// no container yet is left alone: there is nothing to attach a device to, and
// provisioning will do it when the container exists.
func (m *Manager) applyLibrary(ctx context.Context, s Seat) error {
	status, err := m.client.Status(s.Name)
	if err != nil {
		return err
	}

	if status == "" {
		return nil
	}

	// Without a recorded uid there is nothing to own the files with, and
	// learning it means running a command inside a container that may be off.
	// Provisioning records it, so this only affects a seat that has never been
	// built by a generation that does.
	if s.PlayerUID == 0 && status != "Running" {
		return fmt.Errorf("this seat has no recorded uid yet, provision it once")
	}

	p := &Provisioner{
		Client:  m.client,
		Seat:    s,
		Image:   m.cfg.Image,
		Library: m.pool,
		Log:     func(f string, a ...any) { m.logf(s.Name, f, a...) },
		uid:     s.PlayerUID,
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	if err := p.stepLibrary(ctx); err != nil {
		return err
	}

	// Cloning into the new member straight away, so the games are there by the
	// time somebody looks rather than up to a minute later.
	go m.syncLibrary(context.Background())

	return nil
}

// outside lists the seats that are not taking part in the library.
func (m *Manager) outside() []string {
	seats, err := m.store.List()
	if err != nil {
		return nil
	}

	var out []string

	for _, s := range seats {
		if !s.Library {
			out = append(out, s.Name)
		}
	}

	return out
}

// syncLibrary runs one pass and reports anything it did.
func (m *Manager) syncLibrary(ctx context.Context) {
	if m.pool == nil {
		return
	}

	members := m.members()
	if len(members) == 0 {
		return
	}

	// Serialised against the interface's own buttons, so an import started by
	// hand and the timer cannot both be cloning into the same seat.
	m.syncMu.Lock()
	defer m.syncMu.Unlock()

	if err := ctx.Err(); err != nil {
		return
	}

	report, err := m.pool.Sync(members, func(f string, a ...any) {
		m.log.Info("library: " + fmt.Sprintf(f, a...))
	})
	if err != nil {
		m.log.Error("the library sync failed", "err", err)

		return
	}

	for _, problem := range report.Problems {
		m.log.Warn("library", "problem", problem)
	}

	if report.Quiet() {
		return
	}

	// Written into each affected seat's own log, because that is where somebody
	// looking at a seat will go to ask why a game turned up in it.
	for _, move := range report.Harvested {
		m.logf(move.Seat, "%s went into the shared library", move.Name)
	}

	for _, move := range report.Delivered {
		m.logf(move.Seat, "%s came from the shared library", move.Name)
	}

	for _, move := range report.Declined {
		m.logf(move.Seat, "%s was uninstalled here, the library will not offer it again", move.Name)
	}

	for _, move := range report.Pending {
		m.logf(move.Seat, "an update to %s is waiting, the seat is using the shared library right now", move.Name)
	}

	m.notify()
}

// ------------------------------------------------------------------ interface

// Library reports the pool.
func (m *Manager) Library() LibraryStatus {
	if m.pool == nil {
		return LibraryStatus{Available: false, Problem: m.libraryErr}
	}

	inv, err := m.pool.Inventory(m.members())
	if err != nil {
		return LibraryStatus{Available: false, Problem: err.Error()}
	}

	sources := m.pool.Sources()

	return LibraryStatus{
		Available:  true,
		Root:       m.pool.Root(),
		Candidates: steamLibraries(m.pool.Root(), sources),
		Sources:    sources,
		Outside:    m.outside(),
		Inventory:  inv,
	}
}

// SyncLibrary runs a pass now rather than waiting for the timer.
func (m *Manager) SyncLibrary(ctx context.Context) error {
	if m.pool == nil {
		return fmt.Errorf("%w: %s", ErrNoLibrary, m.libraryErr)
	}

	m.syncLibrary(ctx)

	return nil
}

// ImportLibrary starts tracking a Steam library outside the seats, which is how
// the games already on the host get in and stay in step afterwards.
func (m *Manager) ImportLibrary(ctx context.Context, steamapps string) (library.Report, error) {
	if m.pool == nil {
		return library.Report{}, fmt.Errorf("%w: %s", ErrNoLibrary, m.libraryErr)
	}

	m.syncMu.Lock()

	report, err := m.pool.AddSource(steamapps, func(f string, a ...any) {
		m.log.Info("library: " + fmt.Sprintf(f, a...))
	})

	m.syncMu.Unlock()

	if err != nil {
		return report, err
	}

	// Straight on to a sync, so an import lands in the seats in one action
	// rather than leaving somebody watching an inventory that says the games
	// are in the pool and nowhere else.
	m.syncLibrary(ctx)

	return report, nil
}

// RemoveFromLibrary drops a title from the pool without touching the seats that
// already have it.
func (m *Manager) RemoveFromLibrary(appID string) error {
	if m.pool == nil {
		return fmt.Errorf("%w: %s", ErrNoLibrary, m.libraryErr)
	}

	m.syncMu.Lock()
	defer m.syncMu.Unlock()

	return m.pool.Remove(appID)
}

// OfferToSeat clears a seat's refusal so the next pass hands the title over
// again. The way back from an uninstall.
func (m *Manager) OfferToSeat(ctx context.Context, seat, appID string) error {
	if m.pool == nil {
		return fmt.Errorf("%w: %s", ErrNoLibrary, m.libraryErr)
	}

	if _, err := m.store.Get(seat); err != nil {
		return err
	}

	m.syncMu.Lock()
	err := m.pool.Offer(seat, appID)
	m.syncMu.Unlock()

	if err != nil {
		return err
	}

	m.syncLibrary(ctx)

	return nil
}
