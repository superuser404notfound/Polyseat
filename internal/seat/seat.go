// Package seat owns everything about a seat: what it is, how it is built, and
// what state it is in right now.
package seat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Seat is the daemon's definition of a seat. This is the part that is written
// down; everything else about a seat is observed rather than stored.
//
// The name is load bearing in three places at once: it is the Incus instance
// name, it is the value of XDG_SEAT inside the container, and through that it
// is the tag Sunshine appends to its virtual input device names, which is what
// the broker matches on. Renaming a seat is therefore not a cosmetic change,
// which is what Label exists for.
type Seat struct {
	Name  string `json:"name"`
	Label string `json:"label"`

	// Autostart brings the seat up when the daemon starts.
	Autostart bool `json:"autostart"`

	// Resolution of the virtual output. Sunshine can renegotiate this per
	// client later; this is what the session comes up with.
	Resolution string `json:"resolution"`

	// Address is a static address in CIDR form for the LAN interface, with
	// Gateway alongside it. Empty means DHCP.
	//
	// Worth having even though DHCP works: Sunshine's CSRF protection has to
	// be told which origins are allowed, and that list is generated from the
	// seat's addresses. When a lease changes, the web interface stops
	// accepting saves until the seat is provisioned again. A static address
	// makes the seat's identity on the network as fixed as its name.
	Address string `json:"address,omitempty"`
	Gateway string `json:"gateway,omitempty"`

	// Library gives the seat a share of the pooled game library: a directory
	// from the host, mounted in and registered with Steam, into which the
	// daemon clones anything installed in any other seat.
	//
	// Opt in rather than on by default, for two reasons that point the same
	// way. It is the only feature that mounts a host directory into a seat, so
	// it is the only one that widens what a seat can reach, and turning that on
	// silently for seats that already exist would be a change nobody asked for.
	// A seat left without it keeps its games entirely to itself.
	Library bool `json:"library"`

	// PointerSpeed is how much of the screen the gamepad pointer crosses in a
	// second at full deflection. Zero means the built-in default.
	//
	// Per seat rather than one setting for the machine, because it is a matter
	// of whose hand is on the stick and what they are looking at: the same
	// number that suits somebody on a television is too much for somebody on a
	// phone, and two people can be playing at once.
	//
	// A fraction of the screen and not a number of pixels, so that it keeps
	// meaning the same thing when a client connects at a different resolution.
	PointerSpeed float64 `json:"pointer_speed,omitempty"`

	// PlayerUID is the player's uid inside the container, learned during
	// provisioning and written down here.
	//
	// Written down rather than looked up when needed, because the library work
	// happens entirely on the host filesystem and therefore works on a seat
	// that is switched off. Asking the container for it would give that up and
	// make sharing a game depend on every seat being awake.
	PlayerUID int64 `json:"player_uid,omitempty"`

	// Provisioned records which provisioning generation was last applied in
	// full. A seat whose generation is behind the daemon's needs to be
	// provisioned again before it can be trusted to work.
	Provisioned int `json:"provisioned"`

	Created time.Time `json:"created"`
}

// State is where a seat is in its lifecycle. Distinct from the Incus instance
// status because a seat is more than a container: it is a container plus a
// session plus an input broker.
type State string

const (
	// StateAbsent means there is no container for this seat yet.
	StateAbsent State = "absent"
	// StateStopped means the container exists and is not running.
	StateStopped State = "stopped"
	// StateStarting covers the window between the start request and the
	// session inside actually being up.
	StateStarting State = "starting"
	// StateRunning means container, session and broker are all up.
	StateRunning State = "running"
	// StateStopping means a shutdown is in progress. Nothing may talk to the
	// container in this state, see the warning on incusx.Exec.
	StateStopping State = "stopping"
	// StateBuilding means provisioning is running.
	StateBuilding State = "building"
	// StateError means the last operation failed and said why.
	StateError State = "error"
)

