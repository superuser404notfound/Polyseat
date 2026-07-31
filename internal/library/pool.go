package library

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Layout on the host, under the directory named in the configuration:
//
//	<root>/pool/steamapps/            the canonical copy of every shared title
//	<root>/seats/<seat>/steamapps/    one private library per seat, bind mounted
//	<root>/state.json                 what has been handed to whom
//
// The seat directories are ordinary Steam libraries with nothing special about
// them. That is the point: Steam is never asked to cope with an arrangement it
// does not already understand.
const (
	poolDir   = "pool"
	seatsDir  = "seats"
	steamApps = "steamapps"
	commonDir = "common"
	sharedDir = "shared"
	stateFile = "state.json"
)

// folderKey namespaces a shared folder in the per seat bookkeeping, which is
// otherwise keyed by Steam app id. Without it a folder somebody called "440"
// would share an entry with Team Fortress 2.
func folderKey(name string) string { return "folder:" + name }

// settleTime is how long an app has to have been untouched before it is taken
// into the pool.
//
// Steam sets StateFlags to 4 the moment the last byte is verified, and then
// keeps working: moving files out of downloading, writing the depot list,
// running the installer scripts some titles ship. Cloning during that window
// captures a tree that is complete according to the manifest and not according
// to the disk.
const settleTime = 2 * time.Minute

// Logger receives a line per thing done.
type Logger func(format string, args ...any)

// Member is one library the pool keeps in step: a name, the host ownership its
// files need, and whether its files may be replaced right now.
//
// Usually a seat. It can also be the host's own Steam library, which is the
// same thing seen from the other side: a directory holding installed games that
// somebody plays out of. The only differences are where it lives and who owns
// it, and both are fields.
type Member struct {
	Name  string
	Owner Owner

	// Updatable says the member's copies can safely be replaced with a newer
	// build from the pool. The caller decides what safe means, because it is
	// the one that knows whether a container is running and whether somebody is
	// playing out of that directory; overwriting a game under a running Steam
	// corrupts an install rather than improving one.
	//
	// A member that is not updatable is left alone and the pending update is
	// reported, so it shows up as something waiting rather than as nothing
	// happening.
	Updatable bool

	// Apps is where the member's Steam library lives on the host. Empty means
	// the seat layout under the pool root, which is where every seat's is.
	//
	// A member that sets it is external: the daemon did not create the
	// directory, does not own it and never chowns it. It is somebody's real
	// Steam library and the only thing that happens to it is that titles are
	// cloned in.
	Apps string

	// Folders is the launcher agnostic directory. Only read for an external
	// member, and empty there means it does not take part in that side at all.
	// A seat gets one from the provisioner; the host has no equivalent place,
	// so it shares Steam titles and nothing else.
	Folders string
}

// External reports whether this member's library lives outside the pool root,
// which is to say whether it belongs to somebody other than the daemon.
func (m Member) External() bool { return m.Apps != "" }

// Pool is the shared library on disk.
type Pool struct {
	root string

	// own is the ownership the pool's own files get. Taken from the running
	// process rather than written as root, which is the same thing when the
	// daemon runs it and the reason this package can be tested by a normal
	// user.
	own Owner

	// Settle overrides how long an install has to be quiet before it is taken
	// into the pool. Only tests set this, which would otherwise have to wait
	// out settleTime to reach the code that matters.
	Settle time.Duration

	mu    sync.Mutex
	state state
}

