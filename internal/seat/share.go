package seat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/superuser404notfound/Polyseat/internal/library"
)

// Carrying a launcher's entries between the members.
//
// What lutrisshare.go describes for Lutris holds for every launcher that keeps
// what it knows about a game outside the game's folder: the pool moves the
// folder, and the entry that says how to start it has to be carried by the
// daemon. This is the part that does not depend on the launcher. A shareBook
// keeps the recipes and the record of what was written where, and runs the
// rounds that read every member and give each one what it is missing; the
// launcher brings a helper script that scans, registers, unregisters and moves,
// and the checks that turn one of its entries into a recipe. See
// lutrisshare.go and faugusshare.go for the two there are.

// shareLimit bounds one call of a helper. Each reads a small database or a
// JSON file, or renames one directory, and takes a fraction of a second.
const shareLimit = 60 * time.Second

// shareSuffix is what the helpers name their own entries by, so that a scan can
// tell an entry somebody made from one the daemon made, and a recipe is only
// ever taken from the first kind.
const shareSuffix = "-polyseat"

// shareRecipe is how one shared folder is started in one launcher. Comparable,
// so that a recipe read again unchanged is not written out again.
type shareRecipe[R any] interface {
	comparable

	// author is the member the recipe was read from, which is never written to
	// from it; authored is the same recipe with another.
	author() string
	authored(from string) R
}

// shareEntry is one game in a member's launcher as its helper reports it.
type shareEntry[R any] interface {
	// key is what the launcher calls the game, its name what a person does.
	key() string
	title() string

	// place is the shared folder its files are in and the directory below the
	// games root they are in, either empty when they are not.
	place() (folder, home string)

	// written reports whether the daemon wrote this entry itself.
	written() bool

	// recipe checks the entry and makes a recipe of it.
	recipe(from string) (R, error)
}

// shareLauncher is what one launcher brings.
type shareLauncher struct {
	// name is the launcher's own, for the log.
	name string

	// script is the helper, run with python3 -I as the member's person.
	script string

	// stamp is what changes on the host when the launcher's games do, below
	// the home of the library's owner.
	stamp []string

	// validKey says whether a key from the interface is one the launcher
	// could have given a game, before it is handed to the helper.
	validKey func(string) bool

	// prepare is a helper command run with the member's shared directory
	// before each scan, when the launcher has one: whatever has to be put
	// right in the member before its games are read.
	prepare string
}

type shareState[R any] struct {
	// Recipes is keyed by folder name.
	Recipes map[string]R `json:"recipes"`

	// Registered is keyed by member and then folder, with what the helper
	// named the entry it wrote there. An empty name means the member already
	// had an entry of its own, or one by that key, and was left alone; it is
	// kept so that the question is not asked every round.
	Registered map[string]map[string]string `json:"registered"`
}

// shareBook is the daemon's memory of one launcher's entries, and of which
// members it has to look at again.
type shareBook[R shareRecipe[R], E shareEntry[R]] struct {
	shareLauncher

	path string

	mu    sync.Mutex
	state shareState[R]

	// everyone is set when every member has to be looked at, which is the case
	// at startup and after any folder moved. stale is the members still owed
	// a look once that has been handed out, which a seat that is switched off
	// stays until it is running.
	everyone bool
	stale    map[string]bool

	// hostStamp is what the host's launcher looked like at the last round, so
	// that an entry added there by hand is noticed without a folder moving.
	hostStamp string

	// running is set while a round goes, see soon.
	running bool

	// host is the last listing of the host's own launcher for the interface,
	// with the stamp it was taken against.
	host      []HostGame
	hostShown string
}

func openShares[R shareRecipe[R], E shareEntry[R]](path string, launcher shareLauncher) (*shareBook[R, E], error) {
	s := &shareBook[R, E]{shareLauncher: launcher, path: path, everyone: true, stale: map[string]bool{}}
	s.state.Recipes = map[string]R{}
	s.state.Registered = map[string]map[string]string{}

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

	var state shareState[R]
	if err := json.Unmarshal(data, &state); err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}

	if state.Recipes != nil {
		s.state.Recipes = state.Recipes
	}

	if state.Registered != nil {
		s.state.Registered = state.Registered
	}

	return s, nil
}

