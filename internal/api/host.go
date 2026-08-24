// The things the interface does to the machine rather than to a seat: getting
// it ready, putting the uplink on a bridge, and taking Polyseat off it again.
//
// The first and the last exist for one reason. Everything else Polyseat does is already done from
// this page, and the two ends of its life were the exception: a terminal to
// prepare the machine, a terminal to remove it. Neither can be moved into the
// package itself, because an Arch package may place files and may not
// initialise Incus, write to /etc/subuid or put an account in a group, and
// pacman knows nothing about the order seats have to be taken apart in.
//
// The bridge is here for a different one. It could be run from a terminal and
// was, but the script stops every seat to do its work and then refuses to start
// them again, because starting a seat properly is this daemon's job and not a
// shell script's. Doing it from here is the only way the two halves meet.
//
// None of the three does the work itself. All three run the same script
// somebody at a terminal would run, which is what keeps one procedure rather
// than two: host/prepare.sh as polyseat-prepare, host/uninstall.sh as
// polyseat-uninstall, host/lan-bridge.sh as polyseat-lan-bridge.

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/superuser404notfound/Polyseat/internal/auth"
	"github.com/superuser404notfound/Polyseat/internal/config"
	"github.com/superuser404notfound/Polyseat/internal/prepare"
	"github.com/superuser404notfound/Polyseat/internal/seat"
	"github.com/superuser404notfound/Polyseat/internal/uninstall"
	"github.com/superuser404notfound/Polyseat/internal/update"
	"github.com/superuser404notfound/Polyseat/internal/version"
	"github.com/superuser404notfound/Polyseat/internal/web"
)

// maxHostBody is the largest request either handler reads. Both bodies are a
// password, an account name and two flags.
const maxHostBody = 64 << 10

// hostRequest is what the two host actions are asked with.
type hostRequest struct {
	// Password is the interface password again, when the moment calls for it.
	// Removing always calls for it; preparing does when update_needs_password
	// is on, which is the same rule the update follows.
	Password string `json:"password"`

	// Account goes in the input group. Empty means nobody, which is a real
	// answer on a headless machine.
	Account string `json:"account"`

	Seats   bool `json:"seats"`
	Library bool `json:"library"`

	// Undo asks for the uplink to be put back on a plain interface, rather than
	// made a bridge. One field and not two endpoints: it is the same script,
	// the same lock and the same sentence about the seats either way, and the
	// direction is the only thing that differs.
	Undo bool `json:"undo"`

	// Confirm is the word typed out before seats are deleted. Checked here as
	// well as in the page, because a check that only exists in the browser
	// protects nobody: it is the API that deletes things.
	Confirm string `json:"confirm"`
}

// readHostRequest decodes the body and hands it back for the password check.
//
// confirmPassword takes a request rather than a decoded body on purpose. It is
// the guard between a session and running pacman as root, and it is tested on
// its own with nothing else in the way, so widening it to take somebody else's
// struct would be paying for this convenience with the one function here that
// most deserves to stay simple. The two handlers that need more out of the body
// than a password read it once and put a reader over the same bytes back.
func readHostRequest(r *http.Request) (hostRequest, error) {
	var req hostRequest

	body, err := io.ReadAll(io.LimitReader(r.Body, maxHostBody))
	if err != nil {
		return req, err
	}

	r.Body = io.NopCloser(bytes.NewReader(body))

	// No body at all is not an error. The page sends none when it has nothing
	// to say, which is the ordinary case for preparing a machine whose
	// configuration does not ask for the password.
	if len(bytes.TrimSpace(body)) == 0 {
		return req, nil
	}

	if err := json.Unmarshal(body, &req); err != nil {
		return req, err
	}

	return req, nil
}

// ------------------------------------------------------------------ prepare