// state records what the daemon has handed to which seat.
//
// Without it the sync loop has no way to tell "this seat has never been given
// the game" from "this seat was given the game and somebody uninstalled it",
// and would helpfully clone it straight back every time. Somebody clearing
// space in their own seat and finding the game restored a minute later would
// be right to call that broken.
type state struct {
	// Delivered maps seat name to app id to status, one of "delivered" or
	// "declined".
	Delivered map[string]map[string]string `json:"delivered"`

	// Folders records the version of each shared folder in the pool, since a
	// plain directory carries no manifest to read one from.
	Folders map[string]Folder `json:"folders"`

	// Sources are Steam libraries outside the seats that the pool takes games
	// from, normally the one belonging to whoever uses this machine directly.
	//
	// Remembered rather than imported once. A one time import means a game the
	// host updates afterwards never reaches the seats, which is the same drift
	// this pool exists to avoid. They are read only in both senses: the daemon
	// harvests from them and never writes into them, and a game uninstalled
	// there is not uninstalled anywhere else.
	Sources []string `json:"sources"`
}

const (
	statusDelivered = "delivered"
	statusDeclined  = "declined"
)

// Open prepares the library, refusing to work where cloning is impossible.
func Open(root string) (*Pool, error) {
	if root == "" {
		return nil, fmt.Errorf("no library directory configured")
	}

	if err := os.MkdirAll(filepath.Join(root, poolDir, steamApps, commonDir), 0o755); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Join(root, poolDir, sharedDir), 0o755); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Join(root, seatsDir), 0o755); err != nil {
		return nil, err
	}

	if err := SupportsReflink(root); err != nil {
		return nil, err
	}

	p := &Pool{
		root:   root,
		own:    Owner{UID: os.Geteuid(), GID: os.Getegid()},
		Settle: settleTime,
	}

	if err := p.load(); err != nil {
		return nil, err
	}

	return p, nil
}

// Root is where the library lives.
func (p *Pool) Root() string { return p.root }

// PoolApps is the canonical library's steamapps directory.
func (p *Pool) PoolApps() string {
	return filepath.Join(p.root, poolDir, steamApps)
}

// SeatRoot is the host directory bind mounted into a seat. What the seat sees
// is a Steam library folder, so it is this directory that gets mounted and not
// the steamapps below it.
func (p *Pool) SeatRoot(seat string) string {
	return filepath.Join(p.root, seatsDir, seat)
}

// SeatApps is a seat's steamapps directory on the host.
func (p *Pool) SeatApps(seat string) string {
	return filepath.Join(p.SeatRoot(seat), steamApps)
}

// PoolFolders is where the canonical copy of each launcher agnostic folder
// lives, and SeatFolders is a seat's own.
func (p *Pool) PoolFolders() string {
	return filepath.Join(p.root, poolDir, sharedDir)
}

// SeatFolders is a seat's shared folder directory on the host.
func (p *Pool) SeatFolders(seat string) string {
	return filepath.Join(p.SeatRoot(seat), sharedDir)
}

// apps and folders are where a member's copies actually live: under the pool
// root for a seat, wherever its owner put it for an external member.
//
// folders comes back empty for an external member that named none, which is the
// signal that it takes part in the Steam side only.
func (p *Pool) apps(m Member) string {
	if m.External() {
		return m.Apps
	}

	return p.SeatApps(m.Name)
}

func (p *Pool) folders(m Member) string {
	if m.External() {
		return m.Folders
	}

	return p.SeatFolders(m.Name)
}

// Ensure creates a seat's library with the ownership the container needs.
//
// Does nothing for an external member. That directory already exists, it is not
// the daemon's, and creating or chowning parts of somebody's own Steam library
// is not a thing to do on the way to copying a game into it.
func (p *Pool) Ensure(m Member) error {
	if m.External() {
		return nil
	}

	for _, dir := range []string{
		p.SeatRoot(m.Name),
		p.SeatApps(m.Name),
		filepath.Join(p.SeatApps(m.Name), commonDir),
		p.SeatFolders(m.Name),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}

		if err := os.Chown(dir, m.Owner.UID, m.Owner.GID); err != nil {
			return err
		}
	}

	return nil
}

// ------------------------------------------------------------------- syncing

