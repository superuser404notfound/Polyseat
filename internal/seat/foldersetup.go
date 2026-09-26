package seat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/superuser404notfound/Polyseat/internal/library"
)

// folderSetup is the script a shared folder may carry to make itself usable
// where it has just arrived.
//
// The pool moves directories and nothing else, which is the whole reason the
// launcher agnostic side works at all: no manifest, no format, no launcher the
// daemon has to understand. That stops one step short of a game somebody can
// start. Steam gets over the same step for free, because an appmanifest travels
// inside the library folder and Steam reads the library itself; Lutris has no
// equivalent, its registry is a row in that machine's pga.db and a YAML beside
// it, and neither can be copied between machines - a host install names a
// runner under ~/.local/share/lutris/runners that no seat has.
//
// So the folder registers itself, in whatever it has arrived in, and this runs
// it. That is not a manifest: the daemon reads nothing out of the script and
// has no opinion about what is in it. It is the same bargain the rest of this
// side makes, that a directory is the unit and what is inside it is its own
// business.
//
// Named for the daemon rather than for a seat because the host receives folders
// too now, and a file called setup-seat.sh running on the machine the seats are
// hosted on invites exactly one wrong assumption about where it is.
const folderSetup = "polyseat-setup.sh"

// folderSetupLimit bounds one run.
//
// Generous because the honest work behind one of these is slow: the first run
// of the folder this convention came from builds a wine prefix, which is
// wineboot plus a registry pass. Bounded anyway, because it runs unattended and
// a script that waits for an answer nobody is there to give would otherwise
// hold a delivery open forever.
const folderSetupLimit = 10 * time.Minute

// folderSetupMax is the largest script this will consider. What these do is
// register a game with a launcher; one that is megabytes long is not that, and
// it has to be read whole to be hashed and shown.
const folderSetupMax = 1 << 20

// folderSetupShown is how much of a script the interface is given to read.
const folderSetupShown = 64 << 10

// minOwnerUID is the lowest uid a script from the pool is ever run as on the
// host.
//
// Below it are root and the system accounts. The host member's owner is read
// off the Steam library directory, and the search for one includes /root, so a
// library that happens to belong to root would otherwise make every folder's
// setup run as root. 1000 is where useradd starts people on every distribution
// this installs on.
const minOwnerUID = 1000

// errSystemOwner is the refusal, named so that a test can tell it from the
// run failing for some other reason, which as an ordinary user it always would.
var errSystemOwner = errors.New("setup scripts are never run as root or a system account")

// Why a setup script only runs once somebody has said it may.
//
// The folders reach the pool from the members, and a member is usually a seat,
// whose player is not trusted with anything beyond that seat: no sudo, by
// design. The script then ran by itself wherever the folder arrived, on the
// host as the owner of the library, who is the person who administers the
// machine and quite often has sudo, and in every other seat as that seat's
// player. So a player could get a shell as somebody else by putting a folder in
// ~/games/shared, or by putting a newer copy of a folder that was already
// there, since the pool takes the newest copy from whoever has it.
//
// Where a folder came from cannot settle this. The pool keeps no record of it,
// and even with one, a folder the host put in is replaced by the first seat
// that has a newer copy of it. So every script waits for a person, and the
// approval is tied to exactly what they were shown:
//
//   - the sha256 of the script, which is what they read, and
//   - the version of the folder in the pool at that moment, its size and its
//     newest time. The script runs the rest of the folder, a wine prefix or an
//     installer beside it, and the pool only ever takes a copy that is newer
//     than the one it has, so any change to the folder that reaches another
//     member is a different version and needs a new approval.
//
// What arrives before approval is written down per member, so that it runs when
// it is allowed, and in a seat that was switched off when it arrived as soon as
// the seat is running again. That record survives a daemon restart, which it
// has to: the setup is the step that makes the game start at all.

// setupFile is where that is kept, beside the seat records.
const setupFile = "folder-setup.json"