// prepareHost runs polyseat-prepare and streams what it says into the state.
//
// It takes one thing from the caller and it is not a command: the account that
// goes in the input group. Everything else about the run is fixed, so a stolen
// session can ask this machine to be made ready and cannot ask it to run
// anything. That is the same property the update handler has and it is the one
// to keep if this ever grows a second argument.
func (s *Server) prepareHost(w http.ResponseWriter, r *http.Request) {
	if !s.config().WebUpdate {
		fail(w, http.StatusForbidden, errors.New(`preparing the machine from the interface is off. Set "web_update": true in /etc/polyseat/polyseatd.json, or run: sudo polyseat-prepare`))

		return
	}

	req, err := readHostRequest(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	// Asked before anything is looked at, so that a wrong password learns
	// nothing about the machine it was aimed at.
	if err := s.confirmed(r); err != nil {
		s.log.Warn("preparing the machine was asked for with the wrong password",
			"source", auth.Source(r))
		fail(w, http.StatusUnauthorized, err)

		return
	}

	if s.updating() {
		fail(w, http.StatusConflict, errors.New("a newer Polyseat is being installed, which is already running pacman. Wait for that to finish"))

		return
	}

	// Conflict rather than a server error, whichever of the two it is: a run
	// that is already going and a script that is not installed are both things
	// about this machine's state rather than a request that was malformed, and
	// the page prints the sentence either way.
	if err := s.prepare.Start(req.Account, s.notify); err != nil {
		fail(w, http.StatusConflict, err)

		return
	}

	// After it started, not before, or a refusal leaves a line in the journal
	// saying this machine was prepared when nothing ran.
	s.log.Info("preparing this machine from the interface",
		"account", req.Account, "source", auth.Source(r))

	writeJSON(w, http.StatusAccepted, map[string]any{"running": true})
}

// ---------------------------------------------------------------- uninstall

// startRemoval is uninstall.Start behind a name a test can replace.
//
// The seam is here rather than in the uninstall package because what is worth
// checking is this handler: whether a request that asked for the daemon to go
// can end up deleting the seats. Nothing else in this file decides that, and
// the real one hands the job to systemd, which a test cannot take back.
var startRemoval = uninstall.Start

// removeState is built here rather than inline, because both servers send it.
func (s *Server) removeState() removeState {
	out := removeState{
		Enabled: s.config().WebUninstall,
		Running: s.isRemoving(),
	}

	if _, err := uninstall.Command(); err != nil {
		out.Reason = err.Error()
	} else {
		out.Available = true
	}

	return out
}

func (s *Server) isRemoving() bool {
	s.removingMu.Lock()
	defer s.removingMu.Unlock()

	return s.removing
}

// removeHost hands the removal to systemd and says so.
//
// The password is asked for every time, whatever update_needs_password says.
// That setting exists so that somebody can decide whether an update is worth a
// second question; this is not that kind of question. It is the one action in
// the interface that cannot be undone by pressing the button again, and the
// page it is pressed from may be an unlocked phone on a table.
//
// The reply is written before anything happens, and then this process has about
// two seconds left: the script's first act is to stop the daemon serving it.
func (s *Server) removeHost(w http.ResponseWriter, r *http.Request) {
	if !s.config().WebUninstall {
		fail(w, http.StatusForbidden, errors.New(`removing Polyseat from the interface is off. Set "web_uninstall": true in /etc/polyseat/polyseatd.json, or run: sudo polyseat-uninstall`))

		return
	}

	req, err := readHostRequest(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	if err := confirmPassword(s.auth, true, r); err != nil {
		s.log.Warn("removing Polyseat was asked for with the wrong password",
			"source", auth.Source(r))
		fail(w, http.StatusUnauthorized, err)

		return
	}

	// The typed word, checked here and not only in the browser. Only for the
	// half that deletes containers: removing the daemon and leaving the seats
	// is undone by installing it again, and asking somebody to type a word to
	// undo something reversible teaches them to type it without reading.
	if req.Seats && req.Confirm != "remove" {
		fail(w, http.StatusBadRequest,
			errors.New(`deleting the seats needs the word "remove" typed out`))

		return
	}

	s.removingMu.Lock()

	if s.removing {
		s.removingMu.Unlock()
		fail(w, http.StatusConflict, errors.New("this machine is already being removed"))

		return
	}

	s.removing = true
	s.removingMu.Unlock()

	if err := startRemoval(uninstall.Options{Seats: req.Seats, Library: req.Library}); err != nil {
		// Put back, because nothing was scheduled and the page should be able
		// to try again rather than being told forever that it is already going.
		s.removingMu.Lock()
		s.removing = false
		s.removingMu.Unlock()

		fail(w, http.StatusInternalServerError, err)

		return
	}

	// Warn rather than Info. This is the last thing most of these journals will
	// have to say about Polyseat, and it should be findable.
	s.log.Warn("Polyseat is being removed from this machine",
		"seats", req.Seats, "library", req.Library, "source", auth.Source(r))

	s.notify()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"removing": true,
		"seats":    req.Seats,
		"library":  req.Library,

		// Where the rest of it is written down. The interface stops answering
		// a moment from now, so this is the only place the end of the run can
		// be read.
		"journal": "journalctl -u polyseat-uninstall",
	})
}

