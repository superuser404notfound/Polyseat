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

	// HelperDir holds the input broker, the uhid observer and fakeudev.py.
	//
	// Empty means look for them, which is the normal case: a checkout install
	// puts them under /usr/local/lib and a package puts them under /usr/lib,
	// and the daemon is the same binary either way. See HelperDirs.
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

	// UpdateCheck asks GitHub every six hours whether a newer Polyseat has been
	// published, and puts a line in the interface when there is one. It never
	// installs anything, and it sends nothing about this machine.
	//
	// On by default, and here as a switch because a daemon that talks to the
	// internet on its own should be one somebody can tell to stop. Set to false
	// and no request is made at all.
	UpdateCheck bool `json:"update_check"`

	// WebUpdate lets the interface install a newer release, restart the daemon,
	// and prepare the machine, rather than only saying what is wrong with it.
	//
	// On by default, and the switch is here because these are the things the
	// interface does that end on the host rather than inside a seat. With it
	// on, the interface password reaches root on this machine: pacman runs
	// install scripts as root and the binary it places is what systemd starts.
	// Set to false and the interface goes back to saying a version exists and
	// nothing more, which is what host/update.sh and polyseat-prepare are for.
	//
	// Preparing is under this switch rather than under one of its own because
	// it is the same sentence: the interface running pacman as root on the
	// host. It is the smaller half of it, in fact. An update replaces the
	// binary systemd starts; preparing installs the packages the daemon talks
	// to and initialises Incus, and every step of it is one that checks before
	// it changes and can be run again over itself.
	//
	// What the switch does not change, because it is what makes the feature
	// defensible at all: the browser never names what to install. It asks for
	// "the release the daemon found", and the version, the file and the address
	// all come from the daemon's own pinned view of GitHub.
	WebUpdate bool `json:"web_update"`

	// UpdateNeedsPassword asks for the interface password again before
	// installing, the way a bank asks before a transfer and not before showing
	// a balance.
	//
	// Off by default, because nothing else in this interface asks twice and a
	// session that can delete somebody's seat is already a session worth
	// protecting. On, it is worth exactly one thing and it is a real thing: a
	// page left open on an unlocked phone cannot be turned into a root
	// installation by somebody who picks it up. It is not a second factor and
	// does not pretend to be one; it is the same secret asked at the moment it
	// matters.
	UpdateNeedsPassword bool `json:"update_needs_password"`

	// WebUninstall lets the interface remove Polyseat from this machine.
	//
	// Its own switch rather than WebUpdate's, because it is the one action in
	// the whole interface that cannot be undone by pressing the button again.
	// Everything else the page does is reversible by doing it differently:
	// a seat can be provisioned twice, an update can be followed by another
	// update. This ends with the daemon gone and, if it was asked to, the
	// containers deleted.
	//
	// On by default all the same, because a machine that can only be installed
	// from a terminal and only removed from a terminal has not been made any
	// easier to live with. What guards it instead is asked at the moment it
	// matters: the interface password, every time, whatever
	// UpdateNeedsPassword says, plus the word "remove" typed out when the
	// seats are going too.
	WebUninstall bool `json:"web_uninstall"`

	// WebLanBridge lets the interface put the uplink on a bridge, and take it
	// off again, by running polyseat-lan-bridge.
	//
	// Its own switch, because it is the only thing the page does that changes
	// what a seat can reach. Everything else here decides what a seat is given;
	// this decides whether a seat and this machine are on one network segment,
	// and docs/security.md calls that the deliberate removal of an isolation
	// property rather than a setting. A machine whose seats are not all for
	// people in the same room should be able to say no to it once, in the file,
	// rather than trusting that nobody presses it.
	//
	// On by default, because on the machine this is built for the answer is
	// yes and the alternative is a terminal. The password is asked every time
	// regardless of UpdateNeedsPassword, for a reason that is about where the
	// page can be: a seat's own browser reaches this interface over the
	// management bridge, so without that question a session opened from a seat
	// could give that seat the LAN it did not have.
	WebLanBridge bool `json:"web_lan_bridge"`

	// GPURenderNode picks the card the seats render and encode on, for example
	// /dev/dri/renderD129. Empty means the daemon finds it itself, which is the
	// right answer on a machine with one card.
	//
	// A device rather than a vendor name, because a device is the unambiguous
	// thing: on a machine with two AMD cards "amd" still does not say which,
	// and the vendor follows from the node anyway. It is here rather than in
	// the seat record because it is a fact about the machine, and because a
	// wrong guess produces a seat that streams in software, which is the one
	// failure this project keeps having to explain.
	GPURenderNode string `json:"gpu_render_node"`
}

