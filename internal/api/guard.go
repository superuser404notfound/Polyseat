package api

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

// maxBody is the most a request that is not an upload may carry.
//
// Every such body here is a handful of fields, the largest a path or a seat's
// settings. Nothing bounded them before, so any endpoint, including the two
// reachable without a session, read whatever it was sent into memory.
const maxBody = 64 << 10

// maxAuthBody is the bound for the three endpoints that hash a password. Kept
// separately and small because those are the ones a stranger can reach, and a
// password longer than this is not one anybody types.
const maxAuthBody = 4 << 10

// bodyTimeout is how long a request that is not an upload gets to deliver its
// body. A variable so a test can wait for it without waiting fifteen seconds.
//
// The server has a timeout for headers and none for bodies, because an upload
// can take as long as the files are large. Everything else is a few hundred
// bytes, and a client that announces them and never sends them held a
// connection and a goroutine for as long as it liked.
var bodyTimeout = 15 * time.Second

// authPaths are the endpoints held to maxAuthBody.
var authPaths = map[string]bool{
	"/api/login":    true,
	"/api/setup":    true,
	"/api/password": true,
}

// requests checks what a request carries before any handler reads it.
//
// Three checks, for three things the handlers used to take on trust.
//
// Where it came from. The session cookie is SameSite=Strict, and that was the
// whole defence against another site making a browser act here. It does
// nothing for /api/setup, which needs no cookie: on an unclaimed machine any
// page somebody on the network opened could post a form there and choose the
// password. CrossOriginProtection refuses a state changing request that the
// browser says came from another origin, by Sec-Fetch-Site or, from browsers
// too old to send that, by an Origin that is not this host. A request with
// neither is not from a browser and has no cookie to borrow, so it passes.
//
// What it is. A form can post text/plain, multipart or urlencoded to anywhere
// without asking; only a script on this origin can send application/json. So
// a body has to be JSON, except on the upload route, which has to be
// multipart and nothing else. A request with no Content-Type is let through
// only if it carries nothing, which is what the page sends for every button
// that has no fields.
//
// How much of it, and how fast. See maxBody and bodyTimeout. The body is read
// here, whole, and handed on from memory, so that a body that is too large or
// too slow is refused in one place with one answer rather than surfacing as a
// JSON error in whichever handler happened to be reading it.
//
// The deadline is left in place once the body is in. That looks like it would
// cut off a handler that runs for minutes, as an import does, and it does not:
// measured over HTTP/1.1 and HTTP/2 with a body read whole and in part, the
// server clears it itself before its own background read and the handler's
// context stays live. Clearing it here was tried and was worse: after a body
// that never arrived, the server reads what is left of it once the answer is
// written, and without a deadline that read waited for bytes the client was
// never going to send, so the refusal never arrived either.
func requests(next http.Handler) http.Handler {
	protect := http.NewCrossOriginProtection()

	// In the same shape as every other refusal, because the page parses what
	// comes back as JSON and would otherwise show a syntax error.
	protect.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusForbidden, errors.New("refused a request that did not come from this page"))
	}))

	return protect.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)

			return
		}

		contentType := r.Header.Get("Content-Type")

		mediaType := ""
		if contentType != "" {
			parsed, _, err := mime.ParseMediaType(contentType)
			if err != nil {
				fail(w, http.StatusUnsupportedMediaType, err)

				return
			}

			mediaType = parsed
		}

		if isUpload(r) {
			if mediaType != "multipart/form-data" {
				fail(w, http.StatusUnsupportedMediaType, errors.New("an upload has to be multipart/form-data"))

				return
			}

			next.ServeHTTP(w, r)

			return
		}

		limit := int64(maxBody)
		if authPaths[r.URL.Path] {
			limit = maxAuthBody
		}

		switch mediaType {
		case "application/json":
		case "":
			// Nothing at all may follow, so a missing header cannot carry a
			// body past the check above.
			limit = 0
		default:
			fail(w, http.StatusUnsupportedMediaType, errors.New("the body has to be JSON"))

			return
		}

		// Not every writer supports deadlines, a test's recorder for one, and a
		// request without one is no worse off than it was before this.
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(bodyTimeout))

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
		if err != nil {
			var tooLarge *http.MaxBytesError

			switch {
			case errors.As(err, &tooLarge) && limit == 0:
				fail(w, http.StatusUnsupportedMediaType, errors.New("the body has to be JSON"))
			case errors.As(err, &tooLarge):
				fail(w, http.StatusRequestEntityTooLarge, errors.New("the request is too large"))
			default:
				fail(w, http.StatusBadRequest, err)
			}

			return
		}

		r.Body = io.NopCloser(bytes.NewReader(body))

		next.ServeHTTP(w, r)
	}))
}

// isUpload is whether a request is for the one route that takes files.
//
// Matched on the path by hand because this runs before the router has chosen
// a pattern. The router has the last word anyway: a path that looks like an
// upload here and is not one there gets a 404, and one that is one there was
// held to the upload's rules here.
func isUpload(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}

	parts := strings.Split(r.URL.Path, "/")

	return len(parts) == 5 && parts[0] == "" && parts[1] == "api" && parts[2] == "seats" &&
		parts[3] != "" && parts[4] == "files"
}
