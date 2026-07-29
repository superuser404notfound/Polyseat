package seat

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// CatalogEntry is something the web interface offers to install into a seat.
type CatalogEntry struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

// Catalog is the short list, not a copy of Flathub.
//
// A seat is a games machine, so the useful set is small enough to name: the
// launchers Polyseat can also put in the Moonlight app list. Anything else is
// installable by typing its application id, which is why this is a starting
// point rather than a limit.
//
// What is missing from it is deliberate. Steam, Lutris and Firefox are
// installed into every seat as ordinary packages, so offering them again as
// flatpaks would be a second copy of something already there, taking hundreds
// of megabytes per seat to do the same job worse: a sandboxed launcher has to
// be told separately that it may see the shared library.
//
// The ones that stay are the ones with no package in the distribution, or none
// worth preferring, and the ones not everybody wants. Heroic alone is close to
// a gigabyte once its runtime is counted, and a seat belonging to somebody who
// only plays on Steam should not carry it.
var Catalog = []CatalogEntry{
	{"com.heroicgameslauncher.hgl", "Heroic", "Epic, GOG and Amazon games"},
	{"com.usebottles.bottles", "Bottles", "Runs Windows software in prefixes"},
	{"io.itch.itch", "itch", "The itch.io library"},
	{"org.prismlauncher.PrismLauncher", "Prism Launcher", "Minecraft"},
	{"org.libretro.RetroArch", "RetroArch", "Emulators, one interface"},
	{"net.davidotek.pupgui2", "ProtonUp-Qt", "Installs Proton-GE for Lutris and Heroic"},
	{"com.discordapp.Discord", "Discord", "Voice chat while playing"},
}

// Installed is one flatpak that is really in a seat.
type Installed struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size string `json:"size"`
}

// SoftwareStatus is what the interface needs to draw the software section.
type SoftwareStatus struct {
	// Available is whether anything can be installed right now. False for a
	// seat that is switched off or was built before Polyseat installed
	// flatpak, and Problem says which.
	Available bool   `json:"available"`
	Problem   string `json:"problem,omitempty"`

	Installed []Installed    `json:"installed"`
	Catalog   []CatalogEntry `json:"catalog"`
}