// folderSetups is the daemon's memory of setup scripts: which are allowed and
// which are waiting to run where.
type folderSetups struct {
	// path is the file it is kept in, or empty to keep it in memory only,
	// which is what a test wants.
	path string

	mu    sync.Mutex
	state setupState

	// running is set while the worker is going, so that a pass which asks for
	// one while it runs starts no second one.
	running bool
}

type setupState struct {
	// Approved is keyed by folder name.
	Approved map[string]setupApproval `json:"approved"`

	// Pending is keyed by member and then by folder, with the version of the
	// folder in the pool when it was delivered there.
	Pending map[string]map[string]library.Folder `json:"pending"`
}

// setupApproval is one script somebody allowed to run.
type setupApproval struct {
	Script  string         `json:"script"`
	Version library.Folder `json:"version"`
}

// sameVersion compares two measurements of a folder. Equal rather than ==,
// because a time read back from JSON has lost its monotonic reading.
func sameVersion(a, b library.Folder) bool {
	return a.Bytes == b.Bytes && a.Newest.Equal(b.Newest)
}

// openSetups reads what was kept, and starts empty when there is nothing or
// nothing readable. Empty is the safe direction: an approval forgotten is a
// question asked again, never a script run without one.
func openSetups(path string) (*folderSetups, error) {
	s := &folderSetups{path: path}
	s.state.Approved = map[string]setupApproval{}
	s.state.Pending = map[string]map[string]library.Folder{}

	if path == "" {
		return s, nil
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}

	if err != nil {
		return s, err
	}

	var state setupState
	if err := json.Unmarshal(data, &state); err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}

	if state.Approved != nil {
		s.state.Approved = state.Approved
	}

	if state.Pending != nil {
		s.state.Pending = state.Pending
	}

	return s, nil
}

// save writes the state out. Called with mu held.
func (s *folderSetups) save() error {
	if s.path == "" {
		return nil
	}

	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}

	return os.Rename(tmp, s.path)
}

// expect records that a folder with a setup script arrived at a member.
// It reports whether that version is already allowed.
func (s *folderSetups) expect(member, name string, version library.Folder) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state.Pending[member] == nil {
		s.state.Pending[member] = map[string]library.Folder{}
	}

	s.state.Pending[member][name] = version

	approval, ok := s.state.Approved[name]

	return ok && sameVersion(approval.Version, version), s.save()
}

// drop forgets one member's pending run.
func (s *folderSetups) drop(member, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.state.Pending[member][name]; !ok {
		return nil
	}

	delete(s.state.Pending[member], name)

	if len(s.state.Pending[member]) == 0 {
		delete(s.state.Pending, member)
	}

	return s.save()
}

// forget drops everything about one folder, for a folder removed from the
// pool. Without it, a folder taken out and later put back by a seat could come
// back with the old version's numbers and different contents, and find the old
// approval waiting for it.
func (s *folderSetups) forget(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.state.Approved, name)

	for member, folders := range s.state.Pending {
		delete(folders, name)

		if len(folders) == 0 {
			delete(s.state.Pending, member)
		}
	}

	return s.save()
}

// approve records that one script, in one version of its folder, may run.
func (s *folderSetups) approve(name, script string, version library.Folder) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.state.Approved[name] = setupApproval{Script: script, Version: version}

	return s.save()
}

// waiting is one pending run, as the worker sees it.
type waiting struct {
	member  string
	folder  string
	version library.Folder
}

// due lists the pending runs whose folder version is allowed, with the script
// hash it was allowed with. Sorted, so that runs happen in an order somebody
// reading the log can follow.
func (s *folderSetups) due() ([]waiting, map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []waiting

	scripts := map[string]string{}

	for member, folders := range s.state.Pending {
		for name, version := range folders {
			approval, ok := s.state.Approved[name]
			if !ok || !sameVersion(approval.Version, version) {
				continue
			}

			out = append(out, waiting{member: member, folder: name, version: version})
			scripts[name] = approval.Script
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].member != out[j].member {
			return out[i].member < out[j].member
		}

		return out[i].folder < out[j].folder
	})

	return out, scripts
}