// --------------------------------------------------------------- setup mode

// setupResponse is the state of a machine that is not ready yet.
//
// A shape of its own rather than stateResponse with the seat half left empty.
// Half of that struct is about seats, a seatless machine has nothing true to
// say about them, and sending zero values would be answering questions nobody
// can ask yet.
type setupResponse struct {
	// Ready is false here and true in the ordinary state. One field, sent by
	// both, so that the page branches on an answer rather than on which keys
	// happen to be missing.
	Ready bool `json:"ready"`

	// Reason is what went wrong reaching Incus, in the words the client library
	// used. It is shown, because "this machine is not ready" without a reason
	// sends somebody to the journal for the one line that was already known.
	Reason string `json:"reason"`

	Host    hostInfo      `json:"host"`
	Config  config.Config `json:"config"`
	Prepare prepare.State `json:"prepare"`
	Remove  removeState   `json:"remove"`
	Now     time.Time     `json:"now"`
}

// NewSetup builds the interface a daemon serves when it cannot reach Incus.
//
// The daemon used to exit in that case, and on a fresh machine that is every
// case: Incus is one of the packages polyseat-prepare installs, so a machine
// with the package and nothing else has no socket to connect to. The daemon
// died at startup, systemd restarted it five seconds later, and the interface
// that was supposed to explain all this was the one thing that never came up.
//
// So it comes up anyway, with everything that needs a seat left unregistered
// rather than guarded, and offers the one button that fixes it. What it is not
// is a second interface: it serves the same files, the same session, the same
// password, and hands over to the real one by restarting into it.
func NewSetup(cfg config.Config, credentials *auth.Store, updates *update.Checker, preparer *prepare.Runner, reason error, logger *slog.Logger) http.Handler {
	if preparer == nil {
		preparer = &prepare.Runner{}
	}

	s := &Server{
		auth: credentials, updates: updates, prepare: preparer, log: logger,
		setupConfig: cfg,
	}

	if reason != nil {
		s.setupReason = reason.Error()
	}

	mux := http.NewServeMux()

	// The same four as the ordinary interface, and they have to be: this is
	// where a fresh machine is claimed, and the password chosen here is the one
	// that is there after the restart into the real thing.
	mux.HandleFunc("POST /api/setup", s.setup)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/session", s.session)

	guarded := http.NewServeMux()
	guarded.HandleFunc("GET /api/state", s.setupState)

	// Here as well, because the Account dialog is on this page too and a
	// password chosen a minute ago on a machine anybody could reach is exactly
	// the one somebody might want to change straight away.
	guarded.HandleFunc("POST /api/password", s.changePassword)
	guarded.HandleFunc("POST /api/prepare", s.prepareHost)
	guarded.HandleFunc("POST /api/restart", s.restart)

	// Offered here too, because a machine that will not come up is one somebody
	// may reasonably want to take Polyseat off, and doing that from a page that
	// is already open beats finding out which command to type.
	guarded.HandleFunc("POST /api/uninstall", s.removeHost)

	mux.Handle("/api/", s.requireSession(guarded))
	mux.Handle("/", web.Handler())

	return logging(logger, mux)
}

func (s *Server) setupState(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()

	writeJSON(w, http.StatusOK, setupResponse{
		Ready:  false,
		Reason: s.setupReason,
		Host: hostInfo{
			Hostname: hostname,
			Version:  version.Version,
		},
		Config:  s.setupConfig,
		Prepare: s.prepare.State(),
		Remove:  s.removeState(),
		Now:     time.Now(),
	})
}

