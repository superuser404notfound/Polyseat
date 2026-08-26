// Package api serves the daemon's HTTP interface and the web pages that use it.
//
// There is no command line. Everything Polyseat can do is done here, which is
// the whole reason the daemon owns the configuration in the first place: if
// there were a second way in, the generated files would have two authors.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/superuser404notfound/Polyseat/internal/auth"
	"github.com/superuser404notfound/Polyseat/internal/config"
	"github.com/superuser404notfound/Polyseat/internal/lanbridge"
	"github.com/superuser404notfound/Polyseat/internal/library"
	"github.com/superuser404notfound/Polyseat/internal/prepare"
	"github.com/superuser404notfound/Polyseat/internal/seat"
	"github.com/superuser404notfound/Polyseat/internal/sunshine"
	"github.com/superuser404notfound/Polyseat/internal/update"
	"github.com/superuser404notfound/Polyseat/internal/version"
	"github.com/superuser404notfound/Polyseat/internal/web"
)

// Server exposes the manager over HTTP.
type Server struct {
	// manager is nil in setup mode, which is the mode this daemon comes up in
	// when it cannot reach Incus. Everything that touches seats is left
	// unregistered there, and the handful of handlers that serve both modes ask
	// through config, streaming and notify rather than reaching for it. See
	// NewSetup.
	manager *seat.Manager

	auth    *auth.Store
	updates *update.Checker
	log     *slog.Logger

	// setupConfig is what config() answers with when there is no manager to ask.
	setupConfig config.Config

	// setupReason is why Incus could not be reached, in the words the client
	// library used. Shown on the page, because "not ready" without a reason
	// sends people to the journal for the one line that was already known.
	setupReason string

	// prepare runs host/prepare.sh for the interface. Shared between the two
	// modes rather than made per server, so that the one-run-at-a-time rule
	// holds across a restart into the other one.
	prepare *prepare.Runner

	// lanbridge runs host/lan-bridge.sh for the interface. Not shared with
	// setup mode the way prepare is: that mode exists because there is no Incus
	// to talk to, so it has no seats, and the uplink only means anything to a
	// machine that has some.
	lanbridge *lanbridge.Runner

	// removing is set once the removal has been handed to systemd, for the
	// couple of seconds this process has left. Without it the page has nothing
	// to show between pressing the button and the connection going away, which
	// looks exactly like a button that did nothing.
	removingMu sync.Mutex
	removing   bool

	// updater is the state of an update started from the interface.
	//
	// Kept here rather than in the manager because the manager owns seats and
	// this is the daemon updating itself. One at a time, which busy enforces: a
	// second pacman while the first is running would fail on the database lock
	// anyway, and failing in a place that can explain itself is better.
	// managed is whether pacman owns the running binary, worked out at startup.
	// See New.
	managed bool

	updaterMu  sync.Mutex
	updaterOn  bool
	updaterLog []string
	updaterErr string

	// updaterVersion is the release the last successful install put on disk.
	//
	// Kept because "something was installed" and "this release was installed"
	// are different facts, and the page used the second while only knowing the
	// first: it read the version out of whatever the checker currently offered,
	// so a newer release appearing between installing and restarting made it
	// claim that newer one was installed and waiting. It was not.
	updaterVersion string
}

// updaterState is what the page needs to decide what to show.
type updaterState struct {
	// Enabled is the web_update setting, and Managed is whether pacman owns
	// this installation. Both are reported rather than folded into one flag,
	// because the two impossible cases want different sentences: one is a
	// choice somebody made and the other is how this was installed.
	Enabled bool `json:"enabled"`
	Managed bool `json:"managed"`

	// NeedsPassword is the update_needs_password setting, so the page knows to
	// ask before it posts rather than posting and being turned away.
	NeedsPassword bool `json:"needs_password"`

	// CheckEnabled is the update_check setting, and Checked is when GitHub last
	// answered, null until it has.
	//
	// Both are here for the button that asks now rather than waiting for the
	// six-hourly look. Without Checked the page cannot tell "nothing newer"
	// from "nothing heard", which on a machine that has been off the network
	// since yesterday are different answers with the same appearance.
	CheckEnabled bool       `json:"check_enabled"`
	Checked      *time.Time `json:"checked"`

	Running bool     `json:"running"`
	Log     []string `json:"log"`
	Error   string   `json:"error"`

	// Installed is the release the last successful install put on disk, empty
	// when none has. The page compares it against the release on offer rather
	// than assuming they are the same, because between installing and
	// restarting a newer one can appear.
	Installed string `json:"installed"`

	// Streaming is who is playing right now, so the page can refuse the restart
	// and say whose game it would have ended.
	Streaming []string `json:"streaming"`
}