// Default returns the configuration used when no file exists.
func Default() Config {
	return Config{
		Listen:   ":47800",
		StateDir: "/var/lib/polyseat",
		// Empty on purpose, and filled in by Load. See HelperDirs for why
		// this cannot be one path.
		HelperDir:  "",
		Uplink:     "",
		Image:      "archlinux/current",
		Python:     "/usr/bin/python3",
		LibraryDir: "/srv/polyseat/library",

		// True by default, and it works out as true for an existing
		// installation too: Load starts from these defaults and lets the file
		// overwrite what it names, so a configuration written before this
		// setting existed leaves it on rather than off.
		UpdateCheck:  true,
		WebUpdate:    true,
		WebUninstall: true,
		WebLanBridge: true,

		// Empty on purpose: the daemon looks at the machine. See GPURenderNode.
		GPURenderNode: "",
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
			cfg.HelperDir = FindHelperDir(HelperDirs)

			return cfg, nil
		}

		return cfg, err
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}

	if cfg.HelperDir == "" {
		cfg.HelperDir = FindHelperDir(HelperDirs)
	}

	return cfg, nil
}

// HelperDirs are where the input helpers may be, in the order they are tried.
//
// Two entries because there are two ways to install the same daemon, and the
// same binary has to work under both. A checkout install puts them under
// /usr/local, which is where a file placed by hand belongs; an Arch package may
// not write there at all and puts them under /usr.
//
// Local first, so that somebody testing a change from a checkout on a machine
// that also has the package gets the copy they just built. That is the same
// order a shell would use for the binary itself, which makes it the answer
// least likely to surprise anybody.
var HelperDirs = []string{"/usr/local/lib/polyseat", "/usr/lib/polyseat"}

// FindHelperDir picks the first candidate that actually holds the helpers.
//
// Tested for one of the files rather than for the directory. An uninstall
// leaves empty directories behind more often than anybody expects, and a daemon
// that picked one of those would report a broker that will not start rather
// than a helper it could not find.
//
// Falls back to the first candidate when none of them has anything, so that the
// error names the place a checkout install would have used, which is where
// whoever is reading it will look first.
func FindHelperDir(candidates []string) string {
	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "broker.py")); err == nil {
			return dir
		}
	}

	return candidates[0]
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

// Wireless answers the question both macvlan and a bridge care about.
//
// 802.11 carries one MAC address per association, so a station cannot present
// a second one for a seat: macvlan is refused by the driver, and bridging would
// need 4-address mode at both ends, which ordinary access points do not do. A
// wireless uplink is therefore not a degraded arrangement, it is none at all,
// and the only thing worth doing about it is saying so early.
//
// Here rather than beside its one caller because there are three now: the
// report, the interface's warnings, and prepare.sh asks the same two paths in
// shell. Two of those can share a function.
func Wireless(iface string) bool {
	if iface == "" || iface != filepath.Base(iface) || iface == "." || iface == ".." {
		return false
	}

	if _, err := os.Stat(filepath.Join("/sys/class/net", iface, "wireless")); err == nil {
		return true
	}

	_, err := os.Stat(filepath.Join("/sys/class/net", iface, "phy80211"))

	return err == nil
}

// Carrier reports whether there is a cable in the interface.
//
// The question that separates a wired card somebody plugged into the network
// from a wired card nothing is attached to, which matters the moment this
// starts choosing one on its own: a seat given an uplink with no cable is a
// seat that comes up, looks healthy and never gets an address.
//
// A read that fails is a no. carrier is EINVAL on an interface that is down,
// and down with nothing to say for itself is not something to hand seats to.
func Carrier(iface string) bool {
	if iface == "" || iface != filepath.Base(iface) || iface == "." || iface == ".." {
		return false
	}

	data, err := os.ReadFile(filepath.Join("/sys/class/net", iface, "carrier"))
	if err != nil {
		return false
	}

	return strings.TrimSpace(string(data)) == "1"
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
