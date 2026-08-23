package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/superuser404notfound/Polyseat/internal/auth"
	"github.com/superuser404notfound/Polyseat/internal/config"
	"github.com/superuser404notfound/Polyseat/internal/prepare"
	"github.com/superuser404notfound/Polyseat/internal/uninstall"
	"github.com/superuser404notfound/Polyseat/internal/update"
)

// host builds the server the two host handlers need, which is a server without
// a manager: the same shape the setup interface has. That is not a shortcut for
// the test, it is the case worth testing, because both handlers are registered
// in that mode as well and both reach for the configuration.
func host(t *testing.T, cfg config.Config) *Server {
	t.Helper()

	return &Server{
		auth:        claimed(t),
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		setupConfig: cfg,
		prepare:     &prepare.Runner{},
	}
}

// post is a request from a source of its own. The address matters: the rate
// limiter counts per source, and the tests here deliberately get the password
// wrong, which would otherwise lock out the tests that come after them.
func post(path, body, from string) *http.Request {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.RemoteAddr = from + ":40000"

	return r
}

// catchRemoval replaces the thing that hands the job to systemd, and reports
// what it was told to do.
func catchRemoval(t *testing.T) *uninstall.Options {
	t.Helper()

	var seen uninstall.Options

	old := startRemoval
	startRemoval = func(opts uninstall.Options) error {
		seen = opts

		return nil
	}

	t.Cleanup(func() { startRemoval = old })

	return &seen
}

// installed points both lookups at a directory holding a stand-in, so that the
// handlers get past "there is nothing here to run".
func installed(t *testing.T) {
	t.Helper()

	dir := t.TempDir()

	for _, name := range []string{prepare.Name, uninstall.Name} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\ntrue\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	prepareOld, uninstallOld := prepare.Dirs, uninstall.Dirs
	prepare.Dirs, uninstall.Dirs = []string{dir}, []string{dir}

	t.Cleanup(func() { prepare.Dirs, uninstall.Dirs = prepareOld, uninstallOld })
}

// ------------------------------------------------------------------ removal

func TestRemovingIsRefusedWhenTheSettingSaysSo(t *testing.T) {
	installed(t)
	catchRemoval(t)

	cfg := config.Default()
	cfg.WebUninstall = false

	w := httptest.NewRecorder()
	host(t, cfg).removeHost(w, post("/api/uninstall", `{"password":"the right one"}`, "10.1.0.1"))

	if w.Code != http.StatusForbidden {
		t.Errorf("answered %d, wanted 403", w.Code)
	}
}

// The password is asked for whatever update_needs_password says, because that
// setting is about whether an update is worth a second question and this is not
// that kind of question.
func TestRemovingAsksForThePasswordEvenWhenUpdatesDoNot(t *testing.T) {
	installed(t)
	seen := catchRemoval(t)

	cfg := config.Default()
	cfg.UpdateNeedsPassword = false

	for i, body := range []string{`{}`, `{"password":""}`, `{"password":"the wrong one"}`, ``} {
		w := httptest.NewRecorder()
		host(t, cfg).removeHost(w, post("/api/uninstall", body, "10.1.1."+string(rune('1'+i))))

		if w.Code != http.StatusUnauthorized {
			t.Errorf("%q answered %d, wanted 401", body, w.Code)
		}
	}

	if seen.Seats {
		t.Error("something was scheduled without a password")
	}
}

func TestRemovingTheSeatsNeedsTheWordTypedOut(t *testing.T) {
	installed(t)
	seen := catchRemoval(t)

	w := httptest.NewRecorder()
	host(t, config.Default()).removeHost(w,
		post("/api/uninstall", `{"password":"the right one","seats":true}`, "10.1.2.1"))

	if w.Code != http.StatusBadRequest {
		t.Errorf("answered %d, wanted 400", w.Code)
	}

	if seen.Seats {
		t.Error("the seats were deleted without the word being typed")
	}
}

// The password and the flags come out of one body, which is the whole reason
// this handler reads it and hands it back rather than letting the password
// check read it first. Getting that wrong would look like a password that is
// never right, or like flags that are never set.
func TestRemovingCarriesWhatWasAskedFor(t *testing.T) {
	installed(t)
	seen := catchRemoval(t)

	w := httptest.NewRecorder()
	host(t, config.Default()).removeHost(w,
		post("/api/uninstall",
			`{"password":"the right one","seats":true,"library":true,"confirm":"remove"}`,
			"10.1.3.1"))

	if w.Code != http.StatusAccepted {
		t.Fatalf("answered %d (%s), wanted 202", w.Code, w.Body.String())
	}

	if !seen.Seats || !seen.Library {
		t.Errorf("scheduled %+v, wanted both", *seen)
	}
}