// Report says what one sync pass did.
type Report struct {
	Harvested []Move   `json:"harvested"`
	Delivered []Move   `json:"delivered"`
	Declined  []Move   `json:"declined"`
	Problems  []string `json:"problems"`

	// Pending are updates that are ready in the pool and waiting for a seat to
	// be in a state where replacing its files is safe.
	Pending []Move `json:"pending"`
}

// Move is one title going one way.
type Move struct {
	App   string `json:"app"`
	Name  string `json:"name"`
	Seat  string `json:"seat"`
	Bytes int64  `json:"bytes"`
}

// Empty reports whether the pass changed nothing, which is the normal case and
// therefore the one not worth logging.
func (r Report) Empty() bool {
	return len(r.Harvested) == 0 && len(r.Delivered) == 0 && len(r.Declined) == 0
}

// Quiet reports whether there is nothing at all to say, including nothing
// waiting. Empty deliberately ignores Pending, because an update that is
// waiting is not a change and repeating it every minute would be noise.
func (r Report) Quiet() bool {
	return r.Empty() && len(r.Pending) == 0
}

// Sync brings the pool and the seats into agreement: anything newly installed
// in a seat is taken into the pool, and anything in the pool is offered to
// every seat that has not already turned it down.
//
// Never deletes. A title removed from the pool by hand stays gone from the
// pool but is left alone in the seats that already have it, and a title
// uninstalled inside a seat is remembered as declined rather than restored.
// Both directions of that are deliberate: this daemon writes into libraries
// that people are playing out of, and the worst thing it could do is make a
// decision about somebody's disk that they did not ask for.
func (p *Pool) Sync(members []Member, log Logger) (Report, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if log == nil {
		log = func(string, ...any) {}
	}

	var report Report

	// A tracked library that the caller also passed as a member is harvested
	// below along with everything else, and doing it here as well would read the
	// same directory twice per pass for no gain.
	joined := map[string]bool{}

	for _, m := range members {
		if m.External() {
			joined[m.Apps] = true
		}
	}

	// Outside libraries first, so a game the host updated is already in the
	// pool by the time the seats are looked at and reaches them in this pass
	// rather than the next.
	for _, source := range p.state.Sources {
		if joined[source] {
			continue
		}

		if err := p.harvestFrom(source, "host", p.own, &report, log); err != nil {
			report.Problems = append(report.Problems, fmt.Sprintf("%s: %v", source, err))
		}
	}

	for _, m := range members {
		if err := p.Ensure(m); err != nil {
			return report, err
		}

		if err := p.harvest(m, &report, log); err != nil {
			report.Problems = append(report.Problems, fmt.Sprintf("%s: %v", m.Name, err))
		}
	}

	// Distribution reads the pool once, after every seat has contributed, so a
	// game installed in one seat reaches the others in the same pass rather
	// than waiting for the next one.
	pool, err := ReadApps(p.PoolApps())
	if err != nil {
		return report, err
	}

	for _, m := range members {
		if err := p.harvestFolders(m, &report, log); err != nil {
			report.Problems = append(report.Problems, fmt.Sprintf("%s: %v", m.Name, err))
		}
	}

	for _, m := range members {
		if err := p.distribute(m, pool, &report, log); err != nil {
			report.Problems = append(report.Problems, fmt.Sprintf("%s: %v", m.Name, err))
		}

		if err := p.distributeFolders(m, &report, log); err != nil {
			report.Problems = append(report.Problems, fmt.Sprintf("%s: %v", m.Name, err))
		}
	}

	if err := p.save(); err != nil {
		return report, err
	}

	return report, nil
}

// harvest takes finished installs out of a member's library and into the pool.
func (p *Pool) harvest(m Member, report *Report, log Logger) error {
	apps, err := ReadApps(p.apps(m))
	if err != nil {
		return err
	}

	// Everything present is recorded as delivered, which is what later lets an
	// absence be read as somebody having uninstalled it.
	for _, app := range apps {
		p.mark(m.Name, app.AppID, statusDelivered)
	}

	return p.harvestFrom(p.apps(m), m.Name, p.own, report, log)
}