// save writes the state out. Called with mu held.
func (s *shareBook[R, E]) save() error {
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

// markAll says every member is owed a look.
func (s *shareBook[R, E]) markAll() {
	s.mu.Lock()
	s.everyone = true
	s.mu.Unlock()
}

// owed hands out the members to look at this round, out of those given.
func (s *shareBook[R, E]) owed(members []string) map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.everyone {
		for _, name := range members {
			s.stale[name] = true
		}

		s.everyone = false
	}

	known := map[string]bool{}
	for _, name := range members {
		known[name] = true
	}

	out := map[string]bool{}

	for name := range s.stale {
		if !known[name] {
			// A seat that left the pool or was deleted.
			delete(s.stale, name)

			continue
		}

		out[name] = true
	}

	return out
}

// done says one member has been looked at.
func (s *shareBook[R, E]) done(member string) {
	s.mu.Lock()
	delete(s.stale, member)
	s.mu.Unlock()
}

// forgetRecipe drops a folder's recipe, for a folder taken out of the pool, so
// that one put back later is read from whoever installs it then.
func (s *shareBook[R, E]) forgetRecipe(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.state.Recipes, name)
	s.everyone = true

	return s.save()
}

// forgetMember drops what was written into one member, for a seat that is
// deleted.
//
// The recipes read from it are kept, without an author: the folders are still
// in the pool and in the other seats, and a seat built after this one is gone
// should still find them in its launcher. The next member to offer one of its
// own takes it over, see learn.
func (s *shareBook[R, E]) forgetMember(member string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.state.Registered, member)
	delete(s.stale, member)

	for name, recipe := range s.state.Recipes {
		if recipe.author() == member {
			s.state.Recipes[name] = recipe.authored("")
		}
	}

	s.everyone = true

	return s.save()
}

// learn takes recipes out of one member's scan.
//
// The first member to offer one for a folder keeps it, and only that member
// can change it afterwards. Whoever installed a game first is the one whose
// entry was made by the launcher itself; the entries the daemon writes are
// left out of every scan, so the recipe never comes back round from a copy of
// itself.
func (s *shareBook[R, E]) learn(member string, entries []E, pooled func(string) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := false

	for _, e := range entries {
		folder, _ := e.place()
		if e.written() || folder == "" || !pooled(folder) {
			continue
		}

		recipe, err := e.recipe(member)
		if err != nil {
			continue
		}

		have, ok := s.state.Recipes[folder]
		if ok && have.author() != "" && have.author() != member {
			continue
		}

		if ok && have == recipe {
			continue
		}

		s.state.Recipes[folder] = recipe
		changed = true
	}

	// A recipe whose own member no longer has an entry for its folder is
	// withdrawn: the game was taken out of this launcher there, most often to
	// move it to another one. Kept, it would go on being handed to every new
	// seat and listed under this launcher forever, since only a folder leaving
	// the pool dropped one. The entries written from it are taken back by
	// member, which finds the recipe gone.
	own := map[string]bool{}

	for _, e := range entries {
		if folder, _ := e.place(); folder != "" && !e.written() {
			own[folder] = true
		}
	}

	for folder, recipe := range s.state.Recipes {
		if recipe.author() == member && !own[folder] {
			delete(s.state.Recipes, folder)
			s.everyone = true
			changed = true
		}
	}

	if !changed {
		return nil
	}

	return s.save()
}

func (s *shareBook[R, E]) recipes() map[string]R {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make(map[string]R, len(s.state.Recipes))
	for k, v := range s.state.Recipes {
		out[k] = v
	}

	return out
}

func (s *shareBook[R, E]) registered(member string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := map[string]string{}
	for k, v := range s.state.Registered[member] {
		out[k] = v
	}

	return out
}

// note records what a member was given for a folder, or with forget, that it
// no longer has anything.
func (s *shareBook[R, E]) note(member, folder, written string, forget bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if forget {
		delete(s.state.Registered[member], folder)

		if len(s.state.Registered[member]) == 0 {
			delete(s.state.Registered, member)
		}

		return s.save()
	}

	if s.state.Registered[member] == nil {
		s.state.Registered[member] = map[string]string{}
	}

	s.state.Registered[member][folder] = written

	return s.save()
}