// flatpakID is the reverse DNS form Flathub uses.
//
// Checked before anything is run with it, even though every call here is an
// argv list and never a shell string. The argv list is what makes a crafted id
// harmless; this is what makes a typo say so instead of producing a flatpak
// error nobody can read.
var flatpakID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*(\.[A-Za-z][A-Za-z0-9_-]*){2,}$`)

// ValidateAppID checks a proposed application id.
func ValidateAppID(id string) error {
	if !flatpakID.MatchString(id) {
		return fmt.Errorf("an application id looks like com.example.Name, with at least three parts")
	}

	if len(id) > 255 {
		return fmt.Errorf("that application id is too long")
	}

	return nil
}

// Software reports what a seat has installed and what it can be given.
func (m *Manager) Software(ctx context.Context, name string) (SoftwareStatus, error) {
	status := SoftwareStatus{
		Installed: []Installed{},
		Catalog:   Catalog,
	}

	if _, err := m.store.Get(name); err != nil {
		return status, err
	}

	state, err := m.client.Status(name)
	if err != nil {
		return status, err
	}

	if !strings.EqualFold(state, "running") {
		status.Problem = "the seat has to be running before software can be installed"

		return status, nil
	}

	out, code, err := m.client.Try(ctx, name, m.playerEnv(
		"flatpak", "list", "--user", "--app", "--columns=application,name,size")...)
	if err != nil {
		return status, err
	}

	if code != 0 {
		status.Problem = "this seat has no flatpak yet, provision it again to add it"

		return status, nil
	}

	status.Available = true

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}

		fields := strings.Split(line, "\t")
		if len(fields) < 1 || fields[0] == "" {
			continue
		}

		entry := Installed{ID: fields[0], Name: fields[0]}

		if len(fields) > 1 && fields[1] != "" {
			entry.Name = fields[1]
		}

		if len(fields) > 2 {
			entry.Size = fields[2]
		}

		status.Installed = append(status.Installed, entry)
	}

	return status, nil
}

// searchResults is how many matches are worth showing.
//
// A search for something common comes back with dozens, and a list that long
// in a panel is not a list any more. Whoever wants the rest can be more
// specific, which is cheaper than scrolling.
const searchResults = 25

// SearchSoftware asks Flathub what matches a few words.
//
// Because the catalog is a short list and the field next to it used to want an
// exact application id, which is knowledge nobody has: somebody looking for
// Minecraft does not know it is called com.mojang.Minecraft, and there is no
// way to find that out from inside this interface.
//
// The search runs in the seat rather than against Flathub from the host. The
// seat is the machine that has the remote configured and the summary already
// downloaded, and asking it is the only way to be sure the answer matches what
// that seat could actually install.
func (m *Manager) SearchSoftware(ctx context.Context, name, term string) ([]CatalogEntry, error) {
	found := []CatalogEntry{}

	term = strings.TrimSpace(term)
	if term == "" {
		return found, nil
	}

	if len(term) > 100 {
		return found, fmt.Errorf("that search is too long")
	}

	// Every call here is an argv list, so a crafted term cannot become a
	// command. This is about the answer being useful: a term with a newline in
	// it would be split across the tab separated output and parsed as nonsense.
	if strings.ContainsAny(term, "\n\r\t") {
		return found, fmt.Errorf("a search cannot contain line breaks")
	}

	if _, err := m.store.Get(name); err != nil {
		return found, err
	}

	out, code, err := m.client.Try(ctx, name, m.playerEnv(
		"flatpak", "search", "--columns=name,description,application", term)...)
	if err != nil {
		return found, err
	}

	// flatpak says "No matches found" on stderr and exits non-zero for a term
	// that matches nothing. That is an answer, not a failure.
	if code != 0 {
		return found, nil
	}

	return parseSearch(out), nil
}

// parseSearch reads the tab separated columns flatpak prints.
//
// Separate from running it so that the awkward part can be tested: the output
// is somebody's application description, which may contain almost anything,
// and a line that does not have three columns has to be dropped rather than
// turned into an entry with an id made of prose. An id that reaches the
// interface is one somebody can press Install on.
func parseSearch(out string) []CatalogEntry {
	found := []CatalogEntry{}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}

		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}

		id := strings.TrimSpace(fields[2])
		if ValidateAppID(id) != nil {
			continue
		}

		entry := CatalogEntry{
			ID:      id,
			Name:    strings.TrimSpace(fields[0]),
			Summary: strings.TrimSpace(fields[1]),
		}

		if entry.Name == "" {
			entry.Name = id
		}

		found = append(found, entry)

		if len(found) == searchResults {
			break
		}
	}

	return found
}

// playerEnv wraps a command so it runs as the player with a home directory.
//
// Both halves matter. A flatpak installed with --user lives under the player's
// home, so running any of this as root reports the system installation, which
// in a seat is empty; and flatpak without HOME set does not know where that is.
func (m *Manager) playerEnv(argv ...string) []string {
	return append([]string{"sudo", "-u", Player, "env", "HOME=/home/" + Player}, argv...)
}

// InstallSoftware puts a flatpak into a seat.
//
// Installed into the player's own installation rather than system wide, so
// that what the daemon installs and what the player installs are the same
// thing in the same place. The alternative, a system installation only the
// daemon can write, would mean two lists of software with different rules, and
// the player being unable to remove something they can see.
func (m *Manager) InstallSoftware(name, id string) error {
	if err := ValidateAppID(id); err != nil {
		return err
	}

	if _, err := m.store.Get(name); err != nil {
		return err
	}

	return m.operate(name, "installing "+id, func(ctx context.Context) error {
		watch := &installWatch{m: m, seat: name}

		// Under a pseudo terminal, because that is the only way flatpak says
		// how far it has got. Given --noninteractive, or simply no terminal, it
		// prints two lines naming what it is about to do and then nothing at
		// all until it is finished, which for a download of several hundred
		// megabytes is indistinguishable from being stuck. `script` provides
		// the terminal; the percentages arrive on the way.
		//
		// This is the one place a command is built as a shell string rather
		// than an argv list. What goes into it is an application id that has
		// already been through ValidateAppID, which allows letters, digits,
		// hyphens, underscores and dots and nothing else.
		command := fmt.Sprintf(
			"flatpak install --user --assumeyes flathub %s", id)

		code, err := m.client.Exec(ctx, name, m.playerEnv(
			"script", "-qec", command, "/dev/null"), nil, watch, watch)
		if err != nil {
			return err
		}

		if code != 0 {
			return fmt.Errorf("%s could not be installed: %s", id, watch.tail())
		}

		m.logf(name, "%s is installed", id)

		// An installation is also the moment a seat can gain a runtime it did
		// not have, and the framerate cap only reaches inside a sandbox if the
		// matching MangoHud layer is there. Doing this here rather than only
		// while provisioning is what covers the ordinary case: a seat is built
		// with no flatpaks at all and the first launcher arrives later.
		m.layerForFlatpaks(ctx, name)

		// So it appears in Moonlight without waiting for the next restart. The
		// app list is generated from what is installed, and this is the moment
		// that changed.
		m.refreshApps(ctx, name)

		return nil
	})
}

// RemoveSoftware takes a flatpak back out of a seat.
func (m *Manager) RemoveSoftware(name, id string) error {
	if err := ValidateAppID(id); err != nil {
		return err
	}

	if _, err := m.store.Get(name); err != nil {
		return err
	}

	return m.operate(name, "removing "+id, func(ctx context.Context) error {
		out, code, err := m.client.Try(ctx, name, m.playerEnv(
			"flatpak", "uninstall", "--user", "--assumeyes", "--noninteractive", id)...)
		if err != nil {
			return err
		}

		if code != 0 {
			return fmt.Errorf("%s could not be removed: %s", id, lastLines(out, 3))
		}

		m.logf(name, "%s is gone", id)

		// Unused runtimes are what makes a seat quietly grow: removing the last
		// application that needed a two gigabyte platform leaves the platform
		// behind. Reported rather than failed, because the application really
		// is gone either way.
		if _, code, err := m.client.Try(ctx, name, m.playerEnv(
			"flatpak", "uninstall", "--user", "--unused",
			"--assumeyes", "--noninteractive")...); err != nil || code != 0 {
			m.logf(name, "! unused runtimes could not be cleaned up, they are only taking space")
		}

		m.refreshApps(ctx, name)

		return nil
	})
}

// layerForFlatpaks makes sure the framerate cap reaches sandboxed applications.
func (m *Manager) layerForFlatpaks(ctx context.Context, name string) {
	s, err := m.store.Get(name)
	if err != nil {
		return
	}

	p := &Provisioner{
		Client: m.client,
		Seat:   s,
		Image:  m.cfg.Image,
		Log:    func(f string, a ...any) { m.logf(name, f, a...) },
	}

	p.flatpakMangoHud(ctx)
}

// refreshApps rewrites the Moonlight app list for a running seat.
//
// Quiet unless something actually changed, because this also runs on the
// periodic sweep and a line every ten seconds would both fill the seat's log
// and wake the interface each time.
func (m *Manager) refreshApps(ctx context.Context, name string) {
	s, err := m.store.Get(name)
	if err != nil {
		return
	}

	p := &Provisioner{
		Client: m.client,
		Seat:   s,
		Image:  m.cfg.Image,
		Log:    func(f string, a ...any) { m.logf(name, f, a...) },
		uid:    m.runtimeOf(name).uid,
	}

	apps, changed, err := p.WriteApps(ctx)
	if err != nil {
		m.logf(name, "! the app list could not be updated: %v", err)

		return
	}

	if !changed {
		return
	}

	m.logf(name, "Moonlight will offer: %s", strings.Join(apps, ", "))

	// Writing the file is not enough on its own. Sunshine reads it once, at
	// startup, for the list it serves to clients; its own web interface rereads
	// it on every request, which is exactly what made this look solved when it
	// was not. Every check the daemon made agreed with the file while Moonlight
	// went on showing what Sunshine had loaded hours before, so a game
	// uninstalled in a seat stayed in the list until the seat restarted.
	//
	// Asking it to reload rather than restarting it, because a restart drops
	// whatever somebody is streaming, and this can happen while they are.
	client, err := m.sunshineClient(name)
	if err != nil {
		m.logf(name, "! the app list changed but Sunshine could not be told: %v", err)

		return
	}

	if err := client.ReloadApps(ctx); err != nil {
		m.logf(name, "! Sunshine did not reload the app list, it will show the "+
			"old one until the seat restarts: %v", err)
	}
}

// ansi strips the escape sequences a terminal application writes.
//
// There is a terminal here only because flatpak will not report progress
// without one, so everything it draws with arrives wrapped in cursor moves and
// bold markers that would otherwise be parsed as part of the numbers.
var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

// step matches "Installing 2/4… ████ 63%", which is the useful line.
//
// Both halves of it matter. The figure on its own runs to a hundred and starts
// again for the next thing being installed, so a bar following it fills up
// several times over: an application and its runtime are separate steps, and
// the runtime is usually most of the download.
var step = regexp.MustCompile(`(?i)(?:Installing|Updating)\s+(\d+)/(\d+)\D+?(\d{1,3})%`)

// plan matches a row of the table flatpak prints before it starts, which is
// where the sizes are:
//
//  3. org.gnome.Platform  50  i  flathub  < 409.7 MB
var plan = regexp.MustCompile(`(?m)^\s*(\d+)\.\s+\S+.*?([\d.]+)\s*([kKMGT]?B)\s*$`)

var units = map[string]float64{
	"B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12,
}

// installWatch reads an install as it happens.
//
// flatpak redraws one line with a carriage return rather than printing a new
// one, so this is a stream of overwrites rather than lines.
type installWatch struct {
	m    *Manager
	seat string

	mu    sync.Mutex
	last  []byte    // the end of the output, for saying what went wrong
	edge  []byte    // carried between writes, in case a line was split across two
	sizes []float64 // download size per step, from the table, when it is readable
	seen  int
}

func (w *installWatch) Write(p []byte) (int, error) {
	w.mu.Lock()

	chunk := ansi.ReplaceAll(append(append([]byte{}, w.edge...), p...), nil)

	// Enough to hold a line that arrived in two pieces, and no more.
	if len(chunk) > 256 {
		w.edge = append([]byte{}, chunk[len(chunk)-256:]...)
	} else {
		w.edge = append([]byte{}, chunk...)
	}

	w.last = append(w.last, p...)
	if len(w.last) > 8192 {
		w.last = w.last[len(w.last)-8192:]
	}

	if w.sizes == nil {
		w.sizes = planSizes(string(chunk))
	}

	value, ok := overall(string(chunk), w.sizes)

	changed := ok && value != w.seen
	if changed {
		w.seen = value
	}

	w.mu.Unlock()

	// Only on a change, or a hundred identical figures a second would wake the
	// interface a hundred times for nothing.
	if changed && w.m != nil {
		w.m.setProgress(w.seat, value)
	}

	return len(p), nil
}

// planSizes reads the download size of each step out of flatpak's table.
//
// Weighting by size rather than counting steps equally, because they are not
// remotely equal: installing one small application pulled a 1.6 MB locale, a
// 384 MB locale and a 409 MB runtime, so an evenly divided bar would have shot
// to a quarter and then crawled.
//
// Returns nothing when the table cannot be read, and the caller falls back to
// counting. A wrong bar is worse than a coarse one.
func planSizes(out string) []float64 {
	rows := plan.FindAllStringSubmatch(out, -1)
	if len(rows) == 0 {
		return nil
	}

	sizes := make([]float64, 0, len(rows))

	for i, row := range rows {
		// The rows are numbered, so a table read in the wrong order or with a
		// row missing is noticed rather than silently mixed up.
		if n, err := strconv.Atoi(row[1]); err != nil || n != i+1 {
			return nil
		}

		value, err := strconv.ParseFloat(row[2], 64)
		if err != nil {
			return nil
		}

		unit, ok := units[row[3]]
		if !ok {
			return nil
		}

		sizes = append(sizes, value*unit)
	}

	return sizes
}

// overall turns "Installing 2/4… 63%" into how far the whole install has got.
func overall(out string, sizes []float64) (int, bool) {
	found := step.FindAllStringSubmatch(out, -1)
	if len(found) == 0 {
		return 0, false
	}

	last := found[len(found)-1]

	index, err1 := strconv.Atoi(last[1])
	total, err2 := strconv.Atoi(last[2])
	within, err3 := strconv.Atoi(last[3])

	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}

	if index < 1 || total < 1 || index > total || within < 0 || within > 100 {
		return 0, false
	}

	fraction := float64(within) / 100

	// By size when the table was readable, by step count otherwise.
	if len(sizes) == total {
		var done, all float64

		for i, size := range sizes {
			all += size

			switch {
			case i < index-1:
				done += size
			case i == index-1:
				done += size * fraction
			}
		}

		if all > 0 {
			return clamp(int(done / all * 100)), true
		}
	}

	return clamp(int((float64(index-1) + fraction) / float64(total) * 100)), true
}

func clamp(value int) int {
	if value < 0 {
		return 0
	}

	if value > 100 {
		return 100
	}

	return value
}

// tail is the end of the output, for an error message.
func (w *installWatch) tail() string {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Carriage returns would put the whole progress bar on one line of the log.
	text := strings.ReplaceAll(string(w.last), "\r", "\n")

	return lastLines(text, 3)
}

// setProgress records how far an operation has got and tells the interface.
func (m *Manager) setProgress(name string, value int) {
	rt := m.runtimeOf(name)

	m.mu.Lock()
	changed := rt.progress != value
	rt.progress = value
	m.mu.Unlock()

	if changed {
		m.notify()
	}
}