// New builds the HTTP handler.
func New(manager *seat.Manager, credentials *auth.Store, updates *update.Checker, preparer *prepare.Runner, logger *slog.Logger) http.Handler {
	if preparer == nil {
		preparer = &prepare.Runner{}
	}

	s := &Server{
		manager: manager, auth: credentials, updates: updates,
		prepare: preparer, lanbridge: &lanbridge.Runner{}, log: logger,

		// Asked once, here, and not on every request. It runs pacman, which
		// takes about a tenth of a second, and the interface asks for its state
		// on every change the daemon pushes: during a provision that is several
		// a second, each one forking a process to answer a question whose answer
		// cannot change. It cannot change because the thing it asks about is the
		// running binary, and replacing that means a restart, which comes back
		// through here.
		managed: update.Managed(),
	}

	mux := http.NewServeMux()

	// Reachable without a session. The first two are how you get one; the last
	// is what tells the interface whether it needs to ask.
	mux.HandleFunc("POST /api/setup", s.setup)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/session", s.session)

	guarded := http.NewServeMux()
	guarded.HandleFunc("GET /api/state", s.getState)
	guarded.HandleFunc("GET /api/events", s.events)
	guarded.HandleFunc("POST /api/password", s.changePassword)
	guarded.HandleFunc("POST /api/seats", s.createSeat)
	guarded.HandleFunc("POST /api/provision-stale", s.provisionStale)
	guarded.HandleFunc("POST /api/update", s.applyUpdate)
	guarded.HandleFunc("POST /api/update/check", s.checkUpdate)
	guarded.HandleFunc("POST /api/restart", s.restart)
	guarded.HandleFunc("POST /api/prepare", s.prepareHost)
	guarded.HandleFunc("POST /api/uninstall", s.removeHost)
	guarded.HandleFunc("POST /api/lan-bridge", s.bridgeUplink)
	guarded.HandleFunc("PATCH /api/seats/{name}", s.updateSeat)
	guarded.HandleFunc("DELETE /api/seats/{name}", s.deleteSeat)
	guarded.HandleFunc("GET /api/seats/{name}/log", s.seatLog)
	guarded.HandleFunc("GET /api/seats/{name}/clients", s.pairedClients)
	guarded.HandleFunc("GET /api/seats/{name}/sunshine", s.sunshineAccess)
	guarded.HandleFunc("POST /api/seats/{name}/pair", s.pair)
	guarded.HandleFunc("POST /api/seats/{name}/unpair", s.unpair)
	guarded.HandleFunc("GET /api/seats/{name}/software", s.getSoftware)
	guarded.HandleFunc("GET /api/seats/{name}/software/search", s.searchSoftware)
	guarded.HandleFunc("POST /api/seats/{name}/software", s.installSoftware)
	guarded.HandleFunc("DELETE /api/seats/{name}/software/{id}", s.removeSoftware)
	guarded.HandleFunc("POST /api/seats/{name}/appimages", s.installAppImage)
	guarded.HandleFunc("DELETE /api/seats/{name}/appimages/{file}", s.removeAppImage)
	guarded.HandleFunc("GET /api/library", s.getLibrary)
	guarded.HandleFunc("POST /api/library/sync", s.syncLibrary)
	guarded.HandleFunc("POST /api/library/import", s.importLibrary)
	guarded.HandleFunc("POST /api/library/unwatch", s.unwatchLibrary)
	guarded.HandleFunc("DELETE /api/library/{appid}", s.removeTitle)
	guarded.HandleFunc("POST /api/library/{appid}/offer/{seat}", s.offerTitle)
	guarded.HandleFunc("POST /api/seats/{name}/{action}", s.seatAction)

	mux.Handle("/api/", s.requireSession(guarded))

	// The static files carry nothing worth guarding: they are the same markup
	// for everybody and useless without the API behind them. Serving them
	// openly is what lets the page render a login form at all.
	//
	// Registered without a method. "GET /" and "/api/" overlap without either
	// being the more specific of the two, and the router refuses that pair
	// outright rather than guessing.
	mux.Handle("/", web.Handler())

	return logging(logger, mux)
}

// requireSession rejects anything without a valid session cookie.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(auth.CookieName)
		if err != nil || !s.auth.Valid(cookie.Value) {
			fail(w, http.StatusUnauthorized, errors.New("not logged in"))

			return
		}

		next.ServeHTTP(w, r)
	})
}

func logging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only the calls that change something are worth a line. The interface
		// reads state constantly and logging that would bury everything else.
		if r.Method != http.MethodGet {
			logger.Info("request", "method", r.Method, "path", r.URL.Path)
		}

		next.ServeHTTP(w, r)
	})
}

// --------------------------------------------------------------------- auth

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	source := auth.Source(r)

	if ok, wait := s.auth.Allow(source); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		fail(w, http.StatusTooManyRequests,
			fmt.Errorf("too many attempts, wait %d seconds", int(wait.Seconds())+1))

		return
	}

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	if !s.auth.Check(req.Username, req.Password) {
		s.auth.Failed(source)
		s.log.Warn("failed login", "source", source, "username", req.Username)

		// One message for both a wrong name and a wrong password, so it does
		// not confirm which half was right.
		fail(w, http.StatusUnauthorized, errors.New("wrong user name or password"))

		return
	}

	s.auth.Succeeded(source)
	s.log.Info("login", "source", source, "username", req.Username)
	s.setSession(w, s.auth.Issue())

	writeJSON(w, http.StatusOK, map[string]string{"username": req.Username})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// session tells the interface whether it has to ask for a password, and
// whether there is one to ask for yet.
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(auth.CookieName)
	valid := err == nil && s.auth.Valid(cookie.Value)

	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": valid,
		"username":      s.auth.Username(),
		"setup":         s.auth.NeedsSetup(),

		// Which of the two interfaces this is, answered before there is a
		// session to ask anything else with. The page has to know whether to
		// draw seats or the panel that gets the machine ready, and finding out
		// by fetching state and looking at what came back would mean drawing
		// the wrong one first.
		"ready": s.ready(),
	})
}

type setupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Confirm  string `json:"confirm"`
}

// setup claims a machine nobody has claimed yet.
//
// Unguarded, and that is the whole point: there is no password to authenticate
// against until this has run. It stops working the moment it has, so it is a
// door that closes behind the first person through it. The trade, and why it is
// made, is written down in the auth package and in docs/security.md.
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	if !s.auth.NeedsSetup() {
		fail(w, http.StatusConflict, errors.New("this machine already has a password"))

		return
	}

	var req setupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	if req.Password != req.Confirm {
		fail(w, http.StatusBadRequest, errors.New("the two passwords are not the same"))

		return
	}

	username := strings.TrimSpace(req.Username)
	if username == "" {
		username = "admin"
	}

	if err := s.auth.Claim(username, req.Password); err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	// Signed in straight away. Asking somebody to type a password they chose
	// one second ago proves nothing and is one more thing to get wrong.
	s.setSession(w, s.auth.Issue())
	s.log.Info("password set for the first time", "username", username,
		"source", auth.Source(r))

	writeJSON(w, http.StatusOK, map[string]string{"username": username})
}

func (s *Server) setSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(auth.SessionTTL / time.Second),
		HttpOnly: true,

		// Secure, because the interface only ever speaks TLS. SameSite strict
		// is what keeps another site from making a browser act on this session:
		// every state changing call here is a plain request with a cookie, and
		// without this a link in a mail could delete a seat.
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

