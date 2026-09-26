package api

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/superuser404notfound/Polyseat/internal/auth"
	"github.com/superuser404notfound/Polyseat/internal/config"
	"github.com/superuser404notfound/Polyseat/internal/prepare"
	"github.com/superuser404notfound/Polyseat/internal/update"
)

// The setup mode handler is the one these tests drive, because it needs no
// Incus and still carries every route that takes a password.

// loggedHandler is setupHandler with the log kept, for the tests that care what
// the daemon writes down.
func loggedHandler(t *testing.T, store *auth.Store) (http.Handler, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&buf, nil))

	return NewSetup(config.Default(), store,
		update.New("v0.0.0", false, logger), &prepare.Runner{},
		errors.New("connect to Incus: no such file or directory"), logger), &buf
}

// send posts what the page sends: JSON, from the page's own origin.
func send(handler http.Handler, path, body, from string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.RemoteAddr = from + ":40000"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "same-origin")

	if cookie != nil {
		r.AddCookie(cookie)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	return w
}

func sessionCookie(store *auth.Store) *http.Cookie {
	return &http.Cookie{Name: auth.CookieName, Value: store.Issue()}
}

// The field people most often type a password into by mistake is the user
// name, and a failed login used to write that field to the journal as it was.
func TestAFailedLoginDoesNotLogWhatWasTyped(t *testing.T) {
	store := claimed(t)
	handler, log := loggedHandler(t, store)

	w := send(handler, "/api/login", `{"username":"hunter2-typed-here","password":"x"}`, "10.0.2.1", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong login answered %d: %s", w.Code, w.Body)
	}

	if strings.Contains(log.String(), "hunter2-typed-here") {
		t.Errorf("the journal carries what was typed into the name field:\n%s", log)
	}

	if !strings.Contains(log.String(), "failed login") {
		t.Errorf("the failed login was not logged at all:\n%s", log)
	}
}

// Changing the password asks for the current one, and that question was the
// one place a password could be guessed at full speed: a borrowed browser has
// a session and not the password, which is exactly who it is asked of.
func TestChangingThePasswordIsRateLimited(t *testing.T) {
	store := claimed(t)
	handler, _ := loggedHandler(t, store)
	cookie := sessionCookie(store)

	limited := false

	for range 20 {
		w := send(handler, "/api/password",
			`{"current":"wrong","new":"a new password","confirm":"a new password"}`, "10.0.2.2", cookie)

		if w.Code == http.StatusTooManyRequests {
			limited = true

			break
		}
	}

	if !limited {
		t.Error("twenty wrong current passwords from one address were never slowed down")
	}

	// And the right one still works from elsewhere, and clears its own record.
	w := send(handler, "/api/password",
		`{"current":"the right one","new":"a new password","confirm":"a new password"}`, "10.0.2.3", cookie)
	if w.Code != http.StatusOK {
		t.Errorf("the right current password from a fresh address answered %d: %s", w.Code, w.Body)
	}
}
