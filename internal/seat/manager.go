package seat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/superuser404notfound/Polyseat/internal/config"
	"github.com/superuser404notfound/Polyseat/internal/incusx"
	"github.com/superuser404notfound/Polyseat/internal/library"
	"github.com/superuser404notfound/Polyseat/internal/sunshine"
	"github.com/superuser404notfound/Polyseat/internal/supervise"
)

// devicePrefix marks the input device attachments the broker owns.
const devicePrefix = "in-"

// Manager owns every seat: what it is, whether it runs, and the processes that
// belong to it.
//
// One rule runs through the whole type. The manager never asks Incus what state
// a container is in on a timer; it learns from the lifecycle event stream. The
// only polling left is inside a container that is known to be running, at a
// gentle interval, to read what the session is doing. The M2 prototype polled
// `incus exec` twice a second regardless of state, an exec landed inside a
// shutdown, and the Incus daemon hung in "Stopping instance" with the container
// already gone.
type Manager struct {
	cfg    config.Config
	client *incusx.Client
	store  *Store
	log    *slog.Logger

	mu sync.Mutex
	rt map[string]*runtime

	observer *supervise.Process

	// pool is the shared game library, nil when the filesystem cannot share
	// blocks. libraryErr says why in that case, so the interface can show a
	// reason instead of an absence.
	pool       *library.Pool
	libraryErr string

	// syncMu serialises library work. The timer and the interface's own
	// buttons both start passes, and two of them cloning into the same seat at
	// once would race over the same directories.
	syncMu sync.Mutex

	subsMu sync.Mutex
	subs   map[int]chan struct{}
	nextID int

	// sweeping is set while every stale seat is being provisioned in turn, so
	// that pressing the button twice does not start two sweeps over the same
	// seats.
	sweeping bool
}

// runtime is everything about a seat that is observed rather than stored.
type runtime struct {
	state   State
	busy    string
	lastErr string
	cancel  context.CancelFunc

	log    *Log
	broker *supervise.Process

	uid int64

	container string
	addresses map[string][]string

	// configOrigins is what the seat's Sunshine configuration says, and
	// runningOrigins is what the Sunshine that is actually running was started
	// with. They differ exactly when a seat's address moved under it, which
	// under DHCP happens on its own and used to go unnoticed until somebody
	// tried to save something in that seat's own web interface.
	configOrigins  []string
	runningOrigins []string
	notes          []string
	sway           string
	sunshine       string
	encoder        string
	codecs         []string
	output         string
	session        *Session
	devices        []string
	checked        time.Time

	// appsChecked is when the Moonlight app list was last rebuilt from what the
	// seat has installed. Its own clock because that scan is the expensive one:
	// it reads Steam's manifests and asks Lutris, which is a GTK application
	// even when all it does is print a list. On the sweep's own ten seconds
	// that would be a steady load on a machine meant to be playing games, and
	// nobody needs to learn within ten seconds that a game was uninstalled.
	appsChecked time.Time

	// appsPending records that the app list wants rebuilding but somebody is
	// streaming, so it has to wait for them to finish.
	appsPending bool

	// progress is how far a long operation has got, 0 to 100, or -1 when
	// there is nothing to say. Only installing software reports it: that is
	// the operation whose length depends on somebody else's server rather than
	// on a recipe, so a spinner and a line of text leave you guessing whether
	// anything is happening at all.
	progress int
}

// NewManager prepares the manager. Nothing is started yet; see Run.
func NewManager(cfg config.Config, client *incusx.Client, store *Store, logger *slog.Logger) *Manager {
	return &Manager{
		cfg:    cfg,
		client: client,
		store:  store,
		log:    logger,
		rt:     map[string]*runtime{},
		subs:   map[int]chan struct{}{},
	}
}

// Run starts the observer, brings up the seats marked for autostart and follows
// the Incus event stream until the context ends.
func (m *Manager) Run(ctx context.Context) error {
	// The uhid observer needs somewhere to keep its record of which container
	// created which HID device. It used to get this from the unit's
	// RuntimeDirectory.
	if err := os.MkdirAll("/run/polyseat", 0o755); err != nil {
		return err
	}

	m.openLibrary()

	m.startObserver()
	defer m.observer.Stop()

	seats, err := m.store.List()
	if err != nil {
		return err
	}

	for _, s := range seats {
		m.runtimeOf(s.Name)
	}

	m.reconcileAll(ctx)

	for _, s := range seats {
		if !s.Autostart {
			continue
		}

		// Autostart brings up what exists. It does not build, even though Start
		// does now: building takes minutes, downloads an image and installs a
		// distribution's worth of packages, and a daemon restart is not somebody
		// asking for that. Pressing Start in the interface is.
		if s.Provisioned == 0 {
			m.log.Info("not autostarting a seat that has never been built", "seat", s.Name)
			m.logf(s.Name, "not built yet, so not started with the daemon. Press Start when you want it built")

			continue
		}

		m.log.Info("autostarting seat", "seat", s.Name)
		_ = m.Start(s.Name)
	}

	events, err := m.client.Lifecycles(ctx)
	if err != nil {
		return fmt.Errorf("subscribe to Incus events: %w", err)
	}

	// The slow tick exists for what events cannot say: whether the session
	// inside a running seat is healthy and which encoder Sunshine picked.
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()

	// The library is on its own, slower timer. It is filesystem work rather
	// than a look at a container, and running it at the reconcile interval
	// would scan every seat's library six times a minute for no benefit.
	sync := time.NewTicker(syncInterval)
	defer sync.Stop()

	for {
		select {
		case <-ctx.Done():
			m.shutdown()

			return nil

		case ev, ok := <-events:
			if !ok {
				return fmt.Errorf("the Incus event stream ended")
			}

			m.onLifecycle(ctx, ev)

		case <-tick.C:
			m.reconcileAll(ctx)

		case <-sync.C:
			m.syncLibrary(ctx)
		}
	}
}

func (m *Manager) shutdown() {
	// Brokers are stopped, containers are not. A daemon restart should not
	// interrupt anybody's game; the seats keep running and are picked up again
	// on the way back in.
	m.mu.Lock()
	brokers := make([]*supervise.Process, 0, len(m.rt))

	for _, rt := range m.rt {
		if rt.broker != nil {
			brokers = append(brokers, rt.broker)
		}
	}

	m.mu.Unlock()

	for _, b := range brokers {
		b.Stop()
	}
}

// ------------------------------------------------------------------ observer