type passwordRequest struct {
	Username string `json:"username"`
	Current  string `json:"current"`
	New      string `json:"new"`
	Confirm  string `json:"confirm"`
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var req passwordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	// Asked for again even though the caller is already logged in, so that a
	// borrowed browser cannot be turned into a permanent one.
	if !s.auth.Check(s.auth.Username(), req.Current) {
		fail(w, http.StatusUnauthorized, errors.New("the current password is wrong"))

		return
	}

	// Typed twice, and compared here rather than only in the browser. A
	// mistyped password locks somebody out of their own machine, and the file
	// it is stored in cannot be read back to find out what they actually typed.
	if req.New != req.Confirm {
		fail(w, http.StatusBadRequest, errors.New("the two passwords are not the same"))

		return
	}

	username := req.Username
	if username == "" {
		username = s.auth.Username()
	}

	if err := s.auth.SetPassword(username, req.New); err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	// Changing the password ends every session, including this one, so the
	// caller gets a fresh cookie rather than being thrown out of the page they
	// are standing on.
	s.setSession(w, s.auth.Issue())
	s.log.Info("password changed", "username", username)

	writeJSON(w, http.StatusOK, map[string]string{"username": username})
}

// -------------------------------------------------------------------- state

type stateResponse struct {
	Seats []seat.Status `json:"seats"`

	// ProvisioningAll is set while every stale seat is being provisioned in
	// turn, so that the button that started it can say so rather than looking
	// like it did nothing for the minutes before the first seat changes state.
	ProvisioningAll bool `json:"provisioning_all"`

	Observer string        `json:"observer"`
	Config   config.Config `json:"config"`
	Uplinks  []string      `json:"uplinks"`
	Host     hostInfo      `json:"host"`

	// Update is the published release this build does not have, null when
	// there is none or when nothing was asked. Not omitted when it is null:
	// a field that is sometimes absent is a field every caller has to guard
	// twice, and this page has been broken once already by exactly that.
	Update  *update.Release `json:"update"`
	Updater updaterState    `json:"updater"`

	// Ready is whether this is the whole interface or the one that comes up
	// when the daemon cannot reach Incus. Always true here and always false
	// there, and sent from both so that the page has one field to branch on
	// rather than a guess about which keys are missing.
	Ready bool `json:"ready"`

	// Prepare and Remove are the two things the interface does to the machine
	// rather than to a seat. Both are here rather than behind endpoints of
	// their own, because the page needs to know whether to offer them at all
	// before anybody presses anything: an install from a checkout has no
	// polyseat-prepare to run, and a configuration can turn either off.
	Prepare prepare.State `json:"prepare"`
	Remove  removeState   `json:"remove"`

	// LanBridge is the third, and the one whose result is already reported
	// twice over: host.uplink_bridged says what the machine looks like now, and
	// this says what the last attempt to change it did. Both, because between
	// pressing the button and the address arriving on the bridge there are
	// several seconds in which the first field is still answering the old
	// question truthfully.
	LanBridge lanbridge.State `json:"lan_bridge"`

	Warnings []string  `json:"warnings"`
	Now      time.Time `json:"now"`
}

// removeState is what the page needs to decide whether to offer removal.
type removeState struct {
	// Enabled is the web_uninstall setting and Available is whether the script
	// that does the work is installed. Reported separately for the same reason
	// the updater reports two: one is a choice somebody made and the other is
	// how this was installed, and the sentences differ.
	Enabled   bool   `json:"enabled"`
	Available bool   `json:"available"`
	Reason    string `json:"reason"`

	// Running is set between handing the removal to systemd and this process
	// being stopped by it, which is a couple of seconds during which the page
	// should say what is happening rather than nothing.
	Running bool `json:"running"`
}

type hostInfo struct {
	Hostname string `json:"hostname"`

	// Version is what this daemon was built from. Behind the session like the
	// rest of the state rather than on the login page, because the version of a
	// root daemon answering on the whole network is worth exactly as much to a
	// stranger as it is to its owner.
	//
	// Shown at all because two questions need it and neither has another answer:
	// what to put in a bug report, and whether the binary on disk is the one
	// actually running after an install.
	Version string `json:"version"`

	// GPU is the card every seat on this machine is built for, as the daemon
	// found it at startup. Shown because it decides the whole shape of a seat
	// and because it is the first thing worth knowing when a seat reports the
	// software encoder: a card detected as the wrong vendor produces exactly
	// that, and nothing else in the interface would say so.
	GPU string `json:"gpu"`

	// GPUVendor is empty when nothing usable was found. Sent alongside the
	// description rather than left for the page to work out from the text,
	// because deciding whether something is broken by reading a sentence is
	// how a message becomes impossible to reword.
	GPUVendor string `json:"gpu_vendor"`

	// Uplink is the interface the seats reach the LAN through, and
	// UplinkBridged says whether it is a bridge.
	//
	// Sent because one per seat setting means nothing without it: a seat can
	// only reach this machine over the LAN if the uplink is a bridge, and on a
	// plain interface every seat is isolated whatever anybody ticks. A checkbox
	// that silently does nothing is worse than one that says why.
	Uplink        string `json:"uplink"`
	UplinkBridged bool   `json:"uplink_bridged"`

	// UplinkWireless is the third thing worth knowing about it, and the one
	// the interface used to be silent about while prepare.sh and
	// `polyseatd -report` both said it plainly. A seat cannot take a macvlan
	// from a wireless interface and the interface cannot be bridged either, so
	// a machine whose uplink is wireless has no working arrangement at all,
	// rather than the isolated one it looks like from here.
	UplinkWireless bool `json:"uplink_wireless"`

	// UplinkReason is why that one and not another, which became worth sending
	// when the daemon started choosing. A machine that reaches the network over
	// wifi and hands its seats the ethernet port beside it is doing something
	// nobody asked it to, and the page should be able to say so rather than
	// showing a name that matches nothing the operator set.
	UplinkReason string `json:"uplink_reason"`
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	seats, err := s.manager.List()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)

		return
	}

	hostname, _ := os.Hostname()

	resp := stateResponse{
		Seats:           seats,
		ProvisioningAll: s.manager.ProvisioningAll(),
		Observer:        s.manager.ObserverState(),
		Config:          s.manager.Config(),
		Uplinks:         config.Uplinks(),
		Host: hostInfo{
			Hostname:       hostname,
			Version:        version.Version,
			GPU:            s.manager.GPU().String(),
			GPUVendor:      string(s.manager.GPU().Vendor),
			Uplink:         s.manager.Uplink(),
			UplinkBridged:  s.manager.UplinkBridged(),
			UplinkWireless: s.manager.UplinkWireless(),
			UplinkReason:   s.manager.UplinkReason(),
		},
		Update:    s.updates.Available(),
		Updater:   s.updaterState(),
		Ready:     true,
		Prepare:   s.prepare.State(),
		Remove:    s.removeState(),
		LanBridge: s.lanbridge.State(),
		Warnings:  s.warnings(),
		Now:       time.Now(),
	}

	// Always send arrays, never null. A client that has to guard every list
	// against absence gets it wrong exactly once, silently.
	if resp.Seats == nil {
		resp.Seats = []seat.Status{}
	}

	if resp.Warnings == nil {
		resp.Warnings = []string{}
	}

	writeJSON(w, http.StatusOK, resp)
}

