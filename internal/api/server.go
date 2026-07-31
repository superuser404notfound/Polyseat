// Package api serves the daemon's HTTP interface and the web pages that use it.
//
// There is no command line. Everything Polyseat can do is done here, which is
// the whole reason the daemon owns the configuration in the first place: if
// there were a second way in, the generated files would have two authors.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/superuser404notfound/Polyseat/internal/auth"
	"github.com/superuser404notfound/Polyseat/internal/config"
	"github.com/superuser404notfound/Polyseat/internal/library"
	"github.com/superuser404notfound/Polyseat/internal/seat"
	"github.com/superuser404notfound/Polyseat/internal/sunshine"
	"github.com/superuser404notfound/Polyseat/internal/version"
	"github.com/superuser404notfound/Polyseat/internal/web"
)

// Server exposes the manager over HTTP.
type Server struct {
	manager *seat.Manager
	auth    *auth.Store
	log     *slog.Logger
}

// New builds the HTTP handler.
func New(manager *seat.Manager, credentials *auth.Store, logger *slog.Logger) http.Handler {
	s := &Server{manager: manager, auth: credentials, log: logger}

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
	Warnings []string      `json:"warnings"`
	Now      time.Time     `json:"now"`
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
			Hostname:      hostname,
			Version:       version.Version,
			GPU:           s.manager.GPU().String(),
			GPUVendor:     string(s.manager.GPU().Vendor),
			Uplink:        s.manager.Uplink(),
			UplinkBridged: s.manager.UplinkBridged(),
		},
		Warnings: s.warnings(),
		Now:      time.Now(),
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

// warnings are the host level things a seat cannot fix for itself. They belong
// in the interface rather than only in a script nobody runs.
func (s *Server) warnings() []string {
	var out []string

	if s.manager.ObserverState() != "running" {
		out = append(out, "The uhid observer is not running. Gamepads can then "+
			"only be attributed to a seat by name, not structurally.")
	}

	if _, err := os.Stat("/etc/udev/rules.d/72-polyseat-hide.rules"); err != nil {
		out = append(out, "The udev rule that hides seat input devices from the "+
			"host desktop is not installed. Run host/install.sh.")
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
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		return
	}
}

func fail(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