func (m *Manager) startObserver() {
	m.observer = supervise.New([]string{
		m.cfg.Python, "-u", m.cfg.HelperDir + "/uhid_observer.py",
	})

	m.observer.OnOutput = func(line string) {
		m.log.Info("uhid observer", "line", line)
	}

	m.observer.OnState = func(state supervise.State) {
		m.log.Info("uhid observer", "state", string(state))
		m.notify()
	}

	m.observer.Start()
}

// ObserverState reports how the host wide uhid observer is doing. Without it
// gamepads cannot be attributed to a seat structurally, only by name, so the
// interface has to be able to say when it is down.
func (m *Manager) ObserverState() string {
	if m.observer == nil {
		return string(supervise.Stopped)
	}

	return string(m.observer.State())
}

// ------------------------------------------------------------------- runtime

func (m *Manager) runtimeOf(name string) *runtime {
	m.mu.Lock()
	defer m.mu.Unlock()

	rt, ok := m.rt[name]
	if !ok {
		rt = &runtime{state: StateAbsent, log: NewLog(400), uid: 1000, progress: -1}
		m.rt[name] = rt
	}

	return rt
}

func (m *Manager) setState(name string, state State) {
	rt := m.runtimeOf(name)

	m.mu.Lock()
	changed := rt.state != state
	rt.state = state
	m.mu.Unlock()

	if changed {
		m.notify()
	}
}

// Log returns the seat's recent log lines.
func (m *Manager) Log(name string) []string {
	return m.runtimeOf(name).log.Lines()
}

func (m *Manager) logf(name, format string, args ...any) {
	rt := m.runtimeOf(name)
	rt.log.Add(fmt.Sprintf(format, args...))
	m.notify()
}

// ------------------------------------------------------------------- notify

// Subscribe returns a channel that receives a token whenever anything changes,
// so the web interface can hold one stream open instead of polling.
func (m *Manager) Subscribe() (<-chan struct{}, func()) {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()

	id := m.nextID
	m.nextID++
	ch := make(chan struct{}, 1)
	m.subs[id] = ch

	return ch, func() {
		m.subsMu.Lock()
		defer m.subsMu.Unlock()

		delete(m.subs, id)
		close(ch)
	}
}