// unapproved lists, per folder, the members waiting on a version nobody has
// allowed.
func (s *folderSetups) unapproved() map[string][]string {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := map[string][]string{}

	for member, folders := range s.state.Pending {
		for name, version := range folders {
			approval, ok := s.state.Approved[name]
			if ok && sameVersion(approval.Version, version) {
				continue
			}

			out[name] = append(out[name], member)
		}
	}

	for name := range out {
		sort.Strings(out[name])
	}

	return out
}

// isApproved reports whether one script in one version is allowed.
func (s *folderSetups) isApproved(name, script string, version library.Folder) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	approval, ok := s.state.Approved[name]

	return ok && approval.Script == script && sameVersion(approval.Version, version)
}

// readSetup reads a setup script for hashing, and refuses anything that is
// not a plain executable file of a sensible size.
//
// Lstat and not Stat. A symlink by that name would be hashed as whatever it
// points at on this machine, which is decided by the name in the link and by
// nothing the approval covered.
func readSetup(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}

	switch {
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("%s is not a plain file", folderSetup)
	case info.Mode().Perm()&0o111 == 0:
		return nil, fmt.Errorf("%s is not executable", folderSetup)
	case info.Size() > folderSetupMax:
		return nil, fmt.Errorf("%s is %d bytes, and no setup script is that long", folderSetup, info.Size())
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	defer f.Close()

	return io.ReadAll(io.LimitReader(f, folderSetupMax+1))
}

// setupMatches says whether the script at path is the one that was allowed.
func setupMatches(path, allowed string) error {
	data, err := readSetup(path)
	if err != nil {
		return err
	}

	if scriptSum(data) != allowed {
		return fmt.Errorf("its %s here is not the one that was allowed", folderSetup)
	}

	return nil
}