// Status is a seat as the web interface sees it: the definition plus
// everything observed about it.
type Status struct {
	Seat

	State     State  `json:"state"`
	Container string `json:"container"`

	// Addresses per interface, as reported by the running container.
	Addresses map[string][]string `json:"addresses,omitempty"`

	// Session reports the user units inside the seat.
	Sway     string `json:"sway"`
	Sunshine string `json:"sunshine"`

	// Encoder is the hardware path Sunshine settled on, "nvenc" or the name of
	// a software one. The single most useful piece of information in the whole
	// interface: a seat that silently fell back to libx264 looks entirely
	// healthy until somebody tries to play.
	Encoder string `json:"encoder,omitempty"`

	// Codecs are what that encoder can offer, in the order Sunshine probes
	// them. Reporting only H.264, which this did, reads as though H.264 were
	// all a seat could do, when Sunshine offers whichever the client asks for.
	Codecs []string `json:"codecs,omitempty"`

	// Output is the size the seat's screen is running at now, which is not the
	// Resolution above. That one is what the session comes up with; this one is
	// whatever a connected client asked for, since the output is virtual and
	// simply becomes that size.
	Output string `json:"output,omitempty"`

	// Broker is the state of this seat's input broker process.
	Broker string `json:"broker"`

	// Devices currently attached to the seat by the broker.
	Devices []string `json:"devices,omitempty"`

	// Busy names the long running operation in progress, empty when idle.
	Busy string `json:"busy,omitempty"`

	// Progress is how far that operation has got, 0 to 100, or -1 when it has
	// nothing to report. Provisioning does not: it is a recipe whose steps are
	// named in the log as they run. Installing software does, because its
	// length is decided by a download from somebody else's server, and a
	// spinner with a line of text leaves you unable to tell a slow install
	// from a stuck one.
	Progress int `json:"progress"`

	// Notes are things worth telling somebody about this seat that are not
	// failures: it works, but something about it will bite later.
	Notes []string `json:"notes,omitempty"`

	// Error is the last failure, kept until something succeeds.
	Error string `json:"error,omitempty"`

	// Stale means the seat was provisioned by an older generation.
	Stale bool `json:"stale"`
}

// nameRE is what Incus accepts as an instance name, narrowed a little. Kept
// strict on purpose: the name ends up in a systemd unit name, a device name
// and a regular expression, so anything exotic would have to be escaped in
// three different places.
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}[a-z0-9]$`)

// ValidateName checks a proposed seat name.
func ValidateName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("a seat name has to be 3 to 32 characters of lowercase letters, digits and hyphens, starting with a letter")
	}

	// seat0 is what logind calls the physical seat, and Sunshine only appends
	// the seat tag to its device names when XDG_SEAT is something else. A seat
	// called seat0 would produce untagged devices and the broker would have
	// nothing to match on.
	if name == "seat0" {
		return fmt.Errorf("seat0 is the name of the physical seat and cannot be used")
	}

	return nil
}

var resolutionRE = regexp.MustCompile(`^[0-9]{3,5}x[0-9]{3,5}@[0-9]{2,3}Hz$`)

// Validate checks a whole definition.
func (s *Seat) Validate() error {
	if err := ValidateName(s.Name); err != nil {
		return err
	}

	if !resolutionRE.MatchString(s.Resolution) {
		return fmt.Errorf("resolution has to look like 1920x1080@60Hz")
	}

	// A pointer nobody can move and one that crosses the screen in a tenth of a
	// second are both useless, and the second is worse: it cannot be corrected
	// with the same stick that caused it.
	if s.PointerSpeed != 0 &&
		(s.PointerSpeed < MinPointerSpeed || s.PointerSpeed > MaxPointerSpeed) {
		return fmt.Errorf("the pointer speed has to be between %.2f and %.2f screens per second",
			MinPointerSpeed, MaxPointerSpeed)
	}

	if s.Address != "" {
		if !strings.Contains(s.Address, "/") {
			return fmt.Errorf("the address needs a prefix length, for example 10.20.30.71/24")
		}

		if s.Gateway == "" {
			return fmt.Errorf("a static address needs a gateway")
		}
	}

	return nil
}

// Store keeps seat definitions on disk, one file per seat.
type Store struct {
	dir string
}

// OpenStore prepares the state directory.
func OpenStore(stateDir string) (*Store, error) {
	dir := filepath.Join(stateDir, "seats")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	return &Store{dir: dir}, nil
}

func (s *Store) path(name string) string {
	return filepath.Join(s.dir, name+".json")
}

// List returns all seats, ordered by name so the interface does not reshuffle
// itself between requests.
func (s *Store) List() ([]Seat, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}

	var out []Seat

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}

		seat, err := s.load(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return nil, err
		}

		out = append(out, seat)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out, nil
}

// Get returns one seat.
func (s *Store) Get(name string) (Seat, error) {
	return s.load(s.path(name))
}

func (s *Store) load(path string) (Seat, error) {
	var seat Seat

	data, err := os.ReadFile(path)
	if err != nil {
		return seat, err
	}

	if err := json.Unmarshal(data, &seat); err != nil {
		return seat, fmt.Errorf("%s: %w", path, err)
	}

	return seat, nil
}

// Put writes a seat definition atomically.
func (s *Store) Put(seat Seat) error {
	data, err := json.MarshalIndent(seat, "", "  ")
	if err != nil {
		return err
	}

	tmp := s.path(seat.Name) + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}

	return os.Rename(tmp, s.path(seat.Name))
}

// Delete forgets a seat definition. Removing the container is somebody else's
// job; this only drops the record.
func (s *Store) Delete(name string) error {
	err := os.Remove(s.path(name))
	if os.IsNotExist(err) {
		return nil
	}

	return err
}