func (m *Manager) notify() {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()

	for _, ch := range m.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// ------------------------------------------------------------------- events

func (m *Manager) onLifecycle(ctx context.Context, ev incusx.Lifecycle) {
	// Incus emits a lifecycle event for every exec too, and the daemon execs
	// into every running seat to read its session. Reacting to those would make
	// each read cause the next one: the first run of this produced a hundred
	// events in ten seconds, all of them the daemon watching itself. Only the
	// four actions that actually change what a seat is get through.
	switch ev.Action {
	case "instance-started", "instance-shutdown", "instance-stopped", "instance-deleted":
	default:
		return
	}

	if _, err := m.store.Get(ev.Instance); err != nil {
		return // not one of ours
	}

	m.log.Info("lifecycle", "seat", ev.Instance, "action", ev.Action)

	// An operation of ours owns the seat's state while it runs, and
	// provisioning stops and starts the container as part of its work. Acting
	// on those events would be the daemon reacting to itself: reconcile below
	// already refuses to, but the state was being overwritten before it got
	// that far, so a seat halfway through provisioning flipped between building
	// and starting several times a second.
	rt := m.runtimeOf(ev.Instance)

	m.mu.Lock()
	busy := rt.busy
	m.mu.Unlock()

	if busy != "" {
		return
	}

	switch ev.Action {
	case "instance-shutdown", "instance-stopped":
		// Whether the daemon asked for this or somebody typed `incus stop`,
		// the broker has no container to talk to any more.
		m.stopBroker(ev.Instance)
		m.setState(ev.Instance, StateStopped)

	case "instance-started":
		// Only note it. A seat started from outside the daemon has no session
		// and no broker, and the reconcile below is what says so.
		m.setState(ev.Instance, StateStarting)

	case "instance-deleted":
		m.stopBroker(ev.Instance)
		m.setState(ev.Instance, StateAbsent)
	}

	m.reconcile(ctx, ev.Instance)
}

// ---------------------------------------------------------------- reconcile

func (m *Manager) reconcileAll(ctx context.Context) {
	seats, err := m.store.List()
	if err != nil {
		m.log.Error("read the seat store", "error", err)

		return
	}

	for _, s := range seats {
		m.reconcile(ctx, s.Name)
	}
}

// reconcile refreshes what is observed about a seat.
//
// It deliberately does not act. Bringing a seat back up after it stopped by
// itself is a decision, not a repair, and the interface shows it instead.
func (m *Manager) reconcile(ctx context.Context, name string) {
	rt := m.runtimeOf(name)

	m.mu.Lock()
	busy := rt.busy
	state := rt.state
	m.mu.Unlock()

	// Never talk to a container that is being built, started or stopped by an
	// operation of ours. That operation knows what it is doing and this would
	// only race with it.
	if busy != "" || state == StateStopping {
		return
	}

	status, err := m.client.Status(name)
	if err != nil {
		m.log.Error("read the container status", "seat", name, "error", err)

		return
	}

	m.mu.Lock()
	rt.container = status
	m.mu.Unlock()

	switch status {
	case "":
		m.stopBroker(name)
		m.setState(name, StateAbsent)

		return
	case "Running":
	default:
		m.stopBroker(name)
		m.setState(name, StateStopped)

		return
	}

	m.refreshSession(ctx, name)
}

// refreshSession reads what the session inside a running seat is doing.
func (m *Manager) refreshSession(ctx context.Context, name string) {
	rt := m.runtimeOf(name)

	addresses, err := m.client.Addresses(name)
	if err == nil {
		m.mu.Lock()
		rt.addresses = addresses
		m.mu.Unlock()
	}

	sway := m.unitState(ctx, name, "polyseat-sway.service")
	sunshine := m.unitState(ctx, name, "polyseat-sunshine.service")

	devices, err := m.attachedDevices(name)
	if err != nil {
		m.log.Error("read the attached devices", "seat", name, "error", err)
	}

	encoder, codecs, output := "", []string(nil), ""

	var session *Session

	if sunshine == "active" {
		encoder, codecs = m.readEncoders(ctx, name)
		output = m.readOutput(ctx, name)
		session = m.readSession(ctx, name)
	}

	m.checkOrigins(ctx, name, addresses)

	up := sway == "active" && sunshine == "active"

	// The broker is the daemon's own child process, so bringing it back is
	// supervision rather than a decision about somebody's session. This is also
	// what adopts a seat that was already running when the daemon started, and
	// what puts a broker back after the daemon itself was restarted.
	if up {
		m.startBroker(name)

		// Keep the app list in step with what the seat has.
		//
		// The daemon writes it when a seat starts and when something is
		// installed through the interface, and neither covers the case that
		// actually happens: the player removing a launcher from inside the
		// seat, with nothing to tell the daemon. It stayed in Moonlight's list
		// afterwards and starting it did nothing.
		//
		// Never while somebody is streaming. Telling Sunshine to reload its app
		// list ends the stream in progress, and not politely: it emits no
		// CLIENT DISCONNECTED and runs none of the undo commands, so the seat is
		// left at the client's resolution with the framerate still capped. This
		// used to carry a comment saying a reload interrupts nothing, which was
		// an assumption rather than a measurement, and the measurement is a
		// Moonlight session ending mid-game one minute after a launcher was
		// installed.
		//
		// So it waits, and the moment the stream ends it happens.
		m.mu.Lock()
		streaming := session != nil
		due := time.Since(rt.appsChecked) >= appsInterval

		// Remembered only when it was actually due, so that the end of a stream
		// does not always drag an update behind it that nothing asked for.
		if due && streaming {
			rt.appsPending = true
		}

		if due && !streaming {
			rt.appsChecked = time.Now()
		}

		m.mu.Unlock()

		if due && !streaming {
			m.refreshApps(ctx, name)
		}
	}

	m.mu.Lock()
	rt.sway = sway
	rt.sunshine = sunshine
	rt.devices = devices
	rt.checked = time.Now()

	if encoder != "" {
		rt.encoder = encoder
		rt.codecs = codecs
	}

	rt.output = output

	// The end of a stream, seen by the daemon rather than reported by Sunshine.
	// Sunshine's own undo commands put the resolution and the framerate cap back,
	// and they do not run when a session ends abnormally, which the app list
	// reload above used to cause.
	ended := rt.session != nil && session == nil
	rt.session = session

	state := StateStarting
	if up {
		state = StateRunning
	}

	if ended {
		defer m.sessionEnded(ctx, name)
	}

	changed := rt.state != state
	rt.state = state
	m.mu.Unlock()

	if changed {
		m.notify()
	}
}

// checkOrigins notices a seat whose address moved under it.
//
// Sunshine's allowed web origins are generated from the seat's addresses, and
// under DHCP those change on their own. When they do, that seat's own web
// interface starts refusing every save with a CSRF error that gives no hint
// where it comes from. This is the whole reason a seat is better off with a
// fixed address.
//
// Two separate things are tracked, because they fail differently. The
// configuration on disk can be fixed here and now, so it is. What the running
// Sunshine loaded at startup cannot be, short of restarting it, and restarting
// Sunshine under somebody who is in the middle of a game is not a repair worth
// making on its own. So that one is reported instead.
func (m *Manager) checkOrigins(ctx context.Context, name string, addresses map[string][]string) {
	want := OriginsFor(addresses)
	if len(want) == 0 {
		return
	}

	rt := m.runtimeOf(name)

	m.mu.Lock()
	known := rt.configOrigins
	running := rt.runningOrigins
	m.mu.Unlock()

	// Nothing recorded means this daemon did not start the session: it adopted
	// a seat that was already running. Read what is actually in the seat rather
	// than guessing, once.
	if known == nil {
		conf, err := m.client.ReadFile(name, SunshineConfigPath)
		if err != nil {
			return
		}

		known = ParseOrigins(conf)
		running = known

		m.mu.Lock()
		rt.configOrigins = known
		rt.runningOrigins = running
		m.mu.Unlock()
	}

	if !sameStrings(want, known) {
		seat, err := m.store.Get(name)
		if err != nil {
			return
		}

		p := &Provisioner{
			Client: m.client,
			Seat:   seat,
			Image:  m.cfg.Image,
			Log:    func(f string, a ...any) { m.logf(name, f, a...) },
			uid:    rt.uid,
		}

		m.logf(name, "the address changed, rewriting the Sunshine configuration")

		origins, err := p.WriteSunshineConfig(ctx)
		if err != nil {
			m.logf(name, "! could not rewrite it: %v", err)

			return
		}

		m.mu.Lock()
		rt.configOrigins = origins
		m.mu.Unlock()
	}

	var notes []string
	if !sameStrings(want, running) {
		notes = append(notes, "This seat's address changed since Sunshine started. "+
			"Its own web interface will refuse to save anything until the seat is "+
			"restarted. A fixed address avoids this.")
	}

	m.mu.Lock()
	changed := !sameStrings(notes, rt.notes)
	rt.notes = notes
	m.mu.Unlock()

	if changed {
		m.notify()
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// quickTimeout bounds a call into a seat that is only ever a read and should
// answer at once.
//
// Bounded because one of them did not, and nothing above it noticed. An `incus
// exec` goes through the daemon's long lived connection to Incus, and when that
// connection stops delivering operation results the call waits for ever: two of
// them were found parked in WaitContext for twelve minutes while the same
// command from a shell answered instantly. The seat sat in "provisioning" with
// nothing in its log and nothing to press, which is the worst shape a failure
// can take. A deadline turns that into an error on the card.
const quickTimeout = 30 * time.Second

// quick derives a context for those calls. The caller's own cancellation still
// applies, so an operation somebody cancelled stops as it did before.
func quick(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, quickTimeout)
}

func (m *Manager) unitState(ctx context.Context, name, unit string) string {
	ctx, cancel := quick(ctx)
	defer cancel()

	out, _, err := m.client.Try(ctx, name, m.asPlayer(name, "systemctl", "--user", "is-active", unit)...)
	if err != nil {
		return "unknown"
	}

	return strings.TrimSpace(out)
}

// readEncoder reports which encoder Sunshine settled on.
//
// The single most useful line in the whole interface. A seat whose EGL landed
// on Mesa still starts, still streams and still looks healthy; it just encodes
// in software, and nobody finds out until a game stutters.
// readEncoders reports which hardware path Sunshine settled on and which
// codecs it can offer with it.
//
// The single most useful line in the whole interface is still whether the GPU
// path works: a seat that quietly fell back to software looks entirely healthy
// until somebody tries to play. But reporting only the H.264 encoder, which is
// what this did, reads as though H.264 were all a seat could do. Sunshine
// probes for three and offers whichever the client asks for, so the answer is
// a list.
func (m *Manager) readEncoders(ctx context.Context, name string) (string, []string) {
	ctx, cancel := quick(ctx)
	defer cancel()

	argv := m.asPlayer(name, "sh", "-c",
		"journalctl --user -u polyseat-sunshine.service --no-pager 2>/dev/null | "+
			"grep -oE 'Found (H\\.264|HEVC|AV1) encoder: [a-z0-9_]+'")

	out, _, err := m.client.Try(ctx, name, argv...)
	if err != nil {
		return "", nil
	}

	return parseEncoders(out)
}

// parseEncoders reads the lines Sunshine writes while probing.
//
// Separate from fetching them because a seat's journal holds every start it
// has ever had, and only the most recent probe describes what is running now:
// getting that backwards would report a card that has since been swapped, or a
// software fallback long after it was fixed.
func parseEncoders(out string) (string, []string) {
	seen := map[string]string{}
	order := []string{"H.264", "HEVC", "AV1"}

	for _, line := range strings.Split(out, "\n") {
		rest, found := strings.CutPrefix(strings.TrimSpace(line), "Found ")
		if !found {
			continue
		}

		codec, encoder, found := strings.Cut(rest, " encoder: ")
		if !found || encoder == "" {
			continue
		}

		// Later lines overwrite earlier ones, so what remains is the last run.
		seen[codec] = encoder
	}

	var codecs []string

	backend := ""

	for _, codec := range order {
		encoder, ok := seen[codec]
		if !ok {
			continue
		}

		codecs = append(codecs, codec)

		// Every codec of one run shares a backend, so the first says it.
		if backend == "" {
			if _, suffix, cut := strings.Cut(encoder, "_"); cut {
				backend = suffix
			} else {
				backend = encoder
			}
		}
	}

	return backend, codecs
}

// sessionEnded puts a seat back the way an idle seat should be, and does the
// work that was held back while somebody was streaming.
//
// Sunshine's own undo commands do this when a stream ends properly. They do not
// run when it ends any other way: a reload of the app list used to end a session
// without a CLIENT DISCONNECTED and without any undo, and the seat was left at
// the client's resolution with the framerate still capped, which the web
// interface then reported as the truth because it was.
//
// Running them again after a normal end costs nothing. The resize is idempotent
// and the cap is already off.
func (m *Manager) sessionEnded(ctx context.Context, name string) {
	seat, err := m.store.Get(name)
	if err != nil {
		return
	}

	quick, cancel := quick(ctx)
	defer cancel()

	if _, _, err := m.client.Try(quick, name, m.asPlayer(name,
		"/usr/local/bin/polyseat-resize", seat.Resolution)...); err != nil {
		m.logf(name, "! the resolution could not be put back: %v", err)
	}

	if _, _, err := m.client.Try(quick, name, m.asPlayer(name,
		"/usr/local/bin/polyseat-fps", "off")...); err != nil {
		m.logf(name, "! the framerate cap could not be taken off: %v", err)
	}

	rt := m.runtimeOf(name)

	m.mu.Lock()
	pending := rt.appsPending
	rt.appsPending = false

	if pending {
		rt.appsChecked = time.Now()
	}

	m.mu.Unlock()

	if pending {
		m.logf(name, "the stream ended, updating the app list now")
		m.refreshApps(ctx, name)
	}
}

// readSession reports who is streaming from a seat and what they asked for, or
// nil when nobody is.
//
// Read out of a file the seat writes rather than asked of Sunshine, which has no
// endpoint for it: see polyseat-session, which explains what was tried. Written
// by Sunshine's own prep commands, so it should exist for exactly as long as a
// stream does.
//
// "Should", which is why the connection is checked as well and the file is only
// believed when there is one. The file is written when a stream starts and
// removed when it ends, and the removal is a command Sunshine runs: it does not
// happen if Sunshine is restarted mid stream, if the seat is rebooted, or if
// anything writes that file by hand. The card then reports somebody streaming
// for the last twenty-eight minutes when the seat has been idle all along, which
// is worse than reporting nothing, because it is the answer somebody would act
// on. Seen exactly that way while testing.
//
// The stale file is cleared out on the way past, so this corrects itself instead
// of reporting the same absence every ten seconds.
func (m *Manager) readSession(ctx context.Context, name string) *Session {
	ctx, cancel := quick(ctx)
	defer cancel()

	// One command for both, so that the answer cannot come from two different
	// moments. The control channel is a TCP connection that lives as long as
	// the stream does.
	check := `if [ -z "$(ss -Htn state established ` +
		`'( sport = :47989 or sport = :48010 )')" ]; then rm -f ` + SessionPath +
		`; exit 1; fi; cat ` + SessionPath

	out, code, err := m.client.Try(ctx, name, m.asPlayer(name, "sh", "-c", check)...)
	if err != nil || code != 0 {
		return nil
	}

	var session Session
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &session) != nil {
		return nil
	}

	// An app name is the one field always written, so its absence means the file
	// is something else entirely rather than a stream nobody named.
	if session.App == "" {
		return nil
	}

	return &session
}

// readOutput reports the size the seat's screen is actually running at.
//
// Which is not what the seat was configured with, and the difference is the
// point: the output is virtual, so it becomes whatever a connecting client
// asked for and goes back afterwards. The interface was showing the configured
// value and calling it the resolution, so a seat streaming at 2560x1600 still
// claimed 1920x1080.
func (m *Manager) readOutput(ctx context.Context, name string) string {
	ctx, cancel := quick(ctx)
	defer cancel()

	rt := m.runtimeOf(name)

	m.mu.Lock()
	uid := rt.uid
	m.mu.Unlock()

	argv := m.asPlayer(name, "sh", "-c", fmt.Sprintf(
		"SWAYSOCK=$(ls -t /run/user/%d/sway-ipc.* 2>/dev/null | head -1) "+
			"swaymsg -t get_outputs 2>/dev/null", uid))

	out, code, err := m.client.Try(ctx, name, argv...)
	if err != nil || code != 0 {
		return ""
	}

	var outputs []struct {
		CurrentMode struct {
			Width   int `json:"width"`
			Height  int `json:"height"`
			Refresh int `json:"refresh"`
		} `json:"current_mode"`
	}

	if json.Unmarshal([]byte(strings.TrimSpace(out)), &outputs) != nil || len(outputs) == 0 {
		return ""
	}

	mode := outputs[0].CurrentMode
	if mode.Width == 0 || mode.Height == 0 {
		return ""
	}

	return fmt.Sprintf("%dx%d@%dHz", mode.Width, mode.Height, mode.Refresh/1000)
}

// asPlayer builds a command run as the seat's player.
//
// Running `systemctl --user` from outside a login session needs the runtime
// directory and the user bus named explicitly, because there is no login
// context to inherit them from.
func (m *Manager) asPlayer(name string, argv ...string) []string {
	uid := m.runtimeOf(name).uid
	prefix := []string{
		"sudo", "-u", Player, "env",
		fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", uid),
		fmt.Sprintf("DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/%d/bus", uid),
	}

	return append(prefix, argv...)
}

func (m *Manager) attachedDevices(name string) ([]string, error) {
	inst, _, err := m.client.Instance(name)
	if err != nil {
		return nil, err
	}

	var out []string

	for dev := range inst.Devices {
		if strings.HasPrefix(dev, devicePrefix) {
			out = append(out, strings.TrimPrefix(dev, devicePrefix))
		}
	}

	sort.Strings(out)

	return out, nil
}

// ------------------------------------------------------------------- broker

func (m *Manager) startBroker(name string) {
	rt := m.runtimeOf(name)

	m.mu.Lock()

	if rt.broker != nil {
		m.mu.Unlock()
		rt.broker.Start()

		return
	}

	proc := supervise.New([]string{
		m.cfg.Python, "-u", m.cfg.HelperDir + "/broker.py", "--seat", name,
	})
	rt.broker = proc
	m.mu.Unlock()

	proc.OnOutput = func(line string) { m.logf(name, "broker: %s", line) }
	proc.OnState = func(state supervise.State) {
		m.logf(name, "broker %s", state)
	}

	proc.Start()
}

func (m *Manager) stopBroker(name string) {
	m.mu.Lock()
	rt, ok := m.rt[name]

	var proc *supervise.Process
	if ok {
		proc = rt.broker
	}

	m.mu.Unlock()

	if proc != nil {
		proc.Stop()
	}
}

// --------------------------------------------------------------- operations

// ErrBusy is returned when a seat is already in the middle of something.
var ErrBusy = fmt.Errorf("the seat is busy")

// operate runs a long operation on a seat, exactly one at a time.
func (m *Manager) operate(name, label string, fn func(ctx context.Context) error) error {
	rt := m.runtimeOf(name)

	m.mu.Lock()

	if rt.busy != "" {
		m.mu.Unlock()

		return ErrBusy
	}

	ctx, cancel := context.WithCancel(context.Background())
	rt.busy = label
	rt.cancel = cancel
	rt.lastErr = ""
	rt.progress = -1
	m.mu.Unlock()

	m.logf(name, "== %s", label)
	m.notify()

	go func() {
		err := fn(ctx)

		m.mu.Lock()
		rt.busy = ""
		rt.cancel = nil
		rt.progress = -1

		if err != nil {
			rt.lastErr = err.Error()
		}

		m.mu.Unlock()

		if err != nil {
			m.logf(name, "! %s failed: %v", label, err)
			m.setState(name, StateError)
		} else {
			m.logf(name, "%s done", label)
		}

		cancel()

		// A bound of its own, because this one has no caller to cancel it: an
		// exec that never returns here leaks a goroutine for the life of the
		// daemon, which is exactly what was found in a stack dump.
		after, stop := context.WithTimeout(context.Background(), 2*time.Minute)
		m.reconcile(after, name)
		stop()

		m.notify()
	}()

	return nil
}

// Cancel interrupts whatever a seat is doing.
func (m *Manager) Cancel(name string) {
	m.mu.Lock()
	rt, ok := m.rt[name]

	var cancel context.CancelFunc
	if ok {
		cancel = rt.cancel
	}

	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

// Provision builds or converges a seat.
// ProvisioningAll reports whether a sweep over the stale seats is running.
func (m *Manager) ProvisioningAll() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.sweeping
}

// busyWith reports what a seat is in the middle of, or "" when it is idle.
func (m *Manager) busyWith(name string) string {
	rt := m.runtimeOf(name)

	m.mu.Lock()
	defer m.mu.Unlock()

	return rt.busy
}

// Stale lists the seats the daemon is newer than, in the order they are shown.
func (m *Manager) Stale() ([]string, error) {
	seats, err := m.store.List()
	if err != nil {
		return nil, err
	}

	return staleSeats(seats), nil
}

// staleSeats picks out the ones an older generation built.
//
// A function of the list rather than part of the method, so that what counts as
// out of date can be checked without a daemon and a container behind it. A seat
// from a newer generation counts too: that is a daemon somebody downgraded, and
// leaving it alone would mean a seat carrying files this daemon does not know
// about with nothing saying so.
func staleSeats(seats []Seat) []string {
	var out []string

	for _, seat := range seats {
		// Only a seat that was built and is now behind. One that has never been
		// built is not out of date, it is new, and starting it builds it: the
		// banner that offers to bring seats up to date would otherwise open with
		// a sentence about an older version of the daemon for a seat created a
		// minute ago.
		if seat.Provisioned != 0 && seat.Provisioned != Generation {
			out = append(out, seat.Name)
		}
	}

	return out
}

// ProvisionStale provisions every seat that an older generation built, one after
// another, and returns the names it is going to work through.
//
// One at a time on purpose. Each run installs packages inside its own container
// and the first one on a fresh machine downloads an image, so running four at
// once turns four slow operations into four slower ones and makes the log of
// each impossible to follow.
//
// Started in the background and not waited for, because it takes minutes per
// seat and the person who pressed the button is often on a phone that will lock
// its screen. Losing the page must not stop the work.
//
// A seat that fails is left with its error on its own card and the sweep carries
// on to the next. The alternative, stopping at the first failure, leaves the
// remaining seats untouched with nothing saying why.
func (m *Manager) ProvisionStale() ([]string, error) {
	stale, err := m.Stale()
	if err != nil {
		return nil, err
	}

	if len(stale) == 0 {
		return nil, nil
	}

	m.mu.Lock()

	if m.sweeping {
		m.mu.Unlock()

		return nil, ErrBusy
	}

	m.sweeping = true
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			m.sweeping = false
			m.mu.Unlock()
			m.notify()
		}()

		sweep(stale, m.busyWith, m.Provision,
			func(name, note string) { m.logf(name, "%s", note) },
			func() { time.Sleep(2 * time.Second) })
	}()

	m.notify()

	return stale, nil
}

