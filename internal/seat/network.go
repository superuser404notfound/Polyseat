package seat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lxc/incus/v7/shared/api"

	"github.com/superuser404notfound/Polyseat/internal/config"
)

// uplink is the host interface the seats reach the LAN through: what the
// configuration says, or the one carrying the default route.
//
// Empty when neither answers, which the callers report rather than guess
// around. Shared so that provisioning and the per seat switch cannot end up
// asking different interfaces the same question.
func (m *Manager) uplink() string {
	name, _ := Uplink(m.cfg)

	return name
}

// Uplink decides which host interface the seats hang off, and says why in a
// sentence somebody can act on.
//
// One copy of the policy, because three things ask it and they used to answer
// differently: the daemon, `polyseatd -report`, and host/lan-bridge.sh, which
// read only the default route and therefore worked on the wrong card on exactly
// the machine this last case exists for. The script asks the binary now rather
// than reimplementing this in shell.
//
// The order:
//
//   - the configuration, when it names one, followed to its bridge if it has
//     since become a port of one.
//   - the interface carrying the default route, when a seat can use it.
//   - a wired card with a cable in it, when the default route is wireless.
//     Nearly every machine that reaches the network over wifi still has an
//     ethernet port, and that port is all a seat needs: what it wants from the
//     card is a segment to be a host on, not a way to the internet. Choosing it
//     is what turns "no seat on this machine can have a network" into a working
//     machine with nothing typed.
//
// It chooses only where there is one answer. Several wired cards is somebody
// else's decision about which network the seats belong on, and guessing it
// produces seats Moonlight cannot see, which is a miserable thing to debug.
// The three questions Uplink asks about the machine, as variables so that the
// choice can be tested against machines this one is not. The same reason
// prepare keeps passwdFile and idmapFiles in variables: the cases worth proving
// are a wireless default route with one wired card and with three, and this
// machine is neither.
var (
	defaultRoute = config.DefaultUplink
	isWireless   = config.Wireless
	hasCarrier   = config.Carrier
	interfaces   = config.Uplinks
)

func Uplink(cfg config.Config) (string, string) {
	if cfg.Uplink != "" {
		name := enslaved(cfg.Uplink)
		if name != cfg.Uplink {
			return name, fmt.Sprintf("%q in the configuration names %s, which is now a port of %s",
				"uplink", cfg.Uplink, name)
		}

		return name, fmt.Sprintf("%q in the configuration names it", "uplink")
	}

	route, err := defaultRoute()
	if err != nil {
		return "", "this machine has no default route and the configuration names no uplink"
	}

	if !isWireless(route) {
		return route, "it carries the default route"
	}

	wired := wiredCandidates(route)

	switch len(wired) {
	case 1:
		return wired[0], fmt.Sprintf("%s carries the default route and is wireless, which no seat can use, so the seats take %s instead",
			route, wired[0])

	case 0:
		return "", fmt.Sprintf("%s carries the default route and is wireless, which no seat can use, and no wired interface here has a cable in it",
			route)

	default:
		return "", fmt.Sprintf("%s carries the default route and is wireless, which no seat can use, and there is more than one wired card to choose from (%s). Name one with %q in the configuration",
			route, strings.Join(wired, ", "), "uplink")
	}
}

// wiredCandidates lists the interfaces a seat could actually hang off.
//
// Ports of a bridge are left out, because the bridge in front of them is the
// answer and offering both would make one machine look like two choices. That
// is not hypothetical: it is what this machine looks like the moment
// lan-bridge.sh has run on it.
func wiredCandidates(except string) []string {
	var out []string

	for _, name := range interfaces() {
		if name == except || isWireless(name) || !hasCarrier(name) {
			continue
		}

		if enslaved(name) != name {
			continue
		}

		out = append(out, name)
	}

	return out
}

// isBridge reports whether the named host interface is a Linux bridge.
//
// This one fact decides how a seat reaches the LAN, and it is read off the
// interface rather than configured, because it is not a preference: an
// interface either is a bridge or it is not, and attaching the wrong kind of
// NIC to it fails or, worse, half works.
//
// The kernel gives every bridge a bridge/ directory under its sysfs entry and
// gives it to nothing else. Asking that is exact and needs no tools; parsing
// the output of ip link would be the same answer with a fork and a format that
// is allowed to change.
func IsBridge(iface string) bool {
	if iface == "" {
		return false
	}

	// Cleaned and checked, because the name comes out of the configuration file
	// and is about to be joined onto a path. A value like ../../ would otherwise
	// ask about a directory somewhere else entirely.
	if iface != filepath.Base(iface) || iface == "." || iface == ".." {
		return false
	}

	info, err := os.Stat(filepath.Join("/sys/class/net", iface, "bridge"))

	return err == nil && info.IsDir()
}

// enslaved follows an interface to the bridge it is a port of, and gives back
// the name it was handed when it is not one.
//
// Only a configured uplink needs this, and it needs it because bridging changes
// which name is right without changing the configuration that named it. On a
// machine that takes its uplink from the default route the follow-through is
// free: the route moves onto the bridge and the next question gets the new
// answer. On one that says "uplink": "enp4s0", making enp4s0 a port of br0
// leaves a name that is neither a bridge nor a macvlan parent, and every seat
// built after it would get a NIC hanging off a bridge port.
//
// Read from the kernel rather than written back into the configuration.
// Rewriting somebody's file to keep this true would be a second copy of the
// same fact, and the sysfs link is the first.
func enslaved(iface string) string {
	// The same guard IsBridge uses, and for the same reason: this name comes
	// out of a configuration file and is about to be joined onto a path.
	if iface == "" || iface != filepath.Base(iface) || iface == "." || iface == ".." {
		return iface
	}

	target, err := os.Readlink(filepath.Join("/sys/class/net", iface, "master"))
	if err != nil {
		return iface
	}

	master := filepath.Base(target)

	// A port of something that is not a bridge is left alone. Bonds and teams
	// enslave too, and a seat hanging off one of those is somebody else's
	// arrangement rather than this one's to reinterpret.
	if !IsBridge(master) {
		return iface
	}

	return master
}

