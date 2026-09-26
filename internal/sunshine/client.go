// Package sunshine talks to the Sunshine instance inside a seat.
//
// It exists for one feature: pairing every seat from one place. Sunshine's own
// interface can pair a device, but there is one of those per seat on a port of
// its own with a login of its own, which is exactly the juggling Polyseat set
// out to remove.
//
// Nothing here is reverse engineered guesswork. These are the same calls
// Sunshine's own web interface makes, read out of the bundle it ships, and each
// one was tried against a running seat before it was written down.
package sunshine

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// errNotFound is how a call reports a route this Sunshine does not have.
//
// Kept apart from the other failures because it means "an older build" rather
// than "something is wrong", and Polyseat has to speak to both. See Pair.
var errNotFound = errors.New("this Sunshine has no such route")

// Port is where Sunshine serves its web interface.
const Port = 47990

// How long a call may take.
//
// Per call rather than on the client, because the two numbers are not one
// number. Everything here is a question a seat on the same bridge answers at
// once, and ten seconds is already generous for that; pairing is not, since
// 2026.914.233613, and a single limit would have to be the larger of the two
// for every call.
//
// **The pairing one has to outlast Sunshine's own wait.** That release made
// POST /api/pin block until Moonlight has finished the cryptographic handshake
// or `ping_timeout` has passed, and answer with whether the device is now
// paired rather than whether the PIN was taken. ping_timeout defaults to ten
// seconds and a seat's sunshine.conf does not set it, so the old ten second
// limit on this client was the same number: a pairing that took its time raced
// us, and losing that race reads as "Sunshine could not be reached" to somebody
// who has just typed a correct PIN and whose device is about to appear in the
// list. Larger than the wait it has to survive, with room for a seat under load
// rather than a second of margin.
//
// Variables rather than constants so that a test can shrink them. Nothing else
// writes them, and a test that slept out the real ones would take a minute.
var (
	callTimeout = 10 * time.Second
	pairTimeout = 45 * time.Second
)

// Client talks to one seat's Sunshine.
type Client struct {
	base     string
	user     string
	password string
	http     *http.Client
}

// New builds a client for a seat.
//
// The address has to be the seat's management address, the one on the Incus
// bridge, not the one Moonlight connects to. Those seats hang off the LAN
// through macvlan, and a macvlan interface deliberately cannot talk to its own
// host. Using the LAN address here produces a timeout that looks like Sunshine
// being down.
func New(address, user, password string) *Client {
	return &Client{
		base:     fmt.Sprintf("https://%s:%d", address, Port),
		user:     user,
		password: password,
		http: &http.Client{
			// No Timeout here on purpose: callWithin says why.
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					// Sunshine serves a self signed certificate it generates
					// for itself, so there is nothing to verify against. What
					// makes this acceptable is the path rather than the
					// certificate: this connection runs from the host to a
					// container of its own over the Incus bridge, and anything
					// positioned to intercept it is already root on this
					// machine.
					InsecureSkipVerify: true,
				},
			},
		},
	}
}

// Device is a client paired with a seat.
type Device struct {
	Name    string `json:"name"`
	UUID    string `json:"uuid"`
	Enabled bool   `json:"enabled"`
}

type listResponse struct {
	NamedCerts []Device `json:"named_certs"`
	Status     bool     `json:"status"`
}

// Devices lists the clients paired with this seat.
func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	var out listResponse
	if err := c.call(ctx, http.MethodGet, "/api/clients/list", nil, &out); err != nil {
		return nil, err
	}

	return out.NamedCerts, nil
}

type statusResponse struct {
	Status bool `json:"status"`
}

// Pairing is a client waiting for somebody to type its PIN.
type Pairing struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
}

type pairingsResponse struct {
	// A pointer, and that is the whole trick. An older Sunshine has no GET on
	// this route and answers with something carrying no pairings key at all,
	// while a newer one with nobody waiting answers with an empty list. Those
	// two mean opposite things and a plain slice cannot tell them apart.
	Pairings *[]Pairing `json:"pairings"`
}