// scriptSum is the hash an approval is given for.
func scriptSum(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// runnable reports whether a path is a file somebody could execute.
//
// The executable bit is part of the question rather than left to exec. A folder
// arriving from a filesystem that does not carry the bit, or unpacked from a
// zip, has a setup script that cannot run, and "permission denied" out of the
// daemon's log is a worse way to learn that than not trying.
//
// Lstat, for the reason readSetup gives: a link at that name is not a script
// anybody was shown.
func runnable(path string) bool {
	info, err := os.Lstat(path)

	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// noteDeliveries writes down the folders a pass just delivered that carry a
// setup script. Called by the pass with the sync lock held, so that the version
// recorded is the one the pool had when it handed the folder over and not one a
// later pass has since taken in.
func (m *Manager) noteDeliveries(members []library.Member, report library.Report) {
	if m.pool == nil || m.setups == nil {
		return
	}

	known := make(map[string]library.Member, len(members))
	for _, member := range members {
		known[member.Name] = member
	}

	for _, move := range report.Delivered {
		name, ok := library.FolderName(move.App)
		if !ok {
			// A Steam title. Its manifest travels with it and Steam reads the
			// library itself, so there is nothing to run.
			continue
		}

		member, ok := known[move.Seat]
		if !ok {
			continue
		}

		dir := m.pool.FoldersOf(member)
		if dir == "" {
			continue
		}

		// Looked for on the host in both cases, because a seat's folders live
		// under the pool root and are only bind mounted into the container.
		if !runnable(filepath.Join(dir, name, folderSetup)) {
			// A new version without a script, or with one that cannot run,
			// takes back whatever the old one was waiting to do.
			if err := m.setups.drop(member.Name, name); err != nil {
				m.log.Warn("the folder setup record could not be written", "err", err)
			}

			continue
		}

		version, err := library.FolderAt(m.pool.PoolFolders(), name)
		if err != nil {
			m.moved(member.Name, "! %s arrived, and its version in the pool could not be read: %v", name, err)

			continue
		}

		allowed, err := m.setups.expect(member.Name, name, version)
		if err != nil {
			m.log.Warn("the folder setup record could not be written", "err", err)
		}

		if !allowed {
			m.moved(member.Name, "%s arrived with a %s, which runs once it is allowed under Library",
				name, folderSetup)
		}
	}
}

// settleSoon starts the worker that runs allowed setup scripts, unless it is
// already going.
//
// Off the caller's goroutine, which is either the daemon's main loop or an
// interface request, and neither may wait minutes for a wine prefix. One worker
// at a time, so that the scripts run one after another as they always did.
//
// The worker's context is its own rather than the caller's. A pass started
// from an interface button carries the request's context, which ends when the
// answer has been sent, and that would have killed every setup it started a
// moment after starting it.
func (m *Manager) settleSoon() {
	if m.setups == nil {
		return
	}

	m.setups.mu.Lock()

	if m.setups.running {
		m.setups.mu.Unlock()

		return
	}

	m.setups.running = true
	m.setups.mu.Unlock()

	go func() {
		defer func() {
			m.setups.mu.Lock()
			m.setups.running = false
			m.setups.mu.Unlock()
		}()

		m.settle(context.Background())
	}()
}

// settle runs every allowed setup that has somewhere to run, until a round
// runs nothing.
func (m *Manager) settle(ctx context.Context) {
	tried := map[waiting]bool{}

	for {
		due, scripts := m.setups.due()

		ran := false

		for _, w := range due {
			if tried[w] {
				continue
			}

			tried[w] = true

			if m.runFolderSetup(ctx, w, scripts[w.folder]) {
				ran = true
			}
		}

		if !ran {
			return
		}
	}
}

// runFolderSetup runs one allowed setup where its folder now lives, and reports
// whether it got as far as running it.
//
// A member that cannot run it yet, a seat that is switched off, keeps the run
// waiting. Everything else, a run that worked, one that failed and one that was
// refused, is done with: running a failing script again every minute would
// spend ten minutes of a machine at a time on the same failure.
func (m *Manager) runFolderSetup(ctx context.Context, w waiting, allowed string) bool {
	done := func() {
		if err := m.setups.drop(w.member, w.folder); err != nil {
			m.log.Warn("the folder setup record could not be written", "err", err)
		}
	}

	var member library.Member

	if w.member == hostMember {
		hosts := m.hostMembers(false)
		if len(hosts) == 0 {
			return false
		}

		member = hosts[0]
	} else {
		s, err := m.store.Get(w.member)
		if err != nil {
			// The seat is gone, and its runs with it.
			done()

			return false
		}

		status, err := m.client.Status(s.Name)
		if err != nil || status != "Running" {
			return false
		}

		member = library.Member{Name: s.Name}
	}

	dir := m.pool.FoldersOf(member)
	if dir == "" {
		return false
	}

	// The copy that is about to run, hashed here and now, and held to what was
	// allowed. The version says the pool's copy is the one somebody looked at;
	// this says the member's copy of the script still is.
	if err := setupMatches(filepath.Join(dir, w.folder, folderSetup), allowed); err != nil {
		m.moved(w.member, "! %s was not set up: %v", w.folder, err)
		done()

		return false
	}

	m.moved(w.member, "%s arrived, running its %s", w.folder, folderSetup)

	ctx, cancel := context.WithTimeout(ctx, folderSetupLimit)
	defer cancel()

	var (
		out string
		err error
	)

	if w.member == hostMember {
		out, err = runSetupOnHost(ctx, dir, w.folder, member.Owner)
	} else {
		var reached bool

		out, reached, err = m.runSetupInSeat(ctx, w.member, w.folder)
		if !reached {
			// The seat went away under the run. It is asked again when it is
			// back, which is the point of having kept it.
			m.moved(w.member, "! %s could not be set up yet: %v", w.folder, err)

			return false
		}
	}

	done()

	if err != nil {
		m.moved(w.member, "! %s could not set itself up: %v", w.folder, err)

		if line := lastLine(out); line != "" {
			m.moved(w.member, "! it said: %s", line)
		}

		return true
	}

	if line := lastLine(out); line != "" {
		m.moved(w.member, "%s: %s", w.folder, line)
	}

	return true
}

// runSetupInSeat runs the script through the container, as the player, from
// inside its folder.
//
// The path is the one inside the container rather than the one on the host: the
// folders are bind mounted, so the same files have two names and only one of
// them exists for the process that runs them. The working directory is the
// folder, as it is on the host, so a script that names its neighbours by a
// relative path works in both places.
//
// reached is false when the seat could not be asked at all, which is the one
// outcome worth trying again.
func (m *Manager) runSetupInSeat(ctx context.Context, seat, name string) (string, bool, error) {
	folder := LibraryMount + "/shared/" + name
	script := folder + "/" + folderSetup

	argv := append(playerPrefix(m.uidOf(seat)),
		"HOME=/home/"+Player,
		// Ended by the seat and not only by this side's deadline, for the
		// reason asPlayerFor gives: Incus does not end a command whose
		// caller stopped waiting.
		"timeout", "-k", "10", strconv.Itoa(int(folderSetupLimit/time.Second)),
		"sh", "-c", `cd -- "$1" && exec "$2"`, "sh", folder, script)

	out, code, err := look(ctx, m.client, seat, folderSetupLimit, argv...)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return out, true, fmt.Errorf("it did not finish within %s", folderSetupLimit)
		}

		return out, false, err
	}

	if code != 0 {
		return out, true, fmt.Errorf("it exited %d", code)
	}

	return out, true, nil
}