// harvestFrom takes finished installs out of any Steam library into the pool.
//
// Shared between the seats and the outside libraries the pool tracks, because
// the two are the same operation seen from different sides: read a library,
// take what is complete and newer than what the pool holds, never write back.
// The from label is only for the log and the report.
func (p *Pool) harvestFrom(steamapps, from string, owner Owner, report *Report, log Logger) error {
	apps, err := ReadApps(steamapps)
	if err != nil {
		return err
	}

	for _, app := range apps {
		if !app.Installed() {
			continue
		}

		if !settled(app.Manifest, p.Settle) {
			continue
		}

		// Only ever forward. The pool takes a copy when it has none, or when
		// the library carries a strictly newer build; one that is a patch
		// behind must not overwrite the pool and hand that older build to
		// everybody else.
		have, err := ReadApp(filepath.Join(p.PoolApps(), ManifestName(app.AppID)))
		if err == nil && exists(filepath.Join(p.PoolApps(), commonDir, have.InstallDir)) {
			if !Newer(app.BuildID, have.BuildID) {
				continue
			}

			log("%s updated %s to build %s, the pool had %s",
				from, app.Name, app.BuildID, have.BuildID)
		}

		source := filepath.Join(steamapps, commonDir, app.InstallDir)
		if !exists(source) {
			// A manifest without files. Steam leaves these behind after a
			// failed install; taking it into the pool would hand every other
			// seat a game that is not there.
			continue
		}

		log("taking %s (%s) from %s into the pool", app.Name, app.AppID, from)

		// The pool belongs to the daemon: nothing reads it except the daemon,
		// and a seat that could write here could reach every other seat's
		// library.
		result, err := Clone(source, filepath.Join(p.PoolApps(), commonDir, app.InstallDir), owner)
		if err != nil {
			return fmt.Errorf("clone %s: %w", app.Name, err)
		}

		if result.Copied > 0 {
			report.Problems = append(report.Problems, fmt.Sprintf(
				"%s is not on the same filesystem as the pool, so %d of its files had "+
					"to be copied in full and now occupy their space twice",
				app.Name, result.Copied))
		}

		if err := p.copyManifest(app.Manifest, filepath.Join(p.PoolApps(), ManifestName(app.AppID)), owner); err != nil {
			return err
		}

		report.Harvested = append(report.Harvested, Move{
			App: app.AppID, Name: app.Name, Seat: from, Bytes: result.Bytes,
		})
	}

	return nil
}

// harvestFolders takes launcher agnostic game folders out of a seat.
//
// The same shape as the Steam pass with the two signals replaced: settled
// stands in for StateFlags, and the newest time inside the tree stands in for
// buildid. Only ever forward, for the same reason.
func (p *Pool) harvestFolders(m Member, report *Report, log Logger) error {
	dir := p.folders(m)
	if dir == "" {
		return nil
	}

	folders, err := ScanFolders(dir)
	if err != nil {
		return err
	}

	for _, folder := range folders {
		p.mark(m.Name, folderKey(folder.Name), statusDelivered)

		if !folder.Settled(p.Settle) {
			continue
		}

		have, known := p.state.Folders[folder.Name]
		if known && exists(filepath.Join(p.PoolFolders(), folder.Name)) && !folder.Newer(have) {
			continue
		}

		log("taking the folder %s from %s into the pool", folder.Name, m.Name)

		result, err := Clone(
			filepath.Join(dir, folder.Name),
			filepath.Join(p.PoolFolders(), folder.Name),
			p.own,
		)
		if err != nil {
			return fmt.Errorf("clone %s: %w", folder.Name, err)
		}

		if result.Copied > 0 {
			report.Problems = append(report.Problems, fmt.Sprintf(
				"the folder %s is not on the same filesystem as the pool, so %d of its "+
					"files had to be copied in full and now occupy their space twice",
				folder.Name, result.Copied))
		}

		// Measured again from the copy rather than carried over, so the
		// recorded version describes what the pool actually holds.
		stored, err := FolderAt(p.PoolFolders(), folder.Name)
		if err != nil {
			return err
		}

		p.state.Folders[folder.Name] = stored

		report.Harvested = append(report.Harvested, Move{
			App: folderKey(folder.Name), Name: folder.Name, Seat: m.Name, Bytes: result.Bytes,
		})
	}

	return nil
}

