package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The page encodes every value it puts into a path, and a title from a folder
// is "folder:" and whatever the folder is called. This is the other half: that
// the router hands the handlers the name back whole, slash and all, rather than
// matching a different route or cutting it at the first reserved character.
//
// The encoded forms are what the page's apiPath produces for these names, run
// through encodeURIComponent, not written by hand from what it ought to be.
func TestEncodedTitlesReachTheirRoutesWhole(t *testing.T) {
	const name = "folder:Tom & Jerry #2/x%?"

	var gotTitle, gotSeat string

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/library/{appid}", func(w http.ResponseWriter, r *http.Request) {
		gotTitle = r.PathValue("appid")
	})
	mux.HandleFunc("POST /api/library/{appid}/offer/{seat}", func(w http.ResponseWriter, r *http.Request) {
		gotTitle, gotSeat = r.PathValue("appid"), r.PathValue("seat")
	})

	for _, c := range []struct{ method, path, seat string }{
		{"DELETE", "/api/library/folder%3ATom%20%26%20Jerry%20%232%2Fx%25%3F", ""},
		{"POST", "/api/library/folder%3ATom%20%26%20Jerry%20%232%2Fx%25%3F/offer/vince", "vince"},
	} {
		gotTitle, gotSeat = "", ""

		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))

		if w.Code != http.StatusOK || gotTitle != name || gotSeat != c.seat {
			t.Errorf("%s %s reached the handler as %q for %q (status %d)", c.method, c.path, gotTitle, gotSeat, w.Code)
		}
	}
}
