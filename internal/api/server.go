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
	"time"

	"github.com/superuser404notfound/Polyseat/internal/config"
	"github.com/superuser404notfound/Polyseat/internal/seat"
	"github.com/superuser404notfound/Polyseat/internal/web"
)

// Server exposes the manager over HTTP.
type Server struct {
	manager *seat.Manager
	log     *slog.Logger
}

// New builds the HTTP handler.
func New(manager *seat.Manager, logger *slog.Logger) http.Handler {
	s := &Server{manager: manager, log: logger}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/state", s.getState)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("POST /api/seats", s.createSeat)
	mux.HandleFunc("PATCH /api/seats/{name}", s.updateSeat)
	mux.HandleFunc("DELETE /api/seats/{name}", s.deleteSeat)
	mux.HandleFunc("GET /api/seats/{name}/log", s.seatLog)
	mux.HandleFunc("POST /api/seats/{name}/{action}", s.seatAction)

	mux.Handle("GET /", web.Handler())

	return logging(logger, mux)
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

// -------------------------------------------------------------------- state

type stateResponse struct {
	Seats    []seat.Status `json:"seats"`
	Observer string        `json:"observer"`
	Config   config.Config `json:"config"`
	Uplinks  []string      `json:"uplinks"`
	Host     hostInfo      `json:"host"`
	Warnings []string      `json:"warnings"`
	Now      time.Time     `json:"now"`
}

type hostInfo struct {
	Hostname string `json:"hostname"`
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	seats, err := s.manager.List()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)

		return
	}

	hostname, _ := os.Hostname()

	resp := stateResponse{
		Seats:    seats,
		Observer: s.manager.ObserverState(),
		Config:   s.manager.Config(),
		Uplinks:  config.Uplinks(),
		Host:     hostInfo{Hostname: hostname},
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

	if _, err := os.Stat("/etc/udev/rules.d/70-polyseat-hide.rules"); err != nil {
		out = append(out, "The udev rule that hides seat input devices from the "+
			"host desktop is not installed. Run host/install.sh.")
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

// ------------------------------------------------------------------ helpers

func statusFor(err error) int {
	switch {
	case errors.Is(err, seat.ErrBusy):
		return http.StatusConflict
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