// distributeFolders offers every shared folder in the pool to one seat.
func (p *Pool) distributeFolders(m Member, report *Report, log Logger) error {
	dir := p.folders(m)
	if dir == "" {
		return nil
	}

	here := map[string]Folder{}

	folders, err := ScanFolders(dir)
	if err != nil {
		return err
	}

	for _, folder := range folders {
		here[folder.Name] = folder
	}

	names := make([]string, 0, len(p.state.Folders))
	for name := range p.state.Folders {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		pooled := p.state.Folders[name]

		if !exists(filepath.Join(p.PoolFolders(), name)) {
			continue
		}

		mine, have := here[name]

		switch {
		case have && !pooled.Newer(mine):
			continue

		case have && !m.Updatable:
			report.Pending = append(report.Pending, Move{
				App: folderKey(name), Name: name, Seat: m.Name,
			})

			continue

		case have:
			log("updating the folder %s in %s", name, m.Name)

		case p.status(m.Name, folderKey(name)) == statusDeclined:
			continue

		case p.status(m.Name, folderKey(name)) == statusDelivered:
			log("%s removed the folder %s, not offering it again", m.Name, name)

			p.mark(m.Name, folderKey(name), statusDeclined)

			report.Declined = append(report.Declined, Move{
				App: folderKey(name), Name: name, Seat: m.Name,
			})

			continue

		default:
			log("giving the folder %s to %s", name, m.Name)
		}

		result, err := Clone(
			filepath.Join(p.PoolFolders(), name),
			filepath.Join(dir, name),
			m.Owner,
		)
		if err != nil {
			return fmt.Errorf("clone %s: %w", name, err)
		}

		p.mark(m.Name, folderKey(name), statusDelivered)

		report.Delivered = append(report.Delivered, Move{
			App: folderKey(name), Name: name, Seat: m.Name, Bytes: result.Bytes,
		})
	}

	return nil
}

// distribute offers everything in the pool to one member.
func (p *Pool) distribute(m Member, pool []App, report *Report, log Logger) error {
	present := map[string]App{}

	apps, err := ReadApps(p.apps(m))
	if err != nil {
		return err
	}

	for _, app := range apps {
		present[app.AppID] = app
	}

	for _, app := range pool {
		if !app.Installed() {
			continue
		}

		here, have := present[app.AppID]

		switch {
		case have && !Newer(app.BuildID, here.BuildID):
			// Current, or the seat is ahead of the pool. Either way there is
			// nothing to send.
			continue

		case have && !m.Updatable:
			// The pool has a newer build but this seat cannot take it right
			// now, because replacing files under a running client would corrupt
			// an install rather than improve one. Reported rather than skipped
			// silently, so the interface can say an update is waiting instead
			// of looking like nothing is happening.
			report.Pending = append(report.Pending, Move{
				App: app.AppID, Name: app.Name, Seat: m.Name,
			})

			continue

		case have:
			// Newer in the pool and safe to apply, so this case does not skip
			// and the clone below runs. That clone builds the tree beside the
			// old one and swaps it in, so an interrupted update never leaves a
			// half replaced game.
			log("updating %s in %s from build %s to %s", app.Name, m.Name, here.BuildID, app.BuildID)

		case p.status(m.Name, app.AppID) == statusDeclined:
			continue

		case p.status(m.Name, app.AppID) == statusDelivered:
			// Handed over before and gone now, so somebody uninstalled it.
			log("%s uninstalled %s, not offering it again", m.Name, app.Name)

			p.mark(m.Name, app.AppID, statusDeclined)

			report.Declined = append(report.Declined, Move{App: app.AppID, Name: app.Name, Seat: m.Name})

			continue
		}

		source := filepath.Join(p.PoolApps(), commonDir, app.InstallDir)
		if !exists(source) {
			continue
		}

		log("giving %s (%s) to %s", app.Name, app.AppID, m.Name)

		result, err := Clone(source, filepath.Join(p.apps(m), commonDir, app.InstallDir), m.Owner)
		if err != nil {
			return fmt.Errorf("clone %s: %w", app.Name, err)
		}

		if err := p.copyManifest(app.Manifest, filepath.Join(p.apps(m), ManifestName(app.AppID)), m.Owner); err != nil {
			return err
		}

		p.mark(m.Name, app.AppID, statusDelivered)

		report.Delivered = append(report.Delivered, Move{
			App: app.AppID, Name: app.Name, Seat: m.Name, Bytes: result.Bytes,
		})
	}

	return nil
}