// udevRuleDirs is where the rule can legitimately be, in the order udev reads
// them.
//
// Two installers put it in two places: host/install.sh writes /etc, which is
// where a local administrator's rules go, and the package owns /usr/lib, which
// is where a distribution's do. /run and /usr/local/lib are here because udev
// reads them too, and a check that knows less about the system than udev does
// is a check that will one day disagree with it.
//
// Looking in one of them was a real bug and not a hypothetical: every package
// install was told, permanently and in the interface, that the rule protecting
// it was missing while the rule was in place and working. A security warning
// that cries wolf is worse than none, because the next one is not read either.
var udevRuleDirs = []string{
	"/etc/udev/rules.d",
	"/run/udev/rules.d",
	"/usr/local/lib/udev/rules.d",
	"/usr/lib/udev/rules.d",
}

// udevRuleName is the file, and the number in it is not cosmetic. At 70 it
// sorted before 70-uaccess.rules, which put the tag it had just stripped
// straight back on.
const udevRuleName = "72-polyseat-hide.rules"

// udevRuleInstalled says whether any of those directories has it.
func udevRuleInstalled() bool {
	for _, dir := range udevRuleDirs {
		if _, err := os.Stat(filepath.Join(dir, udevRuleName)); err == nil {
			return true
		}
	}

	return false
}

// warnings are the host level things a seat cannot fix for itself. They belong
// in the interface rather than only in a script nobody runs.
func (s *Server) warnings() []string {
	var out []string

	// Two ways of not running, and they want different sentences. Down is
	// something that may come back on its own; given up is a fact about this
	// kernel, and saying "not running" for it sends somebody looking for a
	// crash that is not there. The cause is named because it is one command to
	// fix and nothing else in the interface hints at it: /dev/uhid exists
	// either way, so everything looks correctly installed.
	switch s.manager.ObserverState() {
	case "running":
	case "failed":
		out = append(out, "The uhid observer gave up: this kernel has no "+
			"uhid_dev_create2 for it to watch, which almost always means uhid "+
			"is a module that is not loaded. Prepare this host, under Host, "+
			"loads it and keeps it loaded across a reboot, and the probe "+
			"attaches when the daemon restarts. Gamepads go on working; "+
			"they are attributed to a seat by name rather than structurally "+
			"until then.")
	default:
		out = append(out, "The uhid observer is not running. Gamepads can then "+
			"only be attributed to a seat by name, not structurally.")
	}

	if !udevRuleInstalled() {
		out = append(out, "The udev rule that hides seat input devices from the "+
			"host desktop is not installed. Install Polyseat again: the package "+
			"places it, and so does host/install.sh.")
	}

	// The number is not cosmetic. At 70 the rule sorted before
	// 70-uaccess.rules, which put the tag it had just stripped straight back
	// on, so a copy left behind at the old name is a rule that runs and does
	// nothing. Worth saying out loud, because everything looks installed.
	if _, err := os.Stat("/etc/udev/rules.d/70-polyseat-hide.rules"); err == nil {
		out = append(out, "An old copy of the udev rule is still installed as "+
			"70-polyseat-hide.rules. It loses to 70-uaccess.rules, which "+
			"grants the tag back. Remove it and run host/install.sh.")
	}

	if _, err := os.Stat("/dev/uhid"); err != nil {
		out = append(out, "/dev/uhid does not exist on the host, so no seat can "+
			"have a gamepad. Load the uhid module.")
	}

	// Said here rather than only in the readme, because whoever opens this page
	// on an AMD machine has quite possibly not read the readme, and this is the
	// one fact that changes what they should expect from the next ten minutes.
	//
	// It is a warning and not a note on purpose. The whole path was written and
	// reasoned about without a card to run it on, so the honest thing to say is
	// that this machine is the first, and to ask for what comes back. It goes
	// away when somebody confirms it, which is a change to this line and not to
	// the code it describes.
	// The two ways an uplink can be no uplink. Both were quiet here: the page
	// showed the interface name and whether it was a bridge, which on a
	// wireless machine reads as "your seats are isolated" when the truth is
	// that no seat on this machine can have a network at all. prepare.sh has
	// warned about it since it existed and `polyseatd -report` prints it, and
	// neither is where somebody looks after pressing a button that failed.
	switch {
	case s.manager.Uplink() == "":
		out = append(out, "No seat on this host can be given a network: "+
			s.manager.UplinkReason()+".")

	case s.manager.UplinkWireless():
		out = append(out, s.manager.Uplink()+" is wireless, and no seat can use "+
			"it. 802.11 carries one MAC address per association, so a seat cannot "+
			"have one of its own: a macvlan is refused by the driver and bridging "+
			"would need 4-address mode at both ends, which ordinary access points "+
			"do not do. Seats need a wired interface. It does not have to be the "+
			`one this host reaches the internet over: set "uplink" in `+
			"/etc/polyseat/polyseatd.json to a wired card on the same network and "+
			"the seats will use that while this host stays on wifi.")
	}

	if s.manager.GPU().Vendor == seat.VendorAMD {
		out = append(out, "This is an AMD machine, and Polyseat's AMD support "+
			"has never been run on real hardware by its author. It is expected "+
			"to work and it may well not. Whichever it turns out to be, please "+
			"say so at github.com/superuser404notfound/Polyseat/issues, with "+
			"the output of: sudo polyseatd -report")
	}

	return out
}

// ------------------------------------------------------------------- events

// events streams a token whenever anything changes.
//
// Server sent events rather than a websocket: the interface only ever listens,
// and this needs no library on either end.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, http.StatusInternalServerError, errors.New("streaming is not supported"))

		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	changes, unsubscribe := s.manager.Subscribe()
	defer unsubscribe()

	_, _ = fmt.Fprint(w, "event: hello\ndata: {}\n\n")
	flusher.Flush()

	// A comment line every so often, so a proxy or a sleeping phone does not
	// quietly drop the connection without either end noticing.
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case <-changes:
			_, _ = fmt.Fprint(w, "event: change\ndata: {}\n\n")
			flusher.Flush()

		case <-keepalive.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// -------------------------------------------------------------------- seats