// The default is the careful one. A removal that was not asked to take the
// seats must not take them, because installing again is what brings them back
// and nothing brings them back from a delete.
func TestRemovingLeavesTheSeatsUnlessAsked(t *testing.T) {
	installed(t)
	seen := catchRemoval(t)

	w := httptest.NewRecorder()
	host(t, config.Default()).removeHost(w,
		post("/api/uninstall", `{"password":"the right one"}`, "10.1.4.1"))

	if w.Code != http.StatusAccepted {
		t.Fatalf("answered %d (%s), wanted 202", w.Code, w.Body.String())
	}

	if seen.Seats || seen.Library {
		t.Errorf("scheduled %+v, wanted neither", *seen)
	}
}

// ------------------------------------------------------------------ prepare

func TestPreparingIsRefusedWhenTheSettingSaysSo(t *testing.T) {
	installed(t)

	cfg := config.Default()
	cfg.WebUpdate = false

	w := httptest.NewRecorder()
	host(t, cfg).prepareHost(w, post("/api/prepare", `{}`, "10.2.0.1"))

	if w.Code != http.StatusForbidden {
		t.Errorf("answered %d, wanted 403", w.Code)
	}
}

func TestPreparingAsksForThePasswordWhenTheSettingSaysSo(t *testing.T) {
	installed(t)

	cfg := config.Default()
	cfg.UpdateNeedsPassword = true

	w := httptest.NewRecorder()
	host(t, cfg).prepareHost(w, post("/api/prepare", `{"password":"the wrong one"}`, "10.2.1.1"))

	if w.Code != http.StatusUnauthorized {
		t.Errorf("answered %d, wanted 401", w.Code)
	}
}

func TestPreparingRunsWithTheSettingsAsTheyCome(t *testing.T) {
	installed(t)

	w := httptest.NewRecorder()
	host(t, config.Default()).prepareHost(w, post("/api/prepare", ``, "10.2.2.1"))

	if w.Code != http.StatusAccepted {
		t.Fatalf("answered %d (%s), wanted 202", w.Code, w.Body.String())
	}
}

// A machine where the script is not installed says so rather than offering a
// button that cannot work. That is the checkout install, where the commands
// live in the checkout and not in a directory the daemon knows.
func TestTheStateSaysWhenThereIsNothingToRun(t *testing.T) {
	empty := t.TempDir()

	prepareOld, uninstallOld := prepare.Dirs, uninstall.Dirs
	prepare.Dirs, uninstall.Dirs = []string{empty}, []string{empty}

	defer func() { prepare.Dirs, uninstall.Dirs = prepareOld, uninstallOld }()

	s := host(t, config.Default())

	if got := s.prepare.State(); got.Command != "" || got.Reason == "" {
		t.Errorf("prepare reported %+v", got)
	}

	if got := s.removeState(); got.Available || got.Reason == "" {
		t.Errorf("remove reported %+v", got)
	}
}

// --------------------------------------------------------------- setup mode

// setupHandler is the interface a daemon serves when it never reached Incus.
func setupHandler(t *testing.T, store *auth.Store) http.Handler {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	return NewSetup(config.Default(), store,
		update.New("v0.0.0", false, logger), &prepare.Runner{},
		errors.New("connect to Incus: no such file or directory"), logger)
}

// What that mode has to get right is not what it serves but what it does not.
// Every seat handler reaches for a manager that does not exist there, so they
// are left unregistered rather than guarded: a 404 is a handler that was never
// wired up, and a panic is one that was.
func TestSetupModeServesWhatItCanAndNothingElse(t *testing.T) {
	installed(t)

	handler := setupHandler(t, claimed(t))

	// Before there is a session at all, because this is what the page reads to
	// decide which of the two interfaces it is looking at.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/session", nil))

	if !strings.Contains(w.Body.String(), `"ready":false`) {
		t.Errorf("the session endpoint did not say the machine is not ready: %s", w.Body)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, post("/api/login", `{"username":"vincent","password":"the right one"}`, "10.3.0.1"))

	if w.Code != http.StatusOK {
		t.Fatalf("could not sign in: %d %s", w.Code, w.Body)
	}

	cookies := w.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("signing in set no cookie")
	}

	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = "10.3.0.1:40000"
		r.AddCookie(cookies[0])
		handler.ServeHTTP(w, r)

		return w
	}

	state := get("/api/state")
	if state.Code != http.StatusOK {
		t.Fatalf("state answered %d", state.Code)
	}

	for _, want := range []string{`"ready":false`, "no such file or directory", `"prepare"`, `"remove"`} {
		if !strings.Contains(state.Body.String(), want) {
			t.Errorf("the state does not carry %s: %s", want, state.Body)
		}
	}

	// The seat half. Not there, and answering rather than crashing is the
	// point: a page that asks for them gets a 404 it can read.
	for _, path := range []string{"/api/library", "/api/events", "/api/seats/one/log"} {
		if got := get(path).Code; got != http.StatusNotFound {
			t.Errorf("%s answered %d, wanted 404", path, got)
		}
	}

	// The same files as the real interface, from the same address. The page a
	// browser gets here is the page it gets afterwards.
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<title>Polyseat</title>") {
		t.Errorf("the setup interface did not serve the page: %d", w.Code)
	}
}