// copyManifest writes a manifest with the account specific fields cleared.
//
// The files land under a temporary name first. Steam globs this directory
// constantly, and a manifest it reads while it is being written is a manifest
// it may act on while it is half there.
func (p *Pool) copyManifest(from, to string, owner Owner) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}

	tmp := to + ".polyseat-tmp"

	if err := os.WriteFile(tmp, Rewrite(data), 0o644); err != nil {
		return err
	}

	if err := os.Chown(tmp, owner.UID, owner.GID); err != nil {
		os.Remove(tmp)

		return err
	}

	return os.Rename(tmp, to)
}

// AddSource starts tracking a Steam library outside the seats and harvests it
// straight away.
//
// Tracked rather than imported once, which is the difference between "the games
// that were on this machine when I pressed the button" and "the games on this
// machine". A one time import leaves the host's copy to drift ahead after the
// next update, and the seats would download that update themselves.
//
// Read only in both senses: the daemon takes from it and never writes into it,
// and a game uninstalled there stays in the pool and in the seats that have it.
func (p *Pool) AddSource(steamapps string, log Logger) (Report, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if log == nil {
		log = func(string, ...any) {}
	}

	var report Report

	resolved, err := filepath.EvalSymlinks(steamapps)
	if err != nil {
		return report, err
	}

	// The pool's own directories are not a source. Adding one would make the
	// pool harvest from itself, and a seat's library added by hand would let
	// that seat's copies be taken as if they were an independent library.
	if strings.HasPrefix(resolved+string(filepath.Separator), p.root+string(filepath.Separator)) {
		return report, fmt.Errorf("%s is inside the library itself", resolved)
	}

	if !slices.Contains(p.state.Sources, resolved) {
		p.state.Sources = append(p.state.Sources, resolved)
		sort.Strings(p.state.Sources)

		log("now tracking %s", resolved)
	}

	if err := p.harvestFrom(resolved, "host", p.own, &report, log); err != nil {
		return report, err
	}

	if err := p.save(); err != nil {
		return report, err
	}

	return report, nil
}

// RemoveSource stops tracking a library. What was already taken from it stays
// in the pool, because the seats are using it.
func (p *Pool) RemoveSource(steamapps string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.state.Sources = slices.DeleteFunc(p.state.Sources, func(s string) bool {
		return s == steamapps
	})

	return p.save()
}

// Sources lists the libraries outside the seats that the pool tracks.
func (p *Pool) Sources() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return slices.Clone(p.state.Sources)
}