// runSetupOnHost runs the script directly, as whoever owns the library.
//
// Never as root or as a system account, see minOwnerUID. Running it as the
// owner gives it exactly what that person already has, which is a great deal
// on a machine they administer, and that is why nothing reaches this without
// the approval above: a script out of the pool is a script a seat may have
// written.
func runSetupOnHost(ctx context.Context, dir, name string, owner library.Owner) (string, error) {
	if owner.UID < minOwnerUID {
		return "", fmt.Errorf("%w: the library belongs to uid %d", errSystemOwner, owner.UID)
	}

	who := ownerOf(owner.UID)
	if who == nil || who.HomeDir == "" {
		return "", fmt.Errorf("uid %d has no passwd entry to take a home from", owner.UID)
	}

	work := filepath.Join(dir, name)

	// A fresh environment rather than the daemon's. systemd gives a service a
	// sparse one anyway, and what is left of it - root's HOME above all - is
	// exactly what would send a setup script writing into the wrong place.
	env := []string{
		"HOME=" + who.HomeDir,
		"USER=" + who.Username,
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(owner.UID),
	}

	return runBounded(ctx, work, filepath.Join(work, folderSetup), env, &syscall.Credential{
		Uid: uint32(owner.UID),
		Gid: uint32(owner.GID),
	})
}

// setupTail is how much of a script's output is kept, from the end. Only the
// last line is ever logged.
const setupTail = 8 << 10

// runBounded runs one program with a deadline that holds.
//
// exec.CommandContext on its own does not give one. It kills the process it
// started and then waits for the output pipe to close, and a script that
// started anything in the background, which wineboot does, leaves that pipe
// open in a grandchild: the wait then lasts as long as the grandchild does.
// So the program gets a process group of its own, the whole group is killed
// when the time is up, and WaitDelay is the last word for anything that left
// the group on its way.
//
// cred is nil only in a test, which cannot change user.
func runBounded(ctx context.Context, dir, program string, env []string, cred *syscall.Credential) (string, error) {
	cmd := exec.CommandContext(ctx, program)
	cmd.Dir = dir
	cmd.Env = env

	out := &tailBuffer{limit: setupTail}
	cmd.Stdout = out
	cmd.Stderr = out

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: cred}

	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	cmd.WaitDelay = 10 * time.Second

	err := cmd.Run()

	if ctx.Err() != nil {
		return out.String(), fmt.Errorf("it did not finish within the time it had: %w", ctx.Err())
	}

	return out.String(), err
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.buf = append(b.buf, p...)

	if over := len(b.buf) - b.limit; over > 0 {
		b.buf = append(b.buf[:0], b.buf[over:]...)
	}

	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return string(b.buf)
}