// sweepPatience is how long the sweep waits for a seat that is in the middle of
// something else before giving up on it. Generous: the thing it is usually
// waiting for is a seat that has just been started, which takes seconds, and the
// worst case worth surviving is a stuck operation.
const sweepPatience = 5 * time.Minute

// sweep provisions each seat in turn, waiting for one to finish before starting
// the next.
//
// Written as a function of its dependencies so that the waiting can be tested,
// because the waiting is the whole of it and the first version did not have any.
// It called Provision straight away, and a seat that was busy for a moment
// answered "busy", which was logged as a note on that seat and skipped. Both
// seats had been started five seconds earlier, so a request that reported it was
// provisioning two seats provisioned neither and said nothing anybody would
// read as failure.
func sweep(names []string, busy func(string) string, provision func(string) error,
	note func(name, text string), wait func()) {
	for _, name := range names {
		// Wait for whatever it is already doing. A seat coming up, a library
		// pass, somebody's own click a second earlier.
		waited := time.Duration(0)

		for busy(name) != "" {
			if waited >= sweepPatience {
				note(name, fmt.Sprintf("! still busy with %q after %s, skipped in this pass",
					busy(name), sweepPatience))

				break
			}

			wait()

			waited += 2 * time.Second
		}

		if busy(name) != "" {
			continue
		}

		if err := provision(name); err != nil {
			note(name, fmt.Sprintf("! it could not be provisioned in this pass: %v", err))

			continue
		}

		// Waited for rather than fired off, which is the point of the sweep:
		// four provisioning runs at once turn four slow operations into four
		// slower ones. Polled because operate owns the goroutine and reports
		// through the same busy flag the interface reads.
		for busy(name) != "" {
			wait()
		}
	}
}