type seatRequest struct {
	Name       *string `json:"name"`
	Label      *string `json:"label"`
	Autostart  *bool   `json:"autostart"`
	Resolution *string `json:"resolution"`
	Address    *string `json:"address"`
	Gateway    *string `json:"gateway"`
	Library    *bool   `json:"library"`

	// HostAccess is the positive form of Seat.Isolated: ticked means the seat
	// and this machine can reach each other on the LAN. Inverted here rather
	// than in the page, so that the one place where the two polarities meet is
	// this file and not a line of JavaScript.
	HostAccess *bool `json:"host_access"`

	PointerSpeed *float64 `json:"pointer_speed"`
}

// provisionStale brings every seat an older generation built up to date.
//
// One request rather than one per seat, because the seats being behind is one
// situation and dealing with it should be one action. Before this, an update
// meant opening each card in turn and remembering which ones had been done.
func (s *Server) provisionStale(w http.ResponseWriter, r *http.Request) {
	names, err := s.manager.ProvisionStale()
	if err != nil {
		fail(w, statusFor(err), err)

		return
	}

	// An empty list rather than null, which is this file's own rule elsewhere: a
	// client that has to guard every list against absence gets it wrong exactly
	// once, silently.
	if names == nil {
		names = []string{}
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"seats": names})
}

func (s *Server) createSeat(w http.ResponseWriter, r *http.Request) {
	var req seatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	if req.Name == nil {
		fail(w, http.StatusBadRequest, errors.New("a seat needs a name"))

		return
	}

	created := seat.Seat{
		Name:       *req.Name,
		Label:      value(req.Label, *req.Name),
		Autostart:  value(req.Autostart, true),
		Resolution: value(req.Resolution, "1920x1080@60Hz"),
		Address:    value(req.Address, ""),
		Gateway:    value(req.Gateway, ""),

		// On for a new seat, because somebody creating a second seat on a
		// machine that already has games is asking for exactly this. Existing
		// seats keep whatever they had, which for seats built before M6 is off.
		Library: value(req.Library, true),

		// A new seat can reach the host unless somebody says otherwise, which
		// is the whole reason for bridging the uplink. On a plain interface it
		// is isolated regardless and the interface says so.
		Isolated: !value(req.HostAccess, true),

		// Zero rather than the default written out, so that a change to the
		// default reaches every seat that never chose a number of its own.
		PointerSpeed: value(req.PointerSpeed, 0),
	}

	if err := s.manager.Create(created); err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"name": created.Name})
}

func (s *Server) updateSeat(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	var req seatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	err := s.manager.Update(name, func(current *seat.Seat) {
		if req.Label != nil {
			current.Label = *req.Label
		}

		if req.Autostart != nil {
			current.Autostart = *req.Autostart
		}

		if req.Resolution != nil {
			current.Resolution = *req.Resolution
		}

		if req.Address != nil {
			current.Address = *req.Address
		}

		if req.Gateway != nil {
			current.Gateway = *req.Gateway
		}

		if req.Library != nil {
			current.Library = *req.Library
		}

		if req.HostAccess != nil {
			current.Isolated = !*req.HostAccess
		}

		if req.PointerSpeed != nil {
			current.PointerSpeed = *req.PointerSpeed
		}
	})
	if err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

func (s *Server) deleteSeat(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	keep := r.URL.Query().Get("keep_container") == "1"

	if err := s.manager.Delete(name, keep); err != nil {
		fail(w, statusFor(err), err)

		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"name": name})
}

func (s *Server) seatLog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"lines": s.manager.Log(r.PathValue("name")),
	})
}

// ------------------------------------------------------------------ pairing