// PendingPairings lists the clients waiting for a PIN.
//
// The second return says whether this Sunshine understands the question. Older
// builds do not, and that is not an error: it is the answer that sends Pair
// down the other path.
func (c *Client) PendingPairings(ctx context.Context) ([]Pairing, bool, error) {
	var out pairingsResponse

	if err := c.call(ctx, http.MethodGet, "/api/pin", nil, &out); err != nil {
		// A route that is not there, or an answer that is not the JSON this
		// asked for, both mean the same thing: this build predates the change.
		// Anything else is a real failure and is passed on.
		var syntax *json.SyntaxError
		var typ *json.UnmarshalTypeError

		if errors.Is(err, errNotFound) || errors.As(err, &syntax) || errors.As(err, &typ) {
			return nil, false, nil
		}

		return nil, false, err
	}

	if out.Pairings == nil {
		return nil, false, nil
	}

	return *out.Pairings, true, nil
}

// Pair submits the PIN Moonlight is showing.
//
// Sunshine changed this route. It used to take the PIN and a name and pair
// whatever single request was outstanding; it now tracks several at once, each
// with an id, and refuses a POST without one:
//
//	if (!nvhttp::is_valid_pairing_id(pairing_id)) {
//	  bad_request(response, request, "pairing_id must contain exactly 32 hexadecimal characters");
//
// So against a newer seat the old call fails with "Sunshine answered 400 Bad
// Request" and pairing is simply broken. Both shapes are spoken here because
// both are in the field: a seat built last month runs the old one.
//
// The new shape is the better one for this project, which is worth saying
// plainly: it can name the devices that are waiting, and this file used to
// carry a comment regretting that it could not.
//
// **Never guess which pairing a PIN belongs to.** A wrong PIN is not merely
// refused, it destroys the request it was aimed at - nvhttp::pin() erases the
// session on failure - so trying a PIN against each waiting device in turn
// would take innocent ones down with it. With more than one waiting, this says
// so and pairs nothing.
func (c *Client) Pair(ctx context.Context, pin, name string) error {
	pending, versioned, err := c.PendingPairings(ctx)
	if err != nil {
		return err
	}

	body := map[string]string{"pin": pin, "name": name}

	if versioned {
		switch len(pending) {
		case 0:
			return fmt.Errorf("no device is waiting to be paired with this seat. " +
				"Moonlight has to be showing the PIN while this is entered")
		case 1:
			body["pairing_id"] = pending[0].ID
		default:
			return fmt.Errorf("%d devices are waiting to be paired with this seat, "+
				"and a PIN belongs to exactly one of them: %s. Pair them one at a "+
				"time", len(pending), describe(pending))
		}
	}

	var out statusResponse
	if err := c.callWithin(ctx, pairTimeout, http.MethodPost, "/api/pin", body, &out); err != nil {
		return err
	}

	if !out.Status {
		return fmt.Errorf("the device was not paired. The PIN has to be entered " +
			"while Moonlight is showing it and belongs to this seat only, and " +
			"Moonlight has to still be waiting: Sunshine now answers this once " +
			"the device is actually paired, so a client that gave up or a PIN " +
			"that was refused both arrive here")
	}

	return nil
}

// describe names the waiting devices for a message somebody has to act on.
func describe(pending []Pairing) string {
	names := make([]string, 0, len(pending))

	for _, p := range pending {
		switch {
		case p.Name != "" && p.Address != "":
			names = append(names, p.Name+" at "+p.Address)
		case p.Name != "":
			names = append(names, p.Name)
		case p.Address != "":
			names = append(names, p.Address)
		default:
			names = append(names, "an unnamed device")
		}
	}

	return strings.Join(names, ", ")
}

// Unpair removes one paired client.
//
// The answer is read and not only decoded. Sunshine reports a refusal in the
// body with a 200 beside it, the same way it does for a PIN, and this used to
// decode that into a value nothing looked at: the interface then said the
// device had been removed while it was still paired, which is the one thing a
// pairing screen must never say.
func (c *Client) Unpair(ctx context.Context, uuid string) error {
	var out statusResponse
	if err := c.call(ctx, http.MethodPost, "/api/clients/unpair", map[string]string{"uuid": uuid}, &out); err != nil {
		return err
	}

	if !out.Status {
		return fmt.Errorf("Sunshine did not remove that device")
	}

	return nil
}