func (m *Manager) Provision(name string) error {
	return m.operate(name, "provisioning", func(ctx context.Context) error {
		return m.build(ctx, name)
	})
}

// build is provisioning without the operation around it, so that starting a seat
// that has never been built can do it as part of starting.
//
// Separate for exactly that: a brand new seat used to answer Start with "this
// seat has no container yet, provision it first", and its card offered to
// provision a seat that had never been provisioned as though it were out of
// date. Two words for one thing, and the second of them wrong.
func (m *Manager) build(ctx context.Context, name string) error {
	seat, err := m.store.Get(name)
	if err != nil {
		return err
	}

	uplink := m.cfg.Uplink
	if uplink == "" {
		uplink, err = config.DefaultUplink()
		if err != nil {
			return fmt.Errorf("no uplink interface configured and none could be guessed: %w", err)
		}
	}

	secrets, err := m.ensureSecrets(name)
	if err != nil {
		return err
	}

	m.setState(name, StateBuilding)

	// The broker has nothing to do while a seat is being rebuilt, and
	// provisioning restarts the container underneath it.
	m.stopBroker(name)

	p := &Provisioner{
		Client:  m.client,
		Seat:    seat,
		Uplink:  uplink,
		Image:   m.cfg.Image,
		Secrets: secrets,
		Library: m.pool,
		Log:     func(f string, a ...any) { m.logf(name, f, a...) },
	}

	if err := p.Run(ctx); err != nil {
		return err
	}

	seat.Provisioned = Generation
	seat.PlayerUID = p.uid

	if err := m.store.Put(seat); err != nil {
		return err
	}

	return m.startSession(ctx, name)
}