// Remove drops a title from the pool, leaving every seat that has it alone.
func (p *Pool) Remove(appID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if name, ok := strings.CutPrefix(appID, "folder:"); ok {
		if err := safeName(name); err != nil {
			return err
		}

		if err := os.RemoveAll(filepath.Join(p.PoolFolders(), name)); err != nil {
			return err
		}

		delete(p.state.Folders, name)

		return p.save()
	}

	if err := safeName(ManifestName(appID)); err != nil {
		return err
	}

	manifest := filepath.Join(p.PoolApps(), ManifestName(appID))

	app, err := ReadApp(manifest)
	if err != nil {
		return err
	}

	if err := os.RemoveAll(filepath.Join(p.PoolApps(), commonDir, app.InstallDir)); err != nil {
		return err
	}

	return os.Remove(manifest)
}

// Offer clears a declined mark so the next sync hands the title over again.
func (p *Pool) Offer(seat, appID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if seats := p.state.Delivered[seat]; seats != nil {
		delete(seats, appID)
	}

	return p.save()
}

// Forget drops a seat's bookkeeping. Called when a seat is deleted, so that a
// seat rebuilt under the same name starts from a clean slate rather than
// inheriting the previous occupant's refusals.
func (p *Pool) Forget(seat string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.state.Delivered, seat)

	return p.save()
}

// ----------------------------------------------------------------- inventory

// Title is one entry in the pool as the web interface shows it.
type Title struct {
	// Kind is "steam" for a title read from an appmanifest and "folder" for a
	// plain directory shared for some other launcher. The interface shows both
	// in one list, because from where somebody is standing they are both just
	// games that are in the pool.
	Kind string `json:"kind"`

	AppID      string `json:"appid"`
	Name       string `json:"name"`
	InstallDir string `json:"installdir"`
	BuildID    string `json:"buildid"`
	Bytes      int64  `json:"bytes"`

	// In lists the seats that have this title, Declined those that turned it
	// down, and Stale those whose copy is an older build than the pool's.
	//
	// Stale is not a failure. It is a seat that was busy when the update came
	// through, and it says so rather than leaving somebody to wonder why one
	// seat is a patch behind.
	In       []string `json:"in"`
	Declined []string `json:"declined"`
	Stale    []string `json:"stale"`
}

// Inventory is the whole library.
type Inventory struct {
	Root   string  `json:"root"`
	Titles []Title `json:"titles"`

	// Bytes is what the pool holds. Saved is what having the same titles in
	// every seat would otherwise have cost, which is the number that says
	// whether any of this was worth building.
	Bytes int64 `json:"bytes"`
	Saved int64 `json:"saved"`
}