// shareScanResult is what a helper's scan answers. Present is false for a
// member whose launcher has never run, or is not installed, so that there is
// nothing to read and nowhere to write yet.
type shareScanResult[E any] struct {
	Present bool   `json:"present"`
	Games   []E    `json:"games"`
	Error   string `json:"error"`
}

// shareResult is what register, unregister and move answer. Written is what
// the helper named the entry it wrote: Lutris's configpath, Faugus's game id.
type shareResult struct {
	Status  string `json:"status"`
	Written string `json:"written"`
	Folder  string `json:"folder"`
	Error   string `json:"error"`

	// Old and New are where a move took the folder from and to.
	Old string `json:"old"`
	New string `json:"new"`
}

// ------------------------------------------------------------------- rounds

// shareCarrier is a shareBook seen from the rest of the manager, which has no
// use for what kind of recipe it holds.
type shareCarrier interface {
	markAll()
	forgetRecipe(name string) error
	forgetMember(member string) error
	soon(m *Manager)
	repointHost(ctx context.Context, m *Manager, old, new string)

	// launcher is the launcher's name, folders the shared folders it has a
	// recipe for.
	launcher() string
	folders() []string
}

// folderLaunchers is, per shared folder, the launchers that carry it, in the
// order shares lists them.
func (m *Manager) folderLaunchers() map[string][]string {
	out := map[string][]string{}

	for _, s := range m.shares() {
		for _, folder := range s.folders() {
			out[folder] = append(out[folder], s.launcher())
		}
	}

	return out
}

func (s *shareBook[R, E]) launcher() string { return s.name }

func (s *shareBook[R, E]) folders() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]string, 0, len(s.state.Recipes))
	for folder := range s.state.Recipes {
		out = append(out, folder)
	}

	sort.Strings(out)

	return out
}

// shares is every launcher whose entries the pool carries, or none when there
// is no pool.
func (m *Manager) shares() []shareCarrier {
	var out []shareCarrier

	if m.lutris != nil {
		out = append(out, m.lutris)
	}

	if m.faugus != nil {
		out = append(out, m.faugus)
	}

	if m.heroic != nil {
		out = append(out, m.heroic)
	}

	return out
}

// sharesAfter is what a library pass hands on: any folder that moved means
// every member is owed a look, and so does a change to a host launcher.
func (m *Manager) sharesAfter(report library.Report) {
	moved := false

	for _, moves := range [][]library.Move{report.Harvested, report.Delivered, report.Declined} {
		for _, move := range moves {
			if _, ok := library.FolderName(move.App); ok {
				moved = true
			}
		}
	}

	for _, s := range m.shares() {
		if moved {
			s.markAll()
		}

		s.soon(m)
	}
}

// sharesSoon starts a round of every launcher that is not in one already.
func (m *Manager) sharesSoon() {
	for _, s := range m.shares() {
		s.soon(m)
	}
}

// soon starts a round unless one is going, off the caller's goroutine for the
// reason settleSoon gives.
func (s *shareBook[R, E]) soon(m *Manager) {
	if m.pool == nil {
		return
	}

	s.mu.Lock()

	if s.running {
		s.mu.Unlock()

		return
	}

	s.running = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		}()

		s.round(context.Background(), m)
	}()
}

// sharePlace is where one member keeps its folders, in the two forms the
// helper and the daemon need: the path as the member sees it and as the host
// does.
type sharePlace struct {
	member library.Member

	// root and shared are the games root and the shared directory as the
	// member's own processes name them.
	root, shared string

	// onHost is the shared directory on the host's filesystem, where the
	// daemon can list it without asking the member.
	onHost string
}

func (m *Manager) sharePlaces() []sharePlace {
	var out []sharePlace

	for _, member := range m.members(false) {
		onHost := m.pool.FoldersOf(member)
		if onHost == "" {
			continue
		}

		place := sharePlace{member: member, onHost: onHost}

		if member.Name == hostMember {
			place.root, place.shared = filepath.Dir(onHost), onHost
		} else {
			place.root, place.shared = LibraryMount, LibraryMount+"/shared"
		}

		out = append(out, place)
	}

	return out
}

