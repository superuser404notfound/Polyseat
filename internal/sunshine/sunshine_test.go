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

// Sunshine changed this route: it now tracks several pairing requests at once
// and refuses a POST that does not name one. The tests below hold both shapes,
// because both are in the field - a seat built before the change runs the old
// one and would break just as loudly if this only spoke the new.

// newSeat answers GET /api/pin with a list, the way a newer Sunshine does, and
// records what the POST carried.
func newSeat(t *testing.T, pairings []map[string]string, got *map[string]string) *Client {
	t.Helper()

	return seat(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"pairings": pairings})

			return
		}

		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)

		if got != nil {
			*got = body
		}

		// The real one refuses without a valid id, and so does this: a test
		// that accepted anything would agree with the bug it exists to catch.
		if len(body["pairing_id"]) != 32 {
			w.WriteHeader(http.StatusBadRequest)

			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"status": true})
	})
}

const anID = "0123456789abcdef0123456789abcdef"

func TestPairNamesTheWaitingDeviceOnANewerSunshine(t *testing.T) {
	var got map[string]string

	c := newSeat(t, []map[string]string{{"id": anID, "name": "a phone"}}, &got)

	if err := c.Pair(t.Context(), "1234", "a client"); err != nil {
		t.Fatalf("did not pair: %v", err)
	}

	if got["pairing_id"] != anID {
		t.Fatalf("posted pairing_id %q, wanted %q", got["pairing_id"], anID)
	}

	if got["pin"] != "1234" || got["name"] != "a client" {
		t.Fatalf("the PIN or the name did not survive: %v", got)
	}
}

func TestPairSaysSoWhenNobodyIsWaiting(t *testing.T) {
	var got map[string]string

	c := newSeat(t, []map[string]string{}, &got)

	err := c.Pair(t.Context(), "1234", "a client")
	if err == nil {
		t.Fatal("paired with nothing waiting")
	}

	if !strings.Contains(err.Error(), "no device is waiting") {
		t.Fatalf("unhelpful message: %v", err)
	}

	if got != nil {
		t.Fatal("posted a PIN anyway")
	}
}

// The one that matters most. A wrong PIN does not merely fail: nvhttp::pin()
// erases the pairing it was aimed at. So a Pair that guessed, or tried each
// waiting device in turn, would destroy the requests it guessed wrong about.
// It has to refuse instead.
func TestPairRefusesToGuessBetweenWaitingDevices(t *testing.T) {
	var got map[string]string

	c := newSeat(t, []map[string]string{
		{"id": anID, "name": "a phone", "address": "10.0.0.2"},
		{"id": "ffffffffffffffffffffffffffffffff", "name": "a tv"},
	}, &got)

	err := c.Pair(t.Context(), "1234", "a client")
	if err == nil {
		t.Fatal("picked one of two waiting devices")
	}

	if got != nil {
		t.Fatal("posted a PIN that could have destroyed the wrong pairing")
	}

	for _, want := range []string{"a phone", "10.0.0.2", "a tv"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the message does not name %q: %v", want, err)
		}
	}
}

// An older Sunshine has no GET on this route at all.
func TestPairFallsBackWhenTheRouteIsNotThere(t *testing.T) {
	var got map[string]string

	c := seat(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true})
	})

	if err := c.Pair(t.Context(), "1234", "a client"); err != nil {
		t.Fatalf("did not pair against an older Sunshine: %v", err)
	}

	if _, named := got["pairing_id"]; named {
		t.Fatal("sent a pairing_id to a Sunshine that has no idea what one is")
	}
}

// And one that answers the GET with its web interface rather than a 404, which
// is the other way an older build declines a question it does not know.
func TestPairFallsBackWhenTheAnswerIsNotTheList(t *testing.T) {
	var got map[string]string

	c := seat(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte("<!doctype html><title>Sunshine</title>"))

			return
		}

		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": true})
	})

	if err := c.Pair(t.Context(), "1234", "a client"); err != nil {
		t.Fatalf("did not pair: %v", err)
	}

	if _, named := got["pairing_id"]; named {
		t.Fatal("sent a pairing_id after an answer that was not the list")
	}
}