// Inventory reads the pool and every seat.
func (p *Pool) Inventory(members []Member) (Inventory, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	inv := Inventory{Root: p.root}

	apps, err := ReadApps(p.PoolApps())
	if err != nil {
		return inv, err
	}

	seatApps := map[string]map[string]string{}

	for _, m := range members {
		have, err := ReadApps(p.apps(m))
		if err != nil {
			continue
		}

		// The build each seat carries, not merely whether it has the title, so
		// a seat that is a patch behind can be told apart from one that is
		// current.
		builds := map[string]string{}
		for _, app := range have {
			builds[app.AppID] = app.BuildID
		}

		seatApps[m.Name] = builds
	}

	for _, app := range apps {
		title := Title{
			Kind:       "steam",
			AppID:      app.AppID,
			Name:       app.Name,
			InstallDir: app.InstallDir,
			BuildID:    app.BuildID,
			Bytes:      app.SizeOnDisk,
		}

		for _, m := range members {
			build, present := seatApps[m.Name][app.AppID]

			switch {
			case present:
				title.In = append(title.In, m.Name)

				if Newer(app.BuildID, build) {
					title.Stale = append(title.Stale, m.Name)
				}

			case p.status(m.Name, app.AppID) == statusDeclined:
				title.Declined = append(title.Declined, m.Name)
			}
		}

		sort.Strings(title.In)
		sort.Strings(title.Declined)
		sort.Strings(title.Stale)

		inv.Bytes += app.SizeOnDisk

		// Each seat holding a copy would have cost the size again if the copies
		// were real. They are not, so this is what the pool saved.
		inv.Saved += app.SizeOnDisk * int64(len(title.In))

		inv.Titles = append(inv.Titles, title)
	}

	// Shared folders alongside the Steam titles, measured from the pool rather
	// than from the recorded version, so a folder deleted from the pool by hand
	// stops being listed.
	for _, name := range p.folderNames() {
		if !exists(filepath.Join(p.PoolFolders(), name)) {
			continue
		}

		pooled := p.state.Folders[name]

		title := Title{
			Kind:       "folder",
			AppID:      folderKey(name),
			Name:       name,
			InstallDir: name,
			Bytes:      pooled.Bytes,
		}

		for _, m := range members {
			dir := p.folders(m)
			if dir == "" {
				continue
			}

			mine, err := FolderAt(dir, name)

			switch {
			case err == nil:
				title.In = append(title.In, m.Name)

				if pooled.Newer(mine) {
					title.Stale = append(title.Stale, m.Name)
				}

			case p.status(m.Name, folderKey(name)) == statusDeclined:
				title.Declined = append(title.Declined, m.Name)
			}
		}

		sort.Strings(title.In)
		sort.Strings(title.Declined)
		sort.Strings(title.Stale)

		inv.Bytes += title.Bytes
		inv.Saved += title.Bytes * int64(len(title.In))

		inv.Titles = append(inv.Titles, title)
	}

	sort.Slice(inv.Titles, func(i, j int) bool { return inv.Titles[i].Name < inv.Titles[j].Name })

	return inv, nil
}

func (p *Pool) folderNames() []string {
	out := make([]string, 0, len(p.state.Folders))
	for name := range p.state.Folders {
		out = append(out, name)
	}

	sort.Strings(out)

	return out
}

// ---------------------------------------------------------------- bookkeeping

func (p *Pool) status(seat, appID string) string {
	return p.state.Delivered[seat][appID]
}

func (p *Pool) mark(seat, appID, status string) {
	if p.state.Delivered == nil {
		p.state.Delivered = map[string]map[string]string{}
	}

	if p.state.Delivered[seat] == nil {
		p.state.Delivered[seat] = map[string]string{}
	}

	// A declined title stays declined even though the seat now has it: the seat
	// having it again means somebody installed it themselves, and the pool has
	// no business overwriting that copy later.
	if p.state.Delivered[seat][appID] == statusDeclined && status == statusDelivered {
		return
	}

	p.state.Delivered[seat][appID] = status
}

func (p *Pool) load() error {
	data, err := os.ReadFile(filepath.Join(p.root, stateFile))
	if err != nil {
		if os.IsNotExist(err) {
			p.state = state{
				Delivered: map[string]map[string]string{},
				Folders:   map[string]Folder{},
			}

			return nil
		}

		return err
	}

	if err := json.Unmarshal(data, &p.state); err != nil {
		return fmt.Errorf("%s: %w", stateFile, err)
	}

	if p.state.Delivered == nil {
		p.state.Delivered = map[string]map[string]string{}
	}

	if p.state.Folders == nil {
		p.state.Folders = map[string]Folder{}
	}

	return nil
}

func (p *Pool) save() error {
	data, err := json.MarshalIndent(p.state, "", "  ")
	if err != nil {
		return err
	}

	path := filepath.Join(p.root, stateFile)
	tmp := path + ".tmp"

	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}

	return os.Rename(tmp, path)
}

func exists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}

// settled reports whether a manifest has been quiet long enough to trust.
func settled(manifest string, quiet time.Duration) bool {
	info, err := os.Stat(manifest)
	if err != nil {
		return false
	}

	return time.Since(info.ModTime()) >= quiet
}