// hostPlace is the host's own place, or nil when the host takes no part in
// the folders.
func (m *Manager) hostPlace() *sharePlace {
	for _, p := range m.sharePlaces() {
		if p.member.Name == hostMember {
			return &p
		}
	}

	return nil
}

// round looks at every member owed a look: first what each one's launcher
// has, so that a game installed anywhere has its recipe before anybody is
// given one, then what each one is missing.
func (s *shareBook[R, E]) round(ctx context.Context, m *Manager) {
	places := m.sharePlaces()

	names := make([]string, 0, len(places))
	for _, p := range places {
		names = append(names, p.member.Name)
	}

	for _, p := range places {
		if p.member.Name != hostMember {
			continue
		}

		stamp := s.stampOf(p.member)

		s.mu.Lock()
		if stamp != s.hostStamp {
			s.hostStamp = stamp
			s.everyone = true
		}
		s.mu.Unlock()
	}

	owed := s.owed(names)
	if len(owed) == 0 {
		return
	}

	pooled := func(name string) bool {
		info, err := os.Lstat(filepath.Join(m.pool.PoolFolders(), name))

		return err == nil && info.IsDir()
	}

	scans := map[string][]E{}

	for _, p := range places {
		if !owed[p.member.Name] || !m.shareReachable(p.member) {
			continue
		}

		if s.prepare != "" {
			s.prepareMember(ctx, m, p)
		}

		result, err := s.scan(ctx, m, p)
		if err != nil {
			m.log.Warn("library: the "+s.name+" of a member could not be read",
				"member", p.member.Name, "err", err)

			continue
		}

		if !result.Present {
			// Never started, so there is nothing to read and nowhere to write
			// yet. Asked again on a later round.
			continue
		}

		scans[p.member.Name] = result.Games

		if err := s.learn(p.member.Name, result.Games, pooled); err != nil {
			m.log.Warn("the record of carried "+s.name+" entries could not be written", "err", err)
		}
	}

	recipes := s.recipes()

	for _, p := range places {
		entries, ok := scans[p.member.Name]
		if !ok {
			continue
		}

		if s.member(ctx, m, p, entries, recipes) {
			s.done(p.member.Name)
		}
	}
}

// shareReachable reports whether the member can be asked now: the host
// always, a seat while it is running.
func (m *Manager) shareReachable(member library.Member) bool {
	if member.Name == hostMember {
		return member.Owner.UID >= minOwnerUID
	}

	status, err := m.client.Status(member.Name)

	return err == nil && status == "Running"
}

// member gives one member the entries it is missing and takes back the ones
// whose folder it no longer has, or whose recipe was withdrawn, see learn. It
// reports whether everything owed was settled, which a member whose launcher
// could not be written to is not.
func (s *shareBook[R, E]) member(ctx context.Context, m *Manager, p sharePlace, entries []E, recipes map[string]R) bool {
	present := map[string]bool{}

	list, err := os.ReadDir(p.onHost)
	if err != nil {
		return false
	}

	for _, d := range list {
		// IsDir off the directory entry, which does not follow a link.
		if d.IsDir() {
			present[d.Name()] = true
		}
	}

	covered := map[string]bool{}

	for _, e := range entries {
		if folder, _ := e.place(); folder != "" {
			covered[folder] = true
		}
	}

	settled := true
	given := s.registered(p.member.Name)

	for folder, written := range given {
		_, wanted := recipes[folder]
		if present[folder] && wanted {
			continue
		}

		if written != "" {
			res, err := s.call(ctx, m, p, "unregister", p.shared, folder, written)
			if err != nil {
				m.moved(p.member.Name, "! the %s entry for %s could not be removed: %v", s.name, folder, err)

				settled = false

				continue
			}

			if res.Status == "removed" {
				if present[folder] {
					m.moved(p.member.Name, "%s left %s where it came from, and its entry here went with it", folder, s.name)
				} else {
					m.moved(p.member.Name, "%s is gone, and so is its %s entry", folder, s.name)
				}
			}
		}

		if err := s.note(p.member.Name, folder, "", true); err != nil {
			m.log.Warn("the record of carried "+s.name+" entries could not be written", "err", err)
		}
	}

	folders := make([]string, 0, len(present))
	for name := range present {
		folders = append(folders, name)
	}

	sort.Strings(folders)

	for _, folder := range folders {
		recipe, ok := recipes[folder]
		if !ok || recipe.author() == p.member.Name || covered[folder] {
			continue
		}

		if _, ok := given[folder]; ok {
			// Written once already, or left alone once. An entry somebody
			// removed by hand stays removed.
			continue
		}

		if runnable(filepath.Join(p.onHost, folder, folderSetup)) {
			continue
		}

		arg, err := json.Marshal(struct {
			Recipe R      `json:"recipe"`
			Folder string `json:"folder"`
		}{recipe, folder})
		if err != nil {
			continue
		}

		res, err := s.call(ctx, m, p, "register", p.shared, string(arg))
		if err != nil {
			m.moved(p.member.Name, "! %s could not be added to %s: %v", folder, s.name, err)

			settled = false

			continue
		}

		switch res.Status {
		case "added":
			m.moved(p.member.Name, "%s was added to %s", folder, s.name)

			if err := s.note(p.member.Name, folder, res.Written, false); err != nil {
				m.log.Warn("the record of carried "+s.name+" entries could not be written", "err", err)
			}

		case "present", "taken":
			if err := s.note(p.member.Name, folder, "", false); err != nil {
				m.log.Warn("the record of carried "+s.name+" entries could not be written", "err", err)
			}

		case "absent", "busy":
			// Not installed or never run, or open and about to write its own
			// list over this one. Asked again on a later round.
			settled = false
		}
	}

	return settled
}