// ------------------------------------------------------------------ interface

// FolderSetup is one setup script waiting for somebody to allow it.
type FolderSetup struct {
	Folder string `json:"folder"`

	// SHA256 is what allowing it has to name, so that what is allowed is what
	// was read and not whatever arrived in the pool in between.
	SHA256 string `json:"sha256"`

	// Script is the text, or its first folderSetupShown bytes with Truncated
	// set.
	Script    string `json:"script"`
	Truncated bool   `json:"truncated,omitempty"`

	// Waiting names the members it would run in, which is also who it would
	// run as.
	Waiting []string `json:"waiting"`

	// Problem is set instead of a script when the pool's copy cannot be
	// offered, a link or a file too large, so that the interface can say why
	// there is nothing to allow.
	Problem string `json:"problem,omitempty"`
}

// folderSetupsWaiting lists the scripts waiting on a version nobody allowed.
//
// Read from the pool's copy, which only the daemon writes, and measured only
// for folders something is waiting on, which is rarely any: this is on the
// interface's path.
func (m *Manager) folderSetupsWaiting() []FolderSetup {
	out := []FolderSetup{}

	if m.pool == nil || m.setups == nil {
		return out
	}

	unapproved := m.setups.unapproved()

	names := make([]string, 0, len(unapproved))
	for name := range unapproved {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		item := FolderSetup{Folder: name, Waiting: unapproved[name]}

		data, err := readSetup(filepath.Join(m.pool.PoolFolders(), name, folderSetup))
		if err != nil {
			item.Problem = err.Error()
			out = append(out, item)

			continue
		}

		item.SHA256 = scriptSum(data)

		// Allowed already, at the pool's current version: the members listed
		// hold an older copy and run it once the newer one reaches them. Not
		// a question for anybody.
		if version, err := library.FolderAt(m.pool.PoolFolders(), name); err == nil &&
			m.setups.isApproved(name, item.SHA256, version) {
			continue
		}

		if len(data) > folderSetupShown {
			data = data[:folderSetupShown]
			item.Truncated = true
		}

		item.Script = strings.ToValidUTF8(string(data), "�")
		out = append(out, item)
	}

	return out
}

// ErrSetupChanged is returned when a script is allowed by a hash it no longer
// has.
var ErrSetupChanged = errors.New("the setup script changed since it was shown, look at it again")

// ApproveFolderSetup allows one folder's setup script to run, in the version
// the pool holds now, and starts running it wherever it waits.
//
// sum is the hash the interface showed next to the script. The pool's copy is
// read again and has to match it, because a newer copy may have arrived between
// somebody reading the script and pressing the button.
func (m *Manager) ApproveFolderSetup(name, sum string) error {
	if m.pool == nil || m.setups == nil {
		return fmt.Errorf("%w: %s", ErrNoLibrary, m.libraryErr)
	}

	m.syncMu.Lock()

	version, err := library.FolderAt(m.pool.PoolFolders(), name)
	if err != nil {
		m.syncMu.Unlock()

		return fmt.Errorf("the pool has no folder %q: %w", name, err)
	}

	data, err := readSetup(filepath.Join(m.pool.PoolFolders(), name, folderSetup))
	if err != nil {
		m.syncMu.Unlock()

		return err
	}

	if !strings.EqualFold(scriptSum(data), strings.TrimSpace(sum)) {
		m.syncMu.Unlock()

		return ErrSetupChanged
	}

	err = m.setups.approve(name, scriptSum(data), version)

	m.syncMu.Unlock()

	if err != nil {
		return err
	}

	m.log.Info("library: a folder's setup script was allowed to run", "folder", name, "sha256", scriptSum(data))

	m.settleSoon()

	return nil
}
