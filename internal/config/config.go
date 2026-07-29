// Package config holds the daemon's bootstrap configuration.
//
// The split matters. This file is the small set of things an administrator may
// legitimately want to decide before the daemon has ever run: where to listen,
// where the state lives, which interface the seats hang off. Everything about
// seats themselves is owned by the daemon and lives in the state directory, as
// set out in docs/architecture.md. Nobody is meant to hand edit that.
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// DefaultPath is where the daemon looks unless told otherwise.
const DefaultPath = "/etc/polyseat/polyseatd.json"

// Config is the bootstrap configuration.
type Config struct {
	// Listen is the address of the web interface. All interfaces by default,
	// which is only defensible because the interface speaks TLS and demands a
	// password. Set it to 127.0.0.1:47800 to keep it on this machine.
	Listen string `json:"listen"`

	// StateDir holds the seat definitions the daemon owns.
	StateDir string `json:"state_dir"`

	// HelperDir holds the input broker, the uhid observer and fakeudev.py,
	// installed there by host/install.sh.
	HelperDir string `json:"helper_dir"`

	// Uplink is the host interface the seats get their macvlan from. Empty
	// means the daemon picks the one carrying the default route.
	Uplink string `json:"uplink"`

	// Image is the Incus image seats are created from.
	Image string `json:"image"`

	// Python runs the helpers. They are Python because that is what the M2
	// spike proved out, and rewriting a working input broker was not worth
	// doing at the same time as writing the daemon around it.
	Python string `json:"python"`

	// LibraryDir holds the shared game library: one canonical copy of every
	// title plus one private, bind mounted library per seat.
	//
	// Configurable and not derived from StateDir, because this is the one
	// directory in the whole daemon whose size is measured in hundreds of
	// gigabytes and whose filesystem has to be able to share blocks. Somebody
	// with a second disk for games needs to be able to say so without moving
	// the seat definitions along with it.
	LibraryDir string `json:"library_dir"`
}

// Default returns the configuration used when no file exists.
func Default() Config {
	return Config{
		Listen:     ":47800",
		StateDir:   "/var/lib/polyseat",
		HelperDir:  "/usr/local/lib/polyseat",
		Uplink:     "",
		Image:      "archlinux/current",
		Python:     "/usr/bin/python3",
		LibraryDir: "/srv/polyseat/library",
	}
}

// Load reads the configuration, falling back to the defaults for anything the
// file leaves out. A missing file is not an error: a fresh install should come
// up and be configurable through the web interface rather than refusing to
// start.
func Load(path string) (Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}

		return cfg, err
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}

	return cfg, nil
}

// Save writes the configuration atomically.
func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}

	return os.Rename(tmp, path)
}

// DefaultUplink returns the interface carrying the default route, which is the
// one a seat's macvlan has to hang off to reach the LAN.
//
// Read from /proc/net/route rather than shelling out to ip, and deliberately
// only a suggestion: the web interface offers it, the operator confirms it.
// Guessing wrong here produces a seat that Moonlight cannot see, which is a
// confusing failure to debug.
func DefaultUplink() (string, error) {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return "", err
	}

	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		// Destination 00000000 is the default route.
		if fields[1] == "00000000" {
			return fields[0], nil
		}
	}

	return "", fmt.Errorf("no default route found")
}

// Uplinks lists the candidate interfaces for the web interface to offer.
func Uplinks() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var out []string

	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		// Incus bridges and container veths are never the uplink.
		if strings.HasPrefix(iface.Name, "veth") ||
			strings.HasPrefix(iface.Name, "incusbr") ||
			strings.HasPrefix(iface.Name, "lxdbr") {
			continue
		}

		out = append(out, iface.Name)
	}

	return out
}
