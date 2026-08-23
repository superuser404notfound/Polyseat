package sunshine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seat stands in for a Sunshine inside a container.
//
// It answers with what the real one answers, including the part that made these
// tests necessary: a refusal arrives as 200 with status false in the body, not
// as an HTTP error. A stub that returned 500 for a refusal would have agreed
// with the code before it was fixed.
func seat(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()

	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)

	c := New("placeholder", "user", "password")
	c.base = server.URL
	c.http = server.Client()

	return c
}

func TestPairRefusesAPinSunshineRefused(t *testing.T) {
	c := seat(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": false})
	})

	err := c.Pair(t.Context(), "1234", "a client")
	if err == nil {
		t.Fatal("accepted a PIN Sunshine refused")
	}
}

func TestPairAcceptsWhatSunshineAccepted(t *testing.T) {
	c := seat(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true})
	})

	if err := c.Pair(t.Context(), "1234", "a client"); err != nil {
		t.Fatalf("refused a PIN Sunshine accepted: %v", err)
	}
}

// The one the interface must never get wrong. Saying a device was removed while
// it is still paired is worse than an error, because nobody looks again.
func TestUnpairRefusesWhatSunshineDidNotRemove(t *testing.T) {
	c := seat(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": false})
	})

	if err := c.Unpair(t.Context(), "some-uuid"); err == nil {
		t.Error("reported a device removed that Sunshine did not remove")
	}
}

func TestUnpairAcceptsWhatSunshineRemoved(t *testing.T) {
	c := seat(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true})
	})

	if err := c.Unpair(t.Context(), "some-uuid"); err != nil {
		t.Errorf("refused a removal that worked: %v", err)
	}
}

func TestReloadAppsRefusesAReloadThatDidNotHappen(t *testing.T) {
	c := seat(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"apps": []map[string]any{{"name": "Steam Big Picture", "index": 3}},
			})

			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"status": false})
	})

	if err := c.ReloadApps(t.Context()); err == nil {
		t.Error("reported a reload that Sunshine did not do")
	}
}

// An empty list is not a failure. A seat with no apps has nothing to post back,
// and posting nothing is the right answer rather than an error.
func TestReloadAppsIsQuietWithNoApps(t *testing.T) {
	c := seat(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"apps": []any{}})
	})

	if err := c.ReloadApps(t.Context()); err != nil {
		t.Errorf("made an error out of a seat with no apps: %v", err)
	}
}

// index is set to 0 on the way back, because 0 is the only index certainly
// valid and Sunshine reorders the file as it pleases. Checked because it is the
// one field this function changes.
func TestReloadAppsPostsTheFirstAppBackAtIndexZero(t *testing.T) {
	var posted map[string]any

	c := seat(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"apps": []map[string]any{
					{"name": "Steam Big Picture", "index": 3, "cmd": "steam"},
					{"name": "Desktop", "index": 4},
				},
			})

			return
		}

		_ = json.NewDecoder(r.Body).Decode(&posted)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true})
	})

	if err := c.ReloadApps(t.Context()); err != nil {
		t.Fatal(err)
	}

	if posted["name"] != "Steam Big Picture" {
		t.Errorf("posted back %v, want the first app", posted["name"])
	}

	if posted["index"] != float64(0) {
		t.Errorf("index is %v, want 0", posted["index"])
	}

	// Everything else has to survive the round trip, or this stops being a
	// reload and becomes an edit.
	if posted["cmd"] != "steam" {
		t.Errorf("cmd is %v, want it unchanged", posted["cmd"])
	}
}

func TestCredentialsAreSent(t *testing.T) {
	var user, password string
	var ok bool

	c := seat(t, func(w http.ResponseWriter, r *http.Request) {
		user, password, ok = r.BasicAuth()
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true})
	})

	if err := c.Unpair(t.Context(), "uuid"); err != nil {
		t.Fatal(err)
	}

	if !ok || user != "user" || password != "password" {
		t.Errorf("basic auth was %q/%q (present: %v)", user, password, ok)
	}
}

// Wrong credentials get their own sentence, because the cure is provisioning
// the seat again and no other failure here has that cure.
func TestRejectedCredentialsSayWhatToDo(t *testing.T) {
	c := seat(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	_, err := c.Devices(t.Context())

	if err == nil {
		t.Fatal("accepted a 401")
	}

	if !strings.Contains(err.Error(), "Provision the seat") {
		t.Errorf("the error is %q, which does not say what to do", err)
	}
}

func TestDevicesReadsTheListSunshineReturns(t *testing.T) {
	c := seat(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": true,
			"named_certs": []map[string]any{
				{"name": "living room", "uuid": "abc", "enabled": true},
			},
		})
	})

	devices, err := c.Devices(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(devices) != 1 || devices[0].Name != "living room" || devices[0].UUID != "abc" {
		t.Errorf("read %+v", devices)
	}
}