// storeNames are what the helpers' kinds of store are called in the log.
var storeNames = map[string]string{
	"ea": "the EA App", "ubisoft": "Ubisoft Connect", "heroic": "Heroic",
}

// prepareMember runs the launcher's prepare command in one member and says
// what it did. What it could not do is for the log, not the interface: it is
// asked again every round, and a game still downloading is no fault.
func (s *shareBook[R, E]) prepareMember(ctx context.Context, m *Manager, p sharePlace) {
	out, err := s.run(ctx, m, p, s.prepare, p.shared)
	if err != nil {
		m.log.Warn("library: "+s.name+" could not be prepared", "member", p.member.Name, "err", err)

		return
	}

	var res struct {
		Error  string `json:"error"`
		Status string `json:"status"`
		Stores []struct {
			Kind    string   `json:"kind"`
			Moved   []string `json:"moved"`
			Linked  bool     `json:"linked"`
			Problem string   `json:"problem"`
		} `json:"stores"`
	}

	if err := json.Unmarshal([]byte(lastLine(out)), &res); err != nil || res.Error != "" {
		m.log.Warn("library: "+s.name+" could not be prepared", "member", p.member.Name,
			"err", res.Error, "out", lastLine(out))

		return
	}

	for _, st := range res.Stores {
		for _, name := range st.Moved {
			m.moved(p.member.Name, "%s moved out of %s into the shared folders", name, storeNames[st.Kind])
		}

		if st.Linked {
			m.moved(p.member.Name, "%s now installs into the shared folders", storeNames[st.Kind])
		}

		if st.Problem != "" {
			m.log.Info("library: a store's games stay where they are for now",
				"member", p.member.Name, "store", st.Kind, "why", st.Problem)
		}
	}
}

func (s *shareBook[R, E]) scan(ctx context.Context, m *Manager, p sharePlace) (shareScanResult[E], error) {
	var result shareScanResult[E]

	out, err := s.run(ctx, m, p, "scan", p.root, p.shared)
	if err != nil {
		return result, err
	}

	if err := json.Unmarshal([]byte(lastLine(out)), &result); err != nil {
		return result, fmt.Errorf("the helper answered with something that is not a scan: %w", err)
	}

	if result.Error != "" {
		return result, errors.New(result.Error)
	}

	return result, nil
}

func (s *shareBook[R, E]) call(ctx context.Context, m *Manager, p sharePlace, args ...string) (shareResult, error) {
	var result shareResult

	out, err := s.run(ctx, m, p, args...)
	if err != nil {
		return result, err
	}

	if err := json.Unmarshal([]byte(lastLine(out)), &result); err != nil {
		return result, fmt.Errorf("the helper answered with something that is not an answer: %w", err)
	}

	if result.Error != "" {
		return result, errors.New(result.Error)
	}

	return result, nil
}