// Uplink and UplinkBridged are what the interface shows about the machine's
// side of the network, so that a per seat setting which only works on a bridge
// can say so instead of quietly doing nothing.
func (m *Manager) Uplink() string { return m.uplink() }

// UplinkBridged reports whether that interface is a bridge.
func (m *Manager) UplinkBridged() bool { return IsBridge(m.uplink()) }

// UplinkReason is why that interface and not another, in a sentence the page
// can print. Sent alongside the name because on a machine with more than one
// answer the name alone does not say whether anybody has to do something.
func (m *Manager) UplinkReason() string {
	_, why := Uplink(m.cfg)

	return why
}

// UplinkWireless reports whether it is a wireless one, which is the arrangement
// neither a macvlan nor a bridge can be built on. Reported rather than guarded
// against here: what to do about it is a sentence, not a refusal, and the place
// for the sentence is the page.
func (m *Manager) UplinkWireless() bool { return config.Wireless(m.uplink()) }

// lanDeviceName is the seat's interface onto the LAN. eth0 is the management
// path on the Incus bridge and is not this.
const lanDeviceName = "eth1"

// lanDevice describes that interface for a given uplink and seat.
//
// One function for both the provisioning run and the checkbox, because the two
// disagreeing is a seat whose network changes depending on which of them
// touched it last.
//
// Three cases, and only the middle one is a choice:
//
//   - a plain interface: macvlan, and the seat cannot reach the host over the
//     LAN whatever anybody ticks. Not a policy, just what macvlan is.
//   - a bridge, seat not isolated: a port on that bridge. The host and the seat
//     are devices on one segment and see each other's broadcasts, which is what
//     a game's LAN discovery needs.
//   - a bridge, seat isolated: a macvlan on the bridge. Measured: it reaches the
//     LAN and the other seats and cannot reach the host, and the host cannot
//     reach it. That is the old arrangement restored for one seat while the
//     others stay on the bridge.
//
// hwaddr is carried over when the caller knows it. Incus generates a new one
// for a device it considers new, and a new MAC means a new DHCP lease: without
// this, ticking the box moves the seat to a different address, which breaks the
// origins Sunshine was told to accept and is a surprising amount of damage for
// a checkbox.
func lanDevice(uplink, hwaddr string, isolated bool) map[string]string {
	nictype := "macvlan"
	if IsBridge(uplink) && !isolated {
		nictype = "bridged"
	}

	device := map[string]string{
		"type":    "nic",
		"nictype": nictype,
		"parent":  uplink,
		"name":    lanDeviceName,
	}

	if hwaddr != "" {
		device["hwaddr"] = hwaddr
	}

	return device
}

// lanMAC reads the address a seat's LAN interface already has, from the device
// if it was pinned there and from the volatile key Incus keeps otherwise.
//
// Empty when the seat has never had one, which is the case where letting Incus
// generate it is exactly right.
func lanMAC(instance *api.Instance) string {
	if instance == nil {
		return ""
	}

	if mac := instance.Devices[lanDeviceName]["hwaddr"]; mac != "" {
		return mac
	}

	return instance.Config["volatile."+lanDeviceName+".hwaddr"]
}

// lanAddress is the provisioner's view of that address.
func (p *Provisioner) lanAddress() string {
	instance, _, err := p.Client.Instance(p.name())
	if err != nil {
		return ""
	}

	return lanMAC(instance)
}

// applyNetwork moves a seat between talking to the host and not, without a
// provisioning run.
//
// Applied at once for the same reason the shared library is: a checkbox that
// changes a stored value and nothing visible is a checkbox that looks broken.
// Incus swaps the interface on a running container, which was measured rather
// than assumed, and the seat's address survives it because the MAC is carried
// over.
func (m *Manager) applyNetwork(ctx context.Context, s Seat) error {
	status, err := m.client.Status(s.Name)
	if err != nil {
		return err
	}

	if status == "" {
		// No container yet. Provisioning builds it with the setting as it
		// stands, so there is nothing to do and nothing wrong.
		return nil
	}

	instance, _, err := m.client.Instance(s.Name)
	if err != nil {
		return err
	}

	uplink := m.uplink()

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	if _, err := m.client.Configure(ctx, s.Name, nil, map[string]map[string]string{
		lanDeviceName: lanDevice(uplink, lanMAC(instance), s.Isolated),
	}); err != nil {
		return err
	}

	if !IsBridge(uplink) {
		m.logf(s.Name, "! %s is not a bridge, so this seat cannot reach the host "+
			"over the LAN either way, see host/lan-bridge.sh", uplink)

		return nil
	}

	if s.Isolated {
		m.logf(s.Name, "this seat can no longer reach the host over the LAN, and "+
			"the host can no longer reach it")
	} else {
		m.logf(s.Name, "this seat and the host can now reach each other over the LAN")
	}

	return nil
}