// CloseApp ends the application Sunshine considers running, which runs its undo
// commands the way quitting from Moonlight does.
//
// A client that leaves without quitting leaves the application running so that
// it can be resumed, and a resumed application runs no prep commands: the seat
// comes back at whatever size it was left at. So once the daemon has decided a
// stream is over, it closes the application here rather than putting the seat
// back behind Sunshine's back, and the next connection is a launch that sizes
// the seat for whoever makes it.
//
// Every application in a seat is detached, so this ends no process. Steam and a
// running game carry on; only Sunshine's idea of what is running changes.
//
// An empty object rather than no body, because Sunshine checks the content type
// of every POST and a request without one is refused before it is read.
func (c *Client) CloseApp(ctx context.Context) error {
	var out statusResponse
	if err := c.call(ctx, http.MethodPost, "/api/apps/close", map[string]any{}, &out); err != nil {
		return err
	}

	if !out.Status {
		return fmt.Errorf("Sunshine did not close the application")
	}

	return nil
}

func (c *Client) call(ctx context.Context, method, path string, body, into any) error {
	return c.callWithin(ctx, callTimeout, method, path, body, into)
}

// callWithin is call with a limit of its own, for the one route that waits.
//
// The limit lives here rather than on the http.Client because a limit there
// applies to every call and cannot be lengthened for one of them: a context
// with a later deadline does not lift http.Client.Timeout, it only loses to it.
// Whatever the caller brought still counts, since WithTimeout keeps the earlier
// of the two deadlines, so a browser that went away still ends the call.
func (c *Client) callWithin(ctx context.Context, limit time.Duration, method, path string, body, into any) error {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	var reader io.Reader

	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}

		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}

	req.SetBasicAuth(c.user, c.password)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("reach Sunshine in the seat: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return fmt.Errorf("Sunshine rejected the credentials. Provision the seat " +
			"again to reset them")
	case http.StatusNotFound:
		return fmt.Errorf("%w: Sunshine answered %s", errNotFound, resp.Status)
	default:
		return fmt.Errorf("Sunshine answered %s", resp.Status)
	}

	if into == nil {
		return nil
	}

	return json.NewDecoder(resp.Body).Decode(into)
}

// appList is the shape of what Sunshine keeps in apps.json.
//
// Only the first entry is ever read out of it, and only so that it can be
// handed straight back. See ReloadApps.
type appList struct {
	Apps []json.RawMessage `json:"apps"`
}

// ReloadApps makes Sunshine pick up an apps.json that changed underneath it.
//
// It reads that file once, at startup, for the list it serves to clients. The
// web interface rereads it on every request, which is what made this look
// solved when it was not: the daemon's own checks agreed with the file while
// Moonlight went on showing what Sunshine had loaded hours earlier. A game
// uninstalled in a seat stayed in the list until the seat restarted.
//
// Writing an app through this API does make it reload, and it reloads the file
// rather than trusting what it holds: posting an entry back unchanged is
// therefore a way of saying "read that again" without altering anything and
// without restarting, which would drop whatever somebody is streaming.
//
// The first entry is the one posted back because index 0 is the only index
// that is certainly valid, and because Sunshine reorders the file as it
// pleases, so nothing else about position can be relied on.
func (c *Client) ReloadApps(ctx context.Context) error {
	var list appList

	if err := c.call(ctx, http.MethodGet, "/api/apps", nil, &list); err != nil {
		return err
	}

	if len(list.Apps) == 0 {
		return nil
	}

	var first map[string]any
	if err := json.Unmarshal(list.Apps[0], &first); err != nil {
		return err
	}

	first["index"] = 0

	var out statusResponse
	if err := c.call(ctx, http.MethodPost, "/api/apps", first, &out); err != nil {
		return err
	}

	// Same reason as Unpair. A reload that quietly did not happen leaves
	// Moonlight showing games a seat no longer has, which is the exact symptom
	// this function was written to cure, so it is worth an error rather than a
	// silence.
	if !out.Status {
		return fmt.Errorf("Sunshine did not reload its app list")
	}

	return nil
}