// run runs the helper as the member's person: in a seat as the player, on the
// host as the owner of the library and never as root.
func (s *shareBook[R, E]) run(ctx context.Context, m *Manager, p sharePlace, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, shareLimit)
	defer cancel()

	if p.member.Name != hostMember {
		out, code, err := look(ctx, m.client, p.member.Name, shareLimit,
			pythonScan(shareLimit, s.script, args...)...)
		if err != nil {
			return out, err
		}

		if code != 0 && lastLine(out) == "" {
			return out, fmt.Errorf("the helper exited %d", code)
		}

		return out, nil
	}

	owner := p.member.Owner
	if owner.UID < minOwnerUID {
		return "", fmt.Errorf("%w: the library belongs to uid %d", errSystemOwner, owner.UID)
	}

	who := ownerOf(owner.UID)
	if who == nil || who.HomeDir == "" {
		return "", fmt.Errorf("uid %d has no passwd entry to take a home from", owner.UID)
	}

	env := []string{
		"HOME=" + who.HomeDir,
		"USER=" + who.Username,
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}

	argv := append([]string{"python3", "-I", "-c", s.script}, args...)

	out, err := runBounded(ctx, who.HomeDir, argv, env, &syscall.Credential{
		Uid: uint32(owner.UID),
		Gid: uint32(owner.GID),
	})

	// The helper prints its own refusal and exits non-zero with it; that is an
	// answer, not a failure to run.
	if err != nil && lastLine(out) == "" {
		return out, err
	}

	return out, nil
}

// stampOf is what changes when the host's launcher does.
func (s *shareBook[R, E]) stampOf(member library.Member) string {
	who := ownerOf(member.Owner.UID)
	if who == nil {
		return ""
	}

	var b strings.Builder

	for _, rel := range s.stamp {
		info, err := os.Stat(filepath.Join(who.HomeDir, rel))
		if err != nil {
			b.WriteString("-;")

			continue
		}

		b.WriteString(strconv.FormatInt(info.ModTime().UnixNano(), 10))
		b.WriteString(":")
		b.WriteString(strconv.FormatInt(info.Size(), 10))
		b.WriteString(";")
	}

	return b.String()
}

// ---------------------------------------------------------------- interface

// HostGame is one game in a launcher on the host, as the Library section shows
// it.
type HostGame struct {
	// Key is what the launcher calls it: Lutris's slug, Faugus's game id. It
	// is what the Share button hands back.
	Key  string `json:"key"`
	Name string `json:"name"`

	// Folder is the directory its files are in: below ~/Games/shared when
	// Shared is set, below ~/Games otherwise.
	Folder string `json:"folder,omitempty"`
	Shared bool   `json:"shared"`

	// Problem says why it cannot be shared by moving it, when it cannot.
	Problem string `json:"problem,omitempty"`
}

// shareMoveRefused are the directories below ~/Games that are never moved,
// because they are not one game. umu keeps the default prefix every Proton
// game without one of its own runs in.
var shareMoveRefused = map[string]bool{"shared": true, "umu": true}

// hostGamesOf is hostGames for a launcher that may have no book, which is
// every launcher when there is no pool.
func hostGamesOf[R shareRecipe[R], E shareEntry[R]](m *Manager, s *shareBook[R, E]) []HostGame {
	if s == nil {
		return []HostGame{}
	}

	return s.hostGames(m)
}