// Start brings a seat up: container, session, broker.
func (m *Manager) Start(name string) error {
	if _, err := m.store.Get(name); err != nil {
		return err
	}

	return m.operate(name, "starting", func(ctx context.Context) error {
		m.setState(name, StateStarting)

		// A seat nobody has built yet is built here, rather than being told to
		// go and press the other button. Somebody who has just created a seat
		// wants it running; that it has to be built first is this program's
		// business and not theirs.
		seat, err := m.store.Get(name)
		if err != nil {
			return err
		}

		if seat.Provisioned == 0 {
			m.logf(name, "this seat has never been built, doing that first")

			return m.build(ctx, name)
		}

		status, err := m.client.Status(name)
		if err != nil {
			return err
		}

		if status == "" {
			return fmt.Errorf("this seat has no container, build it again")
		}

		if status != "Running" {
			if err := m.client.Start(ctx, name); err != nil {
				return err
			}
		} else if m.unitState(ctx, name, "polyseat-sway.service") == "active" &&
			m.unitState(ctx, name, "polyseat-sunshine.service") == "active" {
			// Already up. Saying so and only making sure the broker is there
			// is not a shortcut: starting the session again would restart
			// Sunshine, and restarting Sunshine under somebody who is in the
			// middle of a game is the rudest thing this daemon could do. It is
			// also what would happen on every daemon restart otherwise.
			m.logf(name, "already running, adopting it")
			m.startBroker(name)
			m.setState(name, StateRunning)

			return nil
		}

		return m.startSession(ctx, name)
	})
}

// startSession brings up the session inside a running container and then the
// broker.
//
// The order is the whole point of doing this from the daemon rather than
// letting the units start themselves at container boot. Sunshine's allowed web
// origins are derived from the seat's addresses, so the configuration can only
// be written once the container has them; and the broker may only run while the
// container is up.
func (m *Manager) startSession(ctx context.Context, name string) error {
	seat, err := m.store.Get(name)
	if err != nil {
		return err
	}

	p := &Provisioner{
		Client: m.client,
		Seat:   seat,
		Image:  m.cfg.Image,
		Log:    func(f string, a ...any) { m.logf(name, f, a...) },
	}

	if err := p.waitSystemd(ctx); err != nil {
		return err
	}

	if err := p.readUID(ctx); err != nil {
		return err
	}

	m.mu.Lock()
	m.rt[name].uid = p.uid
	m.mu.Unlock()

	if err := p.waitAddresses(ctx); err != nil {
		m.logf(name, "! %v, the web interface may refuse saves", err)
	}

	// Written on every start as well as while provisioning, so that a seat that
	// was switched off when somebody moved the slider comes up with the value
	// they chose rather than with the one it was built with.
	if err := p.WritePointerConfig(ctx); err != nil {
		m.logf(name, "! the pointer speed could not be applied: %v", err)
	}

	origins, err := p.WriteSunshineConfig(ctx)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.rt[name].configOrigins = origins
	m.rt[name].runningOrigins = origins
	m.rt[name].notes = nil
	m.mu.Unlock()

	// Rebuilt on every start, because a launcher the player installed since the
	// last one is only worth having if Moonlight offers it. A seat that cannot
	// be told what to show is not worth failing to start, so this reports and
	// carries on.
	if apps, _, err := p.WriteApps(ctx); err != nil {
		m.logf(name, "! the app list could not be written: %v", err)
	} else {
		m.logf(name, "Moonlight will offer: %s", strings.Join(apps, ", "))
	}

	m.logf(name, "starting the audio stack")

	if _, err := m.client.Run(ctx, name, m.asPlayer(name,
		"systemctl", "--user", "daemon-reload")...); err != nil {
		return err
	}

	if _, code, err := m.client.Try(ctx, name, m.asPlayer(name,
		"systemctl", "--user", "enable", "--now",
		"pipewire.socket", "pipewire-pulse.socket", "wireplumber.service")...); err != nil {
		return err
	} else if code != 0 {
		m.logf(name, "! PipeWire reported an error, there may be no sound")
	}

	m.logf(name, "starting the session")

	if _, err := m.client.Run(ctx, name, m.asPlayer(name,
		"systemctl", "--user", "restart", "polyseat-sway.service")...); err != nil {
		return err
	}

	if err := m.waitUnit(ctx, name, "polyseat-sunshine.service", 60*time.Second); err != nil {
		return err
	}

	if encoder, codecs := m.readEncoders(ctx, name); encoder != "" {
		m.logf(name, "Sunshine encoder: %s (%s)", encoder, strings.Join(codecs, ", "))

		// libx264 and libx265 are ffmpeg's own, which means the card is not
		// being used at all.
		if strings.HasPrefix(encoder, "lib") {
			m.logf(name, "! that is the software encoder, the GPU path is broken")
		}
	}

	m.startBroker(name)
	m.setState(name, StateRunning)

	return nil
}