// Ready says whether this handler is the whole interface. Used by the session
// endpoint, which both modes serve, so that a page knows which one it is
// talking to before it has a session at all.
func (s *Server) ready() bool {
	return s.manager != nil
}

// --------------------------------------------------------------- lan bridge

// bridgeUplink runs polyseat-lan-bridge and streams what it says into the state.
//
// The one thing in this interface that changes the network underneath the
// connection it was asked over. That is not a reason to refuse it: every step
// in the script that can fail puts everything back, and the run is a goroutine
// rather than the request, so it finishes whether or not the browser that
// started it is still there to watch. It is the reason the page says plainly
// what is about to happen, and the reason this handler reads the seats first.
func (s *Server) bridgeUplink(w http.ResponseWriter, r *http.Request) {
	if !s.config().WebLanBridge {
		fail(w, http.StatusForbidden, errors.New(`changing the uplink from the interface is off. Set "web_lan_bridge": true in /etc/polyseat/polyseatd.json, or run: sudo polyseat-lan-bridge`))

		return
	}

	req, err := readHostRequest(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	// Every time, whatever update_needs_password says. Not because this cannot
	// be undone — it is the one host action that can, by pressing the other
	// button — but because of where the page can be: a seat's own browser
	// reaches this interface over the management bridge, and this is the button
	// that would hand that seat the LAN it was kept off.
	if err := confirmPassword(s.auth, true, r); err != nil {
		s.log.Warn("changing the uplink was asked for with the wrong password",
			"source", auth.Source(r))
		fail(w, http.StatusUnauthorized, err)

		return
	}

	// Read before anything starts, because once the script has stopped them
	// there is nothing left to tell a seat somebody was playing on from one
	// that was already down.
	running := s.runningSeats()

	// Conflict rather than a server error, whichever of the two it is: a run
	// already going and a script that is not installed are both facts about
	// this machine rather than a malformed request, and the page prints the
	// sentence either way.
	if err := s.lanbridge.Start(req.Undo, s.notify, s.resumeSeats(running)); err != nil {
		fail(w, http.StatusConflict, err)

		return
	}

	s.log.Info("changing the uplink from the interface",
		"undo", req.Undo, "seats", running, "source", auth.Source(r))

	writeJSON(w, http.StatusAccepted, map[string]any{
		"running": true,
		"undo":    req.Undo,
		"seats":   running,
	})
}

// runningSeats names the seats that are up, or on their way up.
//
// Starting counts. A seat halfway through coming up is stopped by the script
// exactly like one that is up, and leaving it out here would mean the one seat
// that was in the middle of something is the one that stays down afterwards.
func (s *Server) runningSeats() []string {
	if s.manager == nil {
		return nil
	}

	seats, err := s.manager.List()
	if err != nil {
		// Not fatal, and not silent either: the run is still the right thing to
		// do, it just cannot promise to put anything back.
		s.log.Warn("the seats could not be listed before changing the uplink", "error", err)

		return nil
	}

	out := []string{}

	for _, status := range seats {
		if status.State == seat.StateRunning || status.State == seat.StateStarting {
			out = append(out, status.Name)
		}
	}

	return out
}

// resumeSeats builds the half of the run the script deliberately leaves out.
//
// lan-bridge.sh stops every seat, because the kernel refuses to make an
// interface a bridge port while a macvlan hangs off it, and then prints the
// names instead of starting them again. That is not laziness in the script and
// undoing it there would be wrong: `incus start` brings a container up and
// leaves the compositor, Sunshine, the audio stack, the Moonlight app list and
// the wait for an encoder to whoever knows about them. This does.
//
// It runs after a failed run as well as a successful one, and that is the
// point rather than an oversight: a run that failed put the network back and
// left the seats stopped in exactly the same way, and seats left down after a
// failure is the worse of the two outcomes, not the safer one.
func (s *Server) resumeSeats(names []string) func(func(string)) {
	return func(log func(string)) {
		if s.manager == nil || len(names) == 0 {
			return
		}

		log("")
		log("Starting the seats that were up before this:")

		for _, name := range names {
			if err := s.manager.Start(name); err != nil {
				log(fmt.Sprintf("  ! %s did not start: %s", name, err))

				continue
			}

			log("  " + name)
		}

		log("")
		log("Each of them reports the rest of the way up on its own card.")
	}
}