// hostGames lists the host's games in this launcher for the interface.
//
// The interface reads the library on every change the daemon pushes, so the
// listing is taken again only when the host's launcher has changed since.
func (s *shareBook[R, E]) hostGames(m *Manager) []HostGame {
	out := []HostGame{}

	if m.pool == nil {
		return out
	}

	place := m.hostPlace()
	if place == nil || place.member.Owner.UID < minOwnerUID {
		return out
	}

	stamp := s.stampOf(place.member) + place.shared

	s.mu.Lock()
	if stamp == s.hostShown && s.host != nil {
		games := s.host
		s.mu.Unlock()

		return games
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	result, err := s.scan(ctx, m, *place)
	if err != nil || !result.Present {
		return out
	}

	for _, e := range result.Games {
		if e.written() {
			continue
		}

		game := HostGame{Key: e.key(), Name: e.title()}

		folder, home := e.place()

		switch {
		case folder != "":
			game.Folder, game.Shared = folder, true
		case home == "":
			game.Problem = "its files are not in a folder of their own below " + place.root
		case shareMoveRefused[home]:
			game.Folder = home
			game.Problem = "it lives in " + filepath.Join(place.root, home) +
				", which is more than this one game"
		default:
			game.Folder = home
		}

		out = append(out, game)
	}

	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })

	s.mu.Lock()
	s.host, s.hostShown = out, stamp
	s.mu.Unlock()

	return out
}

// repointHost points the host's entries in this launcher from a folder's old
// place to its new one, after another launcher moved it. Said in the log when
// it fails rather than failing the move, which has happened by then.
func (s *shareBook[R, E]) repointHost(ctx context.Context, m *Manager, old, new string) {
	place := m.hostPlace()
	if place == nil {
		return
	}

	res, err := s.call(ctx, m, *place, "repoint", old, new)
	if err != nil {
		m.log.Warn("library: a moved folder is still named by its old place in "+s.name,
			"old", old, "new", new, "err", err)

		return
	}

	if res.Status == "repointed" {
		m.log.Info("library: "+s.name+" on this host follows a moved folder", "new", new)
	}
}

// ErrNoSharedDir is returned when the host has no ~/Games/shared to share
// into, which is the switch for the host's side of the folders.
var ErrNoSharedDir = errors.New("the host takes no part in the shared folders: create ~/Games/shared first")

// shareHost moves one game of the host's launcher into ~/Games/shared, points
// its entries at the new place, and runs a pass so it starts on its way to the
// seats.
//
// A rename and never a copy: the directory and ~/Games/shared are on one
// filesystem, the second being inside the first's parent, and a game is only
// ever moved while nothing on the host has its files open.
func (s *shareBook[R, E]) shareHost(ctx context.Context, m *Manager, key string) error {
	if m.pool == nil {
		return fmt.Errorf("%w: %s", ErrNoLibrary, m.libraryErr)
	}

	if !s.validKey(key) {
		return fmt.Errorf("%q is not something %s calls a game", key, s.name)
	}

	place := m.hostPlace()
	if place == nil {
		return ErrNoSharedDir
	}

	m.syncMu.Lock()

	result, err := s.scan(ctx, m, *place)
	if err != nil {
		m.syncMu.Unlock()

		return err
	}

	var game *E

	for i := range result.Games {
		if result.Games[i].key() == key && !result.Games[i].written() {
			game = &result.Games[i]

			break
		}
	}

	var folder, home string
	if game != nil {
		folder, home = (*game).place()
	}

	switch {
	case game == nil:
		err = fmt.Errorf("%w: %s on this host has no game %s", os.ErrNotExist, s.name, key)
	case folder != "":
		err = fmt.Errorf("%s is shared already, from %s", key, folder)
	case home == "" || shareMoveRefused[home]:
		err = fmt.Errorf("%s is not in a folder of its own below %s", key, place.root)
	case !hostIdle(filepath.Join(place.root, home), place.member.Owner.UID):
		err = fmt.Errorf("%w: %s is running on this host; close it first, its folder %s would move out from under it",
			ErrBusy, (*game).title(), home)
	}

	if err != nil {
		m.syncMu.Unlock()

		return err
	}

	res, err := s.call(ctx, m, *place, "move", place.root, place.shared, key)

	m.syncMu.Unlock()

	if err != nil {
		return err
	}

	m.log.Info("library: a "+s.name+" game was moved into the shared folders",
		"game", key, "folder", res.Folder)

	// Every launcher, not only this one: a folder that moves may be one
	// another launcher on the host points into as well, and that one's entry
	// would go on naming the old place.
	for _, other := range m.shares() {
		if other != shareCarrier(s) && res.Old != "" && res.New != "" {
			other.repointHost(ctx, m, res.Old, res.New)
		}

		other.markAll()
	}

	m.notify()

	go m.syncLibrary(context.Background())

	return nil
}