func (s *Server) pairedClients(w http.ResponseWriter, r *http.Request) {
	devices, err := s.manager.PairedDevices(r.Context(), r.PathValue("name"))
	if err != nil {
		fail(w, http.StatusBadGateway, err)

		return
	}

	if devices == nil {
		devices = []sunshine.Device{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

// sunshineAccess hands out a seat's own Sunshine login.
//
// Its own endpoint rather than a field in the state, so a password is fetched
// when somebody asks for it instead of travelling with every refresh the
// interface makes.
func (s *Server) sunshineAccess(w http.ResponseWriter, r *http.Request) {
	access, err := s.manager.SunshineCredentials(r.PathValue("name"))
	if err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	writeJSON(w, http.StatusOK, access)
}

type pairRequest struct {
	Pin  string `json:"pin"`
	Name string `json:"name"`
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	var req pairRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	if req.Pin == "" {
		fail(w, http.StatusBadRequest, errors.New("Moonlight shows a PIN, it goes here"))

		return
	}

	if req.Name == "" {
		req.Name = "device"
	}

	if err := s.manager.Pair(r.Context(), r.PathValue("name"), req.Pin, req.Name); err != nil {
		fail(w, http.StatusBadGateway, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) unpair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UUID string `json:"uuid"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	if err := s.manager.Unpair(r.Context(), r.PathValue("name"), req.UUID); err != nil {
		fail(w, http.StatusBadGateway, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) seatAction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	var err error

	switch action := r.PathValue("action"); action {
	case "start":
		err = s.manager.Start(name)
	case "stop":
		err = s.manager.Stop(name)
	case "provision":
		err = s.manager.Provision(name)
	case "update-software":
		err = s.manager.UpdateSoftware(name)
	case "check-updates":
		_, err = s.manager.CheckFreshness(name)
	case "cancel":
		s.manager.Cancel(name)
	default:
		fail(w, http.StatusNotFound, fmt.Errorf("no such action: %s", action))

		return
	}

	if err != nil {
		fail(w, statusFor(err), err)

		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"name": name})
}

// ----------------------------------------------------------------- software

// softwareRequest names one thing to install.
type softwareRequest struct {
	ID string `json:"id"`
}

func (s *Server) getSoftware(w http.ResponseWriter, r *http.Request) {
	status, err := s.manager.Software(r.Context(), r.PathValue("name"))
	if err != nil {
		fail(w, statusFor(err), err)

		return
	}

	writeJSON(w, http.StatusOK, status)
}

func (s *Server) searchSoftware(w http.ResponseWriter, r *http.Request) {
	found, err := s.manager.SearchSoftware(r.Context(),
		r.PathValue("name"), r.URL.Query().Get("q"))
	if err != nil {
		fail(w, statusFor(err), err)

		return
	}

	// A list, never null, because the page maps over it.
	writeJSON(w, http.StatusOK, map[string]any{"results": found})
}

func (s *Server) installSoftware(w http.ResponseWriter, r *http.Request) {
	var req softwareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	if err := seat.ValidateAppID(req.ID); err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	if err := s.manager.InstallSoftware(r.PathValue("name"), req.ID); err != nil {
		fail(w, statusFor(err), err)

		return
	}

	// Accepted rather than OK: a flatpak is hundreds of megabytes and the
	// answer here means the download has started, not that it finished. The
	// seat log is where it is followed.
	writeJSON(w, http.StatusAccepted, map[string]string{"id": req.ID})
}

func (s *Server) removeSoftware(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := seat.ValidateAppID(id); err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	if err := s.manager.RemoveSoftware(r.PathValue("name"), id); err != nil {
		fail(w, statusFor(err), err)

		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"id": id})
}

// ----------------------------------------------------------------- appimages

// appImageRequest names one to download.
type appImageRequest struct {
	URL string `json:"url"`
}

func (s *Server) installAppImage(w http.ResponseWriter, r *http.Request) {
	var req appImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	// Here as well as in the manager, so that a bad address comes back as a bad
	// request rather than as a seat that goes busy and then reports a problem
	// in its log.
	file, err := seat.ValidateAppImageURL(req.URL)
	if err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	if err := s.manager.InstallAppImage(r.PathValue("name"), req.URL); err != nil {
		fail(w, statusFor(err), err)

		return
	}

	// Accepted rather than OK, for the same reason a flatpak is: an emulator is
	// several hundred megabytes and this means the download has started.
	writeJSON(w, http.StatusAccepted, map[string]string{"file": file})
}

func (s *Server) removeAppImage(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")

	if err := seat.ValidateAppImageFile(file); err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	if err := s.manager.RemoveAppImage(r.PathValue("name"), file); err != nil {
		fail(w, statusFor(err), err)

		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"file": file})
}

// ------------------------------------------------------------------ library

// arrays replaces the nil slices in a report with empty ones.
//
// A field that vanishes into null when it is empty is how the seat list once
// stopped rendering altogether: the client called map on it and threw before it
// drew anything. Lists go over the wire as lists.
func arrays(r library.Report) library.Report {
	if r.Harvested == nil {
		r.Harvested = []library.Move{}
	}

	if r.Delivered == nil {
		r.Delivered = []library.Move{}
	}

	if r.Declined == nil {
		r.Declined = []library.Move{}
	}

	if r.Problems == nil {
		r.Problems = []string{}
	}

	if r.Pending == nil {
		r.Pending = []library.Move{}
	}

	return r
}

// libraryStatus reads the pool with every empty list as a list rather than as
// null.
//
// Used by all four library endpoints, not only the one that reads. Normalising
// in one of them and not the others is how three of these would have gone out
// able to break the page: a field that disappears when it is empty is exactly
// what once stopped the seat list rendering at all.
func (s *Server) libraryStatus() seat.LibraryStatus {
	status := s.manager.Library()

	if status.Titles == nil {
		status.Titles = []library.Title{}
	}

	if status.Candidates == nil {
		status.Candidates = []string{}
	}

	if status.Outside == nil {
		status.Outside = []string{}
	}

	if status.Sources == nil {
		status.Sources = []string{}
	}

	for i, title := range status.Titles {
		if title.In == nil {
			status.Titles[i].In = []string{}
		}

		if title.Declined == nil {
			status.Titles[i].Declined = []string{}
		}

		if title.Stale == nil {
			status.Titles[i].Stale = []string{}
		}
	}

	return status
}

func (s *Server) getLibrary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.libraryStatus())
}

func (s *Server) syncLibrary(w http.ResponseWriter, r *http.Request) {
	if err := s.manager.SyncLibrary(r.Context()); err != nil {
		fail(w, statusFor(err), err)

		return
	}

	writeJSON(w, http.StatusOK, s.libraryStatus())
}

type importRequest struct {
	Path string `json:"path"`
}

// importLibrary pulls an existing Steam library into the pool.
//
// The path comes from whoever is logged in and is used as given. That is not an
// oversight: this daemon already creates containers and writes anywhere on the
// host as root, so a session that can reach this endpoint can do far worse than
// name a directory. The only check is that the directory looks like a Steam
// library, which is there to catch a typo rather than an attacker.
func (s *Server) importLibrary(w http.ResponseWriter, r *http.Request) {
	var req importRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	if req.Path == "" {
		fail(w, http.StatusBadRequest, errors.New("no path given"))

		return
	}

	if !filepath.IsAbs(req.Path) {
		fail(w, http.StatusBadRequest, errors.New("the path has to be absolute"))

		return
	}

	if _, err := os.Stat(filepath.Join(req.Path, "common")); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf(
			"%s does not look like a steamapps directory, there is no common folder in it", req.Path))

		return
	}

	report, err := s.manager.ImportLibrary(r.Context(), req.Path)
	if err != nil {
		fail(w, statusFor(err), err)

		return
	}

	writeJSON(w, http.StatusOK, arrays(report))
}

// unwatchLibrary stops tracking a library the pool was watching.
//
// A counterpart to importLibrary rather than a symmetry for its own sake: the
// daemon now adopts a library it finds by itself, and a decision it makes has to
// be one somebody can take back.
func (s *Server) unwatchLibrary(w http.ResponseWriter, r *http.Request) {
	var req importRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	if req.Path == "" {
		fail(w, http.StatusBadRequest, errors.New("no path given"))

		return
	}

	if err := s.manager.UnwatchLibrary(req.Path); err != nil {
		fail(w, statusFor(err), err)

		return
	}

	writeJSON(w, http.StatusOK, s.libraryStatus())
}

func (s *Server) removeTitle(w http.ResponseWriter, r *http.Request) {
	if err := s.manager.RemoveFromLibrary(r.PathValue("appid")); err != nil {
		fail(w, statusFor(err), err)

		return
	}

	writeJSON(w, http.StatusOK, s.libraryStatus())
}

func (s *Server) offerTitle(w http.ResponseWriter, r *http.Request) {
	err := s.manager.OfferToSeat(r.Context(), r.PathValue("seat"), r.PathValue("appid"))
	if err != nil {
		fail(w, statusFor(err), err)

		return
	}

	writeJSON(w, http.StatusOK, s.libraryStatus())
}

// ------------------------------------------------------------------ helpers

func statusFor(err error) int {
	switch {
	case errors.Is(err, seat.ErrBusy):
		return http.StatusConflict
	// Somebody playing is a conflict rather than a bad request in the same way
	// a busy seat is: nothing about the request was wrong, it is the moment
	// that is, and the same request works once the stream ends.
	case errors.Is(err, seat.ErrStreaming):
		return http.StatusConflict
	case errors.Is(err, seat.ErrNoLibrary):
		return http.StatusServiceUnavailable
	case os.IsNotExist(err):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

func value[T any](p *T, fallback T) T {
	if p == nil {
		return fallback
	}

	return *p
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")

	// Every answer this interface gives about the machine goes out through
	// here, and a browser that keeps one is a page that never changes again.
	//
	// These carried no cache headers at all: no Cache-Control, no ETag, no
	// Last-Modified. That is an invitation to a heuristic, and Firefox took it.
	// The daemon found a new release, said so on every ask, and the page went
	// on showing the state from before its first check. Nothing failed and
	// nothing was logged: the button's POST reached the daemon, because a POST
	// is never served from a cache, and the state that would have shown the
	// answer came off disk. It made the update button do nothing at all, which
	// is the same symptom as the release before last and a different cause.
	//
	// no-store rather than no-cache, and the same header the static files have
	// carried all along. There is nothing here worth keeping for a second: the
	// body is what one machine is doing right now, and half of it is only true
	// for the session that asked.
	w.Header().Set("Cache-Control", "no-store")

	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		return
	}
}

func fail(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// ------------------------------------------------------------------- updating

// config is the configuration, from wherever this server has one.
//
// The manager owns it in the ordinary case, because the manager is what acts on
// it. In setup mode there is no manager, and the copy the daemon loaded at
// startup is the whole truth.
func (s *Server) config() config.Config {
	if s.manager == nil {
		return s.setupConfig
	}

	return s.manager.Config()
}

// streaming is who is playing right now, and nobody at all when there are no
// seats to play in.
func (s *Server) streaming() []string {
	if s.manager == nil {
		return nil
	}

	return s.manager.Streaming()
}

// updating says whether an update started from the interface is running. Asked
// by everything else that runs pacman, because the second one would sit on the
// database lock and fail in a way that says nothing about why.
func (s *Server) updating() bool {
	s.updaterMu.Lock()
	defer s.updaterMu.Unlock()

	return s.updaterOn
}

// notify pushes a change to the pages that are watching, where there is
// anything to push it through.
func (s *Server) notify() {
	if s.manager != nil {
		s.manager.Notify()
	}
}

// updaterState answers what the page needs without doing anything.
func (s *Server) updaterState() updaterState {
	s.updaterMu.Lock()
	defer s.updaterMu.Unlock()

	out := updaterState{
		Enabled:       s.config().WebUpdate,
		NeedsPassword: s.config().UpdateNeedsPassword,
		CheckEnabled:  s.updates.Enabled(),
		Managed:       s.managed,
		Running:       s.updaterOn,
		Log:           append([]string{}, s.updaterLog...),
		Error:         s.updaterErr,
		Installed:     s.updaterVersion,
		Streaming:     s.streaming(),
	}

	if out.Streaming == nil {
		out.Streaming = []string{}
	}

	if when := s.updates.LastCheck(); !when.IsZero() {
		out.Checked = &when
	}

	return out
}

// confirmed checks the password again, when the setting says to.
//
// Rate limited through the same counter as the login form, and not a second one
// of its own. Two independent limiters guarding one secret means an attacker
// gets both budgets, and this endpoint would be the cheaper of the two to spend
// because it needs no user name.
//
// The session is still required: this is guarded like every other handler here,
// so a password alone reaches nothing. It is a second question, not a second
// door.
func (s *Server) confirmed(r *http.Request) error {
	return confirmPassword(s.auth, s.config().UpdateNeedsPassword, r)
}

// confirmPassword is the check itself, apart from the server that calls it.
//
// A function of a store, a setting and a request, so that what it accepts and
// refuses can be checked without a seat manager and an Incus behind it. That is
// not a tidying: this is the guard between a session and running pacman as
// root, and a guard nobody has watched fail has not been tested.
func confirmPassword(store *auth.Store, needed bool, r *http.Request) error {
	if !needed {
		return nil
	}

	source := auth.Source(r)

	if ok, wait := store.Allow(source); !ok {
		// Not counted as a failure. This attempt was never tested against the
		// password, and counting it would let a locked out address extend its
		// own lockout by continuing to knock, which punishes nobody who matters.
		return fmt.Errorf("too many attempts, wait %d seconds", int(wait.Seconds())+1)
	}

	// Recorded here rather than left to the caller. The first version of this
	// left it to the handler and the guard was defenceless anywhere else: Allow
	// counts nothing on its own, so a hundred wrong passwords in a row were
	// never slowed down. Found by the test that tries exactly that.
	wrong := func() error {
		store.Failed(source)

		return errors.New("wrong password")
	}

	var req struct {
		Password string `json:"password"`
	}

	// A body that will not parse is a wrong password rather than a bad request.
	// The distinction would only ever tell somebody guessing which of the two
	// they got wrong.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return wrong()
	}

	// A missing field, a JSON null and an empty string all arrive here as "",
	// and all three are refused by Check rather than by a guard above it. There
	// was a guard above it, and breaking it on purpose changed nothing: an
	// empty password cannot be set in the first place, because SetPassword
	// enforces MinPasswordLength, so no stored hash can ever match one. A check
	// that cannot fail reads as protection while providing none.
	if !store.Check(store.Username(), req.Password) {
		return wrong()
	}

	store.Succeeded(source)

	return nil
}

// checkUpdate asks GitHub now, instead of waiting for the next six-hourly look.
//
// It reaches the network and changes nothing on this machine, which is why it
// needs neither the password nor web_update: looking is what update_check
// governs, and installing is the handler below. A POST rather than a GET all
// the same, because it is an action with an effect somewhere else, and because
// every state changing call here is a POST guarded by a strict SameSite cookie.
//
// Nothing limits how often it may be pressed. It is behind a session, one
// person presses it, and the only thing on the other end that could be spent is
// GitHub's own unauthenticated rate limit, which answers for itself and says so
// in the sentence the page prints.
func (s *Server) checkUpdate(w http.ResponseWriter, r *http.Request) {
	// Not the request's context alone: a browser that goes away mid-check would
	// otherwise cancel a request whose answer this daemon keeps either way.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 45*time.Second)
	defer cancel()

	release, err := s.updates.CheckNow(ctx)
	if err != nil {
		fail(w, http.StatusConflict, err)

		return
	}

	s.log.Info("checked for a newer Polyseat from the interface",
		"found", release != nil)

	// So that every other page open on this machine learns what this one just
	// asked, rather than only the one that pressed the button.
	s.notify()

	writeJSON(w, http.StatusOK, map[string]any{"release": release})
}

// applyUpdate installs the release the daemon found, and nothing else.
//
// The request body is not read and there is no parameter to read: which release
// this is comes from the checker, and the file and address come from that
// release. That is what keeps a stolen session from turning into a machine
// installing somebody's own package, and it is the property to preserve if this
// handler ever grows an argument.
func (s *Server) applyUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.config().WebUpdate {
		fail(w, http.StatusForbidden, errors.New(`updating from the interface is off. Set "web_update": true in /etc/polyseat/polyseatd.json, or use host/update.sh`))

		return
	}

	// Asked before anything is looked up, so that a wrong password learns
	// nothing about the machine it was aimed at.
	if err := s.confirmed(r); err != nil {
		// Counted inside confirmPassword, not here, or one wrong password would
		// spend two of the attempts an address is allowed.
		s.log.Warn("an update was asked for with the wrong password",
			"source", auth.Source(r))
		fail(w, http.StatusUnauthorized, err)

		return
	}

	// Two pacman transactions at once is one database lock and a failure that
	// names the lock rather than the reason. Refused where it can be explained.
	if s.prepare.Running() {
		fail(w, http.StatusConflict, errors.New("this machine is being prepared, which is already running pacman. Wait for that to finish"))

		return
	}

	rel := s.updates.Available()
	if rel == nil {
		fail(w, http.StatusConflict, errors.New("there is no newer release to install"))

		return
	}

	if rel.Package == nil {
		fail(w, http.StatusConflict, fmt.Errorf("%s has no package attached to it, so it cannot be installed from here", rel.Version))

		return
	}

	if !s.managed {
		fail(w, http.StatusConflict, errors.New("this Polyseat was not installed from the package, so pacman has nothing to replace. Use host/update.sh in the checkout it was built from"))

		return
	}

	s.updaterMu.Lock()

	if s.updaterOn {
		s.updaterMu.Unlock()
		fail(w, http.StatusConflict, errors.New("an update is already running"))

		return
	}

	s.updaterOn = true
	s.updaterErr = ""
	s.updaterLog = nil
	s.updaterVersion = ""
	s.updaterMu.Unlock()

	s.notify()

	// Detached from the request on purpose. A seven megabyte download and a
	// pacman transaction outlast a phone locking its screen, and an update that
	// stopped halfway because somebody put their phone in their pocket would be
	// the worst possible way for this to fail.
	go s.runUpdate(rel)

	writeJSON(w, http.StatusAccepted, map[string]any{"version": rel.Version})
}

// runUpdate does the work and records what happened for the page to read.
func (s *Server) runUpdate(rel *update.Release) {
	s.log.Info("installing a newer Polyseat from the interface",
		"version", rel.Version, "package", rel.Package.Name)

	progress := func(line string) {
		s.updaterMu.Lock()
		s.updaterLog = append(s.updaterLog, line)
		s.updaterMu.Unlock()

		s.log.Info("update", "line", line)
		s.notify()
	}

	// Not the request's context, which ends when the browser goes away, and a
	// generous ceiling rather than none: this reaches the network and runs
	// pacman, and both can hang rather than fail.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	err := update.Apply(ctx, rel, progress)

	s.updaterMu.Lock()
	s.updaterOn = false

	if err != nil {
		s.updaterErr = err.Error()
	} else {
		s.updaterVersion = rel.Version
	}

	s.updaterMu.Unlock()

	if err != nil {
		s.log.Error("the update failed", "error", err)
	} else {
		s.log.Info("the new version is on disk. It serves after a restart",
			"version", rel.Version)
	}

	s.notify()
}

// restart schedules a restart of the daemon, refusing while somebody plays.
//
// Separate from the update on purpose. Replacing the binary leaves the running
// process alone, so the new version sits on disk until this is asked for, and
// the moment is somebody's to choose. This is also the one thing the interface
// does better than host/update.sh, which has to work out whether anybody is
// streaming: the interface already knows.
func (s *Server) restart(w http.ResponseWriter, r *http.Request) {
	if !s.config().WebUpdate {
		fail(w, http.StatusForbidden, errors.New(`restarting from the interface is off. Set "web_update": true in /etc/polyseat/polyseatd.json`))

		return
	}

	// Not in the middle of preparing this machine. The script runs as a child
	// of this process and a restart takes the whole control group with it, so
	// this would leave a pacman killed halfway, a lock behind it and a
	// transaction half applied.
	if s.prepare.Running() {
		fail(w, http.StatusConflict, errors.New("this machine is being prepared. A restart now would kill pacman halfway through it"))

		return
	}

	// force is the only parameter either of these handlers takes, and it says
	// "yes, end their game", which is a thing somebody may legitimately mean on
	// their own machine. It cannot name what to install or what to run.
	if r.URL.Query().Get("force") != "true" {
		if busy := s.streaming(); len(busy) > 0 {
			fail(w, http.StatusConflict, fmt.Errorf("somebody is streaming on %s. Restarting now would drop their controller", strings.Join(busy, ", ")))

			return
		}
	}

	if err := update.Restart(); err != nil {
		fail(w, http.StatusInternalServerError, err)

		return
	}

	s.log.Info("a restart was asked for from the interface")

	writeJSON(w, http.StatusAccepted, map[string]any{"restarting": true})
}
