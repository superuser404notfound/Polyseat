package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// sessionOf asks the endpoint the page asks before it draws anything.
func sessionOf(t *testing.T, handler http.Handler) map[string]any {
	t.Helper()

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/session", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("the session endpoint answered %d: %s", w.Code, w.Body)
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("could not read the session: %v: %s", err, w.Body)
	}

	return body
}

// The page waiting out a restart reloads when the name changes and not before,
// so a session that carries no name at all leaves it waiting for the whole two
// minutes and then saying the daemon never went.
func TestTheSessionNamesTheProcessAnsweringIt(t *testing.T) {
	installed(t)

	handler := setupHandler(t, claimed(t))

	name, ok := sessionOf(t, handler)["instance"].(string)
	if !ok || name == "" {
		t.Fatalf("the session did not name the process: %v", sessionOf(t, handler)["instance"])
	}

	if name != instance {
		t.Errorf("the session answered %q, and this process is %q", name, instance)
	}
}

// And the same name every time, because the page compares the answer it gets
// now against the one it got before the restart. A name that moved on its own
// would reload the page at the first tick, into the daemon it was waiting for
// the end of.
func TestTheNameDoesNotChangeWhileTheProcessRuns(t *testing.T) {
	installed(t)

	handler := setupHandler(t, claimed(t))

	first := sessionOf(t, handler)["instance"]
	second := sessionOf(t, handler)["instance"]

	if first != second {
		t.Errorf("two answers from one process: %v then %v", first, second)
	}
}

// The page compares what answers later against the name in this answer, so an
// answer that carries no name leaves it with nothing to compare and waiting for
// the daemon to go quiet, which is the guess this replaced.
func TestTheRestartAnswerNamesTheProcessThatIsGoing(t *testing.T) {
	installed(t)

	scheduled := false

	old := startRestart
	startRestart = func() error {
		scheduled = true

		return nil
	}

	t.Cleanup(func() { startRestart = old })

	handler := setupHandler(t, claimed(t))

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, post("/api/login", `{"username":"vincent","password":"the right one"}`, "10.4.0.1"))

	if w.Code != http.StatusOK {
		t.Fatalf("could not sign in: %d %s", w.Code, w.Body)
	}

	cookies := w.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("signing in set no cookie")
	}

	r := post("/api/restart", "", "10.4.0.1")
	r.AddCookie(cookies[0])

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("the restart answered %d: %s", w.Code, w.Body)
	}

	if !scheduled {
		t.Error("the restart was accepted without being scheduled")
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("could not read the answer: %v: %s", err, w.Body)
	}

	// The same name the session gives out, because the page holds this one and
	// compares it against that one. Two names for one process would be a page
	// that reloads at the first tick, into the daemon it is waiting for the end
	// of.
	if body["instance"] != sessionOf(t, handler)["instance"] {
		t.Errorf("the restart answered %v and the session says %v",
			body["instance"], sessionOf(t, handler)["instance"])
	}
}

// What the page reads the name for is telling one process from the next, so
// two of them have to be two names however close together they are made. The
// process that comes back cannot be asked about the one that went, so this
// asks the only thing that can be asked here: that making a name twice makes
// two.
func TestTwoStartsCannotShareAName(t *testing.T) {
	seen := map[string]bool{}

	for range 100 {
		name := newInstance()

		if name == "" {
			t.Fatal("a process was given no name at all")
		}

		if seen[name] {
			t.Fatalf("two processes would answer with %q", name)
		}

		seen[name] = true
	}
}
