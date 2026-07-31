package seat

import (
	"context"
	"os"
	"path/filepath"
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
	if m.cfg.Uplink != "" {
		return m.cfg.Uplink
	}

	name, err := config.DefaultUplink()
	if err != nil {
		return ""
	}

	return name
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
func isBridge(iface string) bool {
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

// Uplink and UplinkBridged are what the interface shows about the machine's
// side of the network, so that a per seat setting which only works on a bridge
// can say so instead of quietly doing nothing.
func (m *Manager) Uplink() string { return m.uplink() }

// UplinkBridged reports whether that interface is a bridge.
func (m *Manager) UplinkBridged() bool { return isBridge(m.uplink()) }

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
	if isBridge(uplink) && !isolated {
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

	if !isBridge(uplink) {
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
