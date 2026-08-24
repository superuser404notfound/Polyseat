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
	"time"

	"github.com/superuser404notfound/Polyseat/internal/auth"
	"github.com/superuser404notfound/Polyseat/internal/config"
	"github.com/superuser404notfound/Polyseat/internal/lanbridge"
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
		lanbridge:   &lanbridge.Runner{},
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

// ------------------------------------------------------------- update check

// Looking is not installing, so this needs neither web_update nor the password.
// What it does obey is the setting that governs looking, and it says so rather
// than answering "nothing newer", which is the one thing a switched-off check
// does not know.
func TestCheckingForUpdatesObeysTheCheckSetting(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	s := host(t, config.Default())
	s.updates = update.New("v0.1.0", false, logger)

	w := httptest.NewRecorder()
	s.checkUpdate(w, post("/api/update/check", "", "10.4.0.1"))

	if w.Code != http.StatusConflict {
		t.Errorf("answered %d, wanted 409", w.Code)
	}

	if !strings.Contains(w.Body.String(), "update_check") {
		t.Errorf("it did not name the setting: %s", w.Body)
	}
}

// A build that cannot name itself as a release cannot be compared with one, and
// that is a sentence rather than a silence for the same reason.
func TestCheckingForUpdatesRefusesADevelopmentBuild(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	s := host(t, config.Default())
	s.updates = update.New("dev", true, logger)

	w := httptest.NewRecorder()
	s.checkUpdate(w, post("/api/update/check", "", "10.4.1.1"))

	if w.Code != http.StatusConflict {
		t.Errorf("answered %d, wanted 409", w.Code)
	}
}

// The page reads three things out of this to decide what to draw, and all three
// are absent on a machine that has never looked.
func TestTheStateCarriesWhatTheCheckKnows(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	s := host(t, config.Default())
	s.updates = update.New("v0.1.0", true, logger)

	got := s.updaterState()

	if !got.CheckEnabled {
		t.Error("the check is on and the state says it is not")
	}

	if got.Checked != nil {
		t.Errorf("it has never asked and the state says it asked at %v", got.Checked)
	}
}

// --------------------------------------------------------------- lan bridge

// bridgeScript points the lookup at a stand-in and hands back the file it
// records its arguments in.
//
// The real script takes this machine off the network, so what is tested here is
// the handler: what it refuses, and whether the one thing it tells the script —
// which direction to go — arrives.
func bridgeScript(t *testing.T, body string) string {
	t.Helper()

	dir := t.TempDir()
	seen := filepath.Join(dir, "args")

	script := "#!/bin/sh\necho \"$*\" > " + seen + "\n" + body

	if err := os.WriteFile(filepath.Join(dir, lanbridge.Name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	old := lanbridge.Dirs
	lanbridge.Dirs = []string{dir}

	t.Cleanup(func() { lanbridge.Dirs = old })

	return seen
}

// settled waits for the run the handler started, which is a goroutine rather
// than the request: the reply is written while the script is still going.
func settled(t *testing.T, s *Server) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)

	for s.lanbridge.Running() {
		if time.Now().After(deadline) {
			t.Fatal("the run never finished")
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func TestBridgingIsRefusedWhenTheSettingSaysSo(t *testing.T) {
	bridgeScript(t, "true\n")

	cfg := config.Default()
	cfg.WebLanBridge = false

	w := httptest.NewRecorder()
	s := host(t, cfg)
	s.bridgeUplink(w, post("/api/lan-bridge", `{"password":"the right one"}`, "10.2.0.1"))

	if w.Code != http.StatusForbidden {
		t.Errorf("answered %d, wanted 403", w.Code)
	}

	if s.lanbridge.State().Running {
		t.Error("something ran anyway")
	}
}

// Asked every time, whatever update_needs_password says. Not because this
// cannot be undone, but because of where the page can be: a seat's own browser
// reaches this interface, and this is the button that would hand that seat the
// LAN it was kept off.
func TestBridgingAsksForThePasswordEvenWhenUpdatesDoNot(t *testing.T) {
	bridgeScript(t, "true\n")

	cfg := config.Default()
	cfg.UpdateNeedsPassword = false

	s := host(t, cfg)

	for i, body := range []string{`{}`, `{"password":""}`, `{"password":"the wrong one"}`, ``} {
		w := httptest.NewRecorder()
		s.bridgeUplink(w, post("/api/lan-bridge", body, "10.2.1."+string(rune('1'+i))))

		if w.Code != http.StatusUnauthorized {
			t.Errorf("%q answered %d, wanted 401", body, w.Code)
		}
	}

	if s.lanbridge.State().Running || s.lanbridge.State().Done {
		t.Error("something ran without a password")
	}
}

// The direction is the only thing the browser decides here, and it decides it
// for a script that takes the machine's address off one interface and puts it
// on another. Carrying it the wrong way round would undo the bridge somebody
// asked to build.
func TestBridgingCarriesTheDirection(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "bridging", body: `{"password":"the right one"}`, want: ""},
		{name: "undoing", body: `{"password":"the right one","undo":true}`, want: "--undo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := bridgeScript(t, "true\n")

			w := httptest.NewRecorder()
			s := host(t, config.Default())
			s.bridgeUplink(w, post("/api/lan-bridge", tc.body, "10.2.2.1"))

			if w.Code != http.StatusAccepted {
				t.Fatalf("answered %d, wanted 202: %s", w.Code, w.Body)
			}

			settled(t, s)

			args, err := os.ReadFile(seen)
			if err != nil {
				t.Fatal(err)
			}

			if got := strings.TrimSpace(string(args)); got != tc.want {
				t.Errorf("the script was given %q, wanted %q", got, tc.want)
			}
		})
	}
}

// One at a time, because two of these at once is a machine with its address on
// neither interface. A conflict rather than a server error: it is a fact about
// what this machine is doing, not a malformed request.
func TestBridgingIsRefusedWhileOneIsGoing(t *testing.T) {
	bridgeScript(t, "sleep 1\n")

	s := host(t, config.Default())

	first := httptest.NewRecorder()
	s.bridgeUplink(first, post("/api/lan-bridge", `{"password":"the right one"}`, "10.2.3.1"))

	if first.Code != http.StatusAccepted {
		t.Fatalf("the first answered %d, wanted 202: %s", first.Code, first.Body)
	}

	second := httptest.NewRecorder()
	s.bridgeUplink(second, post("/api/lan-bridge", `{"password":"the right one"}`, "10.2.3.1"))

	if second.Code != http.StatusConflict {
		t.Errorf("the second answered %d, wanted 409", second.Code)
	}

	settled(t, s)
}

// A checkout install did not place this command until the interface learned to
// run it, so an older one has the daemon and not the script. That is a sentence
// about this machine rather than a fault in the request, and it has to say
// which of the two it is.
func TestBridgingSaysWhenThereIsNothingToRun(t *testing.T) {
	old := lanbridge.Dirs
	lanbridge.Dirs = []string{t.TempDir()}

	defer func() { lanbridge.Dirs = old }()

	w := httptest.NewRecorder()
	s := host(t, config.Default())
	s.bridgeUplink(w, post("/api/lan-bridge", `{"password":"the right one"}`, "10.2.4.1"))

	if w.Code != http.StatusConflict {
		t.Errorf("answered %d, wanted 409", w.Code)
	}

	if state := s.lanbridge.State(); state.Command != "" || state.Reason == "" {
		t.Errorf("the state does not say there is nothing to run: %+v", state)
	}
}
