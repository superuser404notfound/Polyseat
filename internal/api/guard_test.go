package api

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// unclaimed is a store nobody has chosen a password for, which is the one
// state in which /api/setup does anything.
func unclaimed(t *testing.T) *auth.Store {
	t.Helper()

	store, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	return store
}

// The setup endpoint needs no cookie, so SameSite did nothing for it: any page
// somebody on the network opened could post a form there and choose the
// password for a machine nobody had claimed yet.
func TestAnotherSiteCannotClaimTheMachine(t *testing.T) {
	const claim = `{"username":"mallory","password":"chosen elsewhere","confirm":"chosen elsewhere"}`

	cases := []struct {
		why     string
		headers map[string]string
		want    int
	}{
		{
			"a script on another site, in a browser that says so",
			map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"},
			http.StatusForbidden,
		},
		{
			"another site on the same domain, one of the seats' own pages for instance",
			map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "same-site"},
			http.StatusForbidden,
		},
		{
			"a browser too old for Sec-Fetch-Site, which still sends Origin",
			map[string]string{"Content-Type": "application/json", "Origin": "https://elsewhere.example"},
			http.StatusForbidden,
		},
		{
			// The form a page can send anywhere without asking, from a
			// browser that sends neither header. Only the type stops it.
			"a plain form with the JSON typed into it",
			map[string]string{"Content-Type": "text/plain"},
			http.StatusUnsupportedMediaType,
		},
	}

	for _, c := range cases {
		store := unclaimed(t)
		handler := setupHandler(t, store)

		r := httptest.NewRequest("POST", "https://polyseat.local:8443/api/setup", strings.NewReader(claim))
		r.RemoteAddr = "10.0.3.1:40000"

		for k, v := range c.headers {
			r.Header.Set(k, v)
		}

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)

		if w.Code != c.want {
			t.Errorf("%s: answered %d, want %d: %s", c.why, w.Code, c.want, w.Body)
		}

		if !store.NeedsSetup() {
			t.Errorf("%s: claimed the machine", c.why)
		}

		// Refused in the page's own format, or the page shows a syntax error
		// instead of the reason.
		if !strings.Contains(w.Body.String(), `"error"`) {
			t.Errorf("%s: the refusal is not JSON: %s", c.why, w.Body)
		}
	}
}

// What the page sends has to keep working: JSON with a body, and nothing at all
// for a button that has no fields.
func TestThePageItselfStillGetsThrough(t *testing.T) {
	store := unclaimed(t)
	handler := setupHandler(t, store)

	w := send(handler, "/api/setup",
		`{"username":"vincent","password":"the right one","confirm":"the right one"}`, "10.0.3.2", nil)
	if w.Code != http.StatusOK || store.NeedsSetup() {
		t.Fatalf("the page could not claim the machine: %d %s", w.Code, w.Body)
	}

	// Origin matching the host, from a browser without Sec-Fetch-Site.
	r := httptest.NewRequest("POST", "https://polyseat.local:8443/api/logout", nil)
	r.Header.Set("Origin", "https://polyseat.local:8443")

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("a bodyless POST from the page's own origin answered %d: %s", w.Code, w.Body)
	}
}

// A missing Content-Type is allowed only on a request that carries nothing,
// or it would be the way around the type check.
func TestABodyNeedsAType(t *testing.T) {
	handler := setupHandler(t, unclaimed(t))

	r := httptest.NewRequest("POST", "/api/setup",
		strings.NewReader(`{"username":"x","password":"long enough","confirm":"long enough"}`))

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("a body without a Content-Type answered %d, want 415: %s", w.Code, w.Body)
	}
}

// Nothing bounded a body before, including on the two endpoints a stranger can
// reach.
func TestBodiesAreBounded(t *testing.T) {
	handler := setupHandler(t, claimed(t))

	huge := `{"username":"vincent","password":"` + strings.Repeat("a", maxAuthBody) + `"}`

	w := send(handler, "/api/login", huge, "10.0.3.3", nil)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a login of %d bytes answered %d, want 413", len(huge), w.Code)
	}

	// The general bound, on a route that is not one of the three.
	var reached bool

	guarded := requests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))

	r := httptest.NewRequest("POST", "/api/seats", strings.NewReader(`{"label":"`+strings.Repeat("a", maxBody)+`"}`))
	r.Header.Set("Content-Type", "application/json")

	w = httptest.NewRecorder()
	guarded.ServeHTTP(w, r)

	if w.Code != http.StatusRequestEntityTooLarge || reached {
		t.Errorf("a seat of more than %d bytes answered %d and reached the handler: %v", maxBody, w.Code, reached)
	}
}

// The upload route is the exception to both the type and the size, and only
// in the one shape the page sends it in.
func TestUploadsAreMultipartAndUnbounded(t *testing.T) {
	var read int64

	guarded := requests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		read, _ = io.Copy(io.Discard, r.Body)
	}))

	r := httptest.NewRequest("POST", "/api/seats/living-room/files", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	guarded.ServeHTTP(w, r)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("JSON posted to the upload route answered %d, want 415", w.Code)
	}

	big := strings.Repeat("x", 4*maxBody)

	r = httptest.NewRequest("POST", "/api/seats/living-room/files", strings.NewReader(big))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=xyz")

	w = httptest.NewRecorder()
	guarded.ServeHTTP(w, r)

	if read != int64(len(big)) {
		t.Errorf("an upload reached the handler with %d of its %d bytes", read, len(big))
	}
}

// A client that announces a body and never sends it held a connection and a
// goroutine for as long as it liked.
func TestAStalledBodyIsCutOff(t *testing.T) {
	old := bodyTimeout
	bodyTimeout = 200 * time.Millisecond

	t.Cleanup(func() { bodyTimeout = old })

	var reached atomic.Bool

	server := httptest.NewServer(requests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
	})))
	defer server.Close()

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	_, _ = io.WriteString(conn, "POST /api/login HTTP/1.1\r\nHost: x\r\n"+
		"Content-Type: application/json\r\nContent-Length: 100\r\n\r\n{")

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	start := time.Now()

	answer, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no answer to a stalled body after %v: %v", time.Since(start), err)
	}

	answer.Body.Close()

	if time.Since(start) > 2*time.Second {
		t.Errorf("a stalled body held the connection for %v", time.Since(start))
	}

	if answer.StatusCode != http.StatusBadRequest {
		t.Errorf("a stalled body was answered with %d, want 400", answer.StatusCode)
	}

	if reached.Load() {
		t.Error("a body that never arrived reached the handler")
	}
}