func (m *Manager) waitUnit(ctx context.Context, name, unit string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if m.unitState(ctx, name, unit) == "active" {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}

	return fmt.Errorf("%s did not come up", unit)
}

// Stop takes a seat down.
func (m *Manager) Stop(name string) error {
	if _, err := m.store.Get(name); err != nil {
		return err
	}

	return m.operate(name, "stopping", func(ctx context.Context) error {
		m.setState(name, StateStopping)

		// Before the container, not after. A broker still polling into a
		// shutdown is what once left the Incus daemon hung in "Stopping
		// instance" with the container already dead.
		m.logf(name, "stopping the input broker")
		m.stopBroker(name)

		status, err := m.client.Status(name)
		if err != nil {
			return err
		}

		if status == "" || status == "Stopped" {
			m.setState(name, StateStopped)

			return nil
		}

		if err := m.haltContainer(ctx, name); err != nil {
			return err
		}

		m.setState(name, StateStopped)

		return nil
	})
}

// haltContainer brings a running container down, the session first.
//
// The order and the timeout are both from experience. A seat whose Sway and
// Sunshine are still running takes far longer to shut down, because the
// container's systemd waits out its own stop jobs for the lingering user
// manager; asking the session to go first cuts that. And thirty seconds was not
// enough: the stop reported a failure while the container was in fact still on
// its way down, and it stopped by itself a minute later, which is a confusing
// thing to be told.
func (m *Manager) haltContainer(ctx context.Context, name string) error {
	m.logf(name, "stopping the session")

	if _, _, err := m.client.Try(ctx, name, m.asPlayer(name,
		"systemctl", "--user", "stop", "polyseat-sway.service")...); err != nil {
		m.logf(name, "! could not stop the session cleanly: %v", err)
	}

	m.logf(name, "stopping the container")

	err := m.client.Stop(ctx, name, 90)
	if err == nil {
		return nil
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Nothing inside is worth waiting for any longer. The session has already
	// been asked to stop, and a seat holds no state that a clean unmount
	// protects.
	m.logf(name, "! it did not shut down in time (%v), forcing it", err)

	return m.client.Kill(ctx, name)
}

// Create records a new seat. If a container of that name already exists it is
// adopted rather than rejected: provisioning converges it either way, and that
// is how the seats built by hand during the spikes come under the daemon.
func (m *Manager) Create(seat Seat) error {
	if err := seat.Validate(); err != nil {
		return err
	}

	if _, err := m.store.Get(seat.Name); err == nil {
		return fmt.Errorf("a seat called %q already exists", seat.Name)
	}

	seat.Created = time.Now()
	seat.Provisioned = 0

	if err := m.store.Put(seat); err != nil {
		return err
	}

	m.runtimeOf(seat.Name)
	m.reconcile(context.Background(), seat.Name)
	m.notify()

	return nil
}

// Update changes a seat definition. The name cannot change, because it is also
// the container name and the tag Sunshine writes into its device names.
// applyPointerSpeed pushes the pointer speed into a seat that is running.
//
// Silent about a seat that is switched off: there is nothing to write into and
// nothing lost, because startSession writes it again on the way up.
func (m *Manager) applyPointerSpeed(ctx context.Context, seat Seat) {
	if status, err := m.client.Status(seat.Name); err != nil || status != "Running" {
		return
	}

	p := &Provisioner{
		Client: m.client,
		Seat:   seat,
		Image:  m.cfg.Image,
		Log:    func(f string, a ...any) { m.logf(seat.Name, f, a...) },
		uid:    m.runtimeOf(seat.Name).uid,
	}

	if err := p.WritePointerConfig(ctx); err != nil {
		m.logf(seat.Name, "! the pointer speed could not be applied: %v", err)

		return
	}

	m.logf(seat.Name, "pointer speed set to %.2f screens per second", seat.PointerSpeed)
}

func (m *Manager) Update(name string, change func(*Seat)) error {
	seat, err := m.store.Get(name)
	if err != nil {
		return err
	}

	before := seat
	change(&seat)
	seat.Name = name

	if err := seat.Validate(); err != nil {
		return err
	}

	// Anything that only provisioning can apply marks the seat as needing it.
	if seat.Address != before.Address || seat.Gateway != before.Gateway ||
		seat.Resolution != before.Resolution {
		seat.Provisioned = 0
	}

	if err := m.store.Put(seat); err != nil {
		return err
	}

	// Taking part in the shared library is applied right away rather than left
	// for the next provisioning run. It is one disk device and Incus hotplugs
	// those, and the first version of this did leave it for provisioning: the
	// checkbox then changed a stored value and nothing else, the library went
	// on reporting the seat as absent, and there was nothing on the page saying
	// why. A setting that needs a second, unnamed step to take effect is a
	// setting that looks broken.
	if seat.Library != before.Library {
		if err := m.applyLibrary(context.Background(), seat); err != nil {
			m.logf(name, "! the shared library could not be %s: %v",
				map[bool]string{true: "attached", false: "detached"}[seat.Library], err)
		}
	}

	// The same reasoning as the library above, and more so: this is a setting
	// somebody adjusts while holding the controller, so waiting for the next
	// provisioning run would make it look like it does nothing at all.
	if seat.PointerSpeed != before.PointerSpeed {
		m.applyPointerSpeed(context.Background(), seat)
	}

	m.notify()

	return nil
}

// Delete removes a seat and, unless asked to keep it, its container.
func (m *Manager) Delete(name string, keepContainer bool) error {
	if _, err := m.store.Get(name); err != nil {
		return err
	}

	return m.operate(name, "deleting", func(ctx context.Context) error {
		m.stopBroker(name)

		status, err := m.client.Status(name)
		if err != nil {
			return err
		}

		if status != "" && !keepContainer {
			if status == "Running" {
				if err := m.haltContainer(ctx, name); err != nil {
					return err
				}
			}

			m.logf(name, "deleting the container")

			if err := m.client.Delete(ctx, name); err != nil {
				return err
			}
		}

		if err := m.store.Delete(name); err != nil {
			return err
		}

		if err := m.store.DeleteSecrets(name); err != nil {
			return err
		}

		// The seat's own library is left on disk. Deleting a seat should not
		// silently take somebody's installed games with it, and because the
		// blocks are shared it frees almost nothing anyway.
		if m.pool != nil {
			if err := m.pool.Forget(name); err != nil {
				m.logf(name, "! the library bookkeeping for this seat could not be cleared: %v", err)
			}
		}

		m.mu.Lock()
		delete(m.rt, name)
		m.mu.Unlock()

		return nil
	})
}

// ------------------------------------------------------------------ pairing

// SunshineAccess is what somebody needs to open a seat's own Sunshine page.
type SunshineAccess struct {
	Username string `json:"username"`
	Password string `json:"password"`
	URL      string `json:"url"`
}

// ensureSecrets returns the seat's credentials, generating them the first time.
//
// Generated once and then kept, deliberately. A seat whose container is rebuilt
// has to come back with the same Sunshine password, because the paired devices
// are stored against it and would otherwise all have to be paired again.
func (m *Manager) ensureSecrets(name string) (Secrets, error) {
	secrets, err := m.store.Secrets(name)
	if err != nil {
		return secrets, err
	}

	if secrets.SunshineUser != "" && secrets.SunshinePassword != "" {
		return secrets, nil
	}

	password, err := RandomPassword()
	if err != nil {
		return secrets, err
	}

	secrets = Secrets{SunshineUser: "polyseat", SunshinePassword: password}

	return secrets, m.store.PutSecrets(name, secrets)
}

// SunshineCredentials returns a seat's login and the address to use it at.
func (m *Manager) SunshineCredentials(name string) (SunshineAccess, error) {
	secrets, err := m.store.Secrets(name)
	if err != nil {
		return SunshineAccess{}, err
	}

	if secrets.SunshineUser == "" {
		return SunshineAccess{}, fmt.Errorf("this seat has no Sunshine login yet, provision it")
	}

	access := SunshineAccess{
		Username: secrets.SunshineUser,
		Password: secrets.SunshinePassword,
	}

	// The LAN address, because this is the one somebody types into a browser.
	// The daemon itself uses the other one, see sunshineClient.
	if addr := m.addressOn(name, "eth1"); addr != "" {
		access.URL = fmt.Sprintf("https://%s:%d", addr, sunshine.Port)
	}

	return access, nil
}

func (m *Manager) addressOn(name, iface string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	rt, ok := m.rt[name]
	if !ok {
		return ""
	}

	if addrs := rt.addresses[iface]; len(addrs) > 0 {
		return addrs[0]
	}

	return ""
}

// sunshineClient builds a client for a seat's Sunshine.
func (m *Manager) sunshineClient(name string) (*sunshine.Client, error) {
	if _, err := m.store.Get(name); err != nil {
		return nil, err
	}

	secrets, err := m.store.Secrets(name)
	if err != nil {
		return nil, err
	}

	if secrets.SunshineUser == "" {
		return nil, fmt.Errorf("this seat has no Sunshine login yet, provision it")
	}

	// eth0, the Incus bridge, never eth1. The seats reach the LAN through
	// macvlan, and a macvlan interface cannot talk to its own host, so the
	// address Moonlight uses is precisely the one that does not work here.
	address := m.addressOn(name, "eth0")
	if address == "" {
		return nil, fmt.Errorf("this seat is not running")
	}

	return sunshine.New(address, secrets.SunshineUser, secrets.SunshinePassword), nil
}

// PairedDevices lists the clients paired with a seat.
func (m *Manager) PairedDevices(ctx context.Context, name string) ([]sunshine.Device, error) {
	client, err := m.sunshineClient(name)
	if err != nil {
		return nil, err
	}

	return client.Devices(ctx)
}

// Pair hands a seat the PIN Moonlight is showing.
func (m *Manager) Pair(ctx context.Context, name, pin, label string) error {
	client, err := m.sunshineClient(name)
	if err != nil {
		return err
	}

	if err := client.Pair(ctx, pin, label); err != nil {
		m.logf(name, "! pairing %q failed: %v", label, err)

		return err
	}

	m.logf(name, "paired %q", label)

	return nil
}

// Unpair removes a paired client from a seat.
func (m *Manager) Unpair(ctx context.Context, name, uuid string) error {
	client, err := m.sunshineClient(name)
	if err != nil {
		return err
	}

	if err := client.Unpair(ctx, uuid); err != nil {
		return err
	}

	m.logf(name, "unpaired a device")

	return nil
}

// --------------------------------------------------------------------- view

// Status returns everything the interface needs about one seat.
func (m *Manager) Status(name string) (Status, error) {
	seat, err := m.store.Get(name)
	if err != nil {
		return Status{}, err
	}

	rt := m.runtimeOf(name)

	m.mu.Lock()
	defer m.mu.Unlock()

	broker := string(supervise.Stopped)
	if rt.broker != nil {
		broker = string(rt.broker.State())
	}

	return Status{
		Seat:      seat,
		State:     rt.state,
		Container: rt.container,
		Addresses: rt.addresses,
		Sway:      rt.sway,
		Sunshine:  rt.sunshine,
		Encoder:   rt.encoder,
		Codecs:    rt.codecs,
		Output:    rt.output,
		Session:   rt.session,
		Broker:    broker,
		Devices:   rt.devices,
		Busy:      rt.busy,
		Progress:  rt.progress,
		Notes:     rt.notes,
		Error:     rt.lastErr,
		Built:     seat.Provisioned != 0,
		Stale:     seat.Provisioned != 0 && seat.Provisioned != Generation,
	}, nil
}

// List returns the status of every seat.
func (m *Manager) List() ([]Status, error) {
	seats, err := m.store.List()
	if err != nil {
		return nil, err
	}

	out := make([]Status, 0, len(seats))

	for _, s := range seats {
		status, err := m.Status(s.Name)
		if err != nil {
			continue
		}

		out = append(out, status)
	}

	return out, nil
}

// Config returns the bootstrap configuration in use.
func (m *Manager) Config() config.Config { return m.cfg }
