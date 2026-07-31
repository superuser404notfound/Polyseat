package seat

import (
	"os"
	"testing"

	"github.com/lxc/incus/v7/shared/api"
)

// isBridge decides whether a seat gets a macvlan or a bridge port, and getting
// it wrong produces a seat that is either invisible on the LAN or unable to see
// the host. Asked against the real /sys, because a fake one would only prove
// that the function reads what the test wrote.
func TestIsBridge(t *testing.T) {
	// incusbr0 is a bridge and it is on every machine this daemon runs on,
	// which is what makes it usable as the positive case here.
	if _, err := os.Stat("/sys/class/net/incusbr0/bridge"); err == nil {
		if !IsBridge("incusbr0") {
			t.Error("incusbr0 is a bridge and was not recognised as one")
		}
	} else {
		t.Log("no incusbr0 on this machine, skipping the positive case")
	}

	for _, name := range []string{
		"lo",
		"",
		"no-such-interface-here",
		"../../../sys/class/net/incusbr0",
		".",
		"..",
	} {
		if IsBridge(name) {
			t.Errorf("%q was taken for a bridge", name)
		}
	}
}

// lanDevice decides whether a seat can reach the machine it runs on, so each of
// the three cases is checked and each check was run once against deliberately
// broken code.
func TestLanDevice(t *testing.T) {
	// A bridge that exists on every machine this runs on, so the bridge cases
	// are asked of a real one rather than of a name.
	const bridge = "incusbr0"

	if _, err := os.Stat("/sys/class/net/" + bridge + "/bridge"); err != nil {
		t.Skip("no " + bridge + " here to use as a bridge")
	}

	cases := []struct {
		name     string
		uplink   string
		isolated bool
		nictype  string
	}{
		{
			// Not a policy: a macvlan cannot reach its own parent, so a seat on
			// a plain interface is isolated whatever anybody asked for.
			name:   "a plain interface cannot honour the request",
			uplink: "lo", isolated: false, nictype: "macvlan",
		},
		{
			name:   "a plain interface and an isolated seat agree anyway",
			uplink: "lo", isolated: true, nictype: "macvlan",
		},
		{
			name:   "a bridge and a seat that may talk to the host",
			uplink: bridge, isolated: false, nictype: "bridged",
		},
		{
			// A macvlan on the bridge rather than a port on it. Measured on this
			// machine: it reaches the LAN and the other seats and cannot reach
			// the host, which is the arrangement being restored.
			name:   "a bridge and an isolated seat",
			uplink: bridge, isolated: true, nictype: "macvlan",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			device := lanDevice(c.uplink, "", c.isolated)

			if device["nictype"] != c.nictype {
				t.Errorf("nictype is %q, want %q", device["nictype"], c.nictype)
			}

			if device["parent"] != c.uplink {
				t.Errorf("parent is %q, want %q", device["parent"], c.uplink)
			}

			if device["name"] != lanDeviceName {
				t.Errorf("name is %q, want %q", device["name"], lanDeviceName)
			}

			if _, set := device["hwaddr"]; set {
				t.Error("an address was invented for a seat that has none")
			}
		})
	}

	// Carried over rather than regenerated. Incus makes a new one for a device
	// it considers new, a new MAC means a new lease, and a new lease means the
	// checkbox moved the seat to a different address.
	if got := lanDevice(bridge, "10:66:6a:2b:2c:bf", false)["hwaddr"]; got != "10:66:6a:2b:2c:bf" {
		t.Errorf("the existing address was not carried over, got %q", got)
	}
}

// TestLanMAC. The address is looked for in both places Incus keeps it: pinned
// on the device by us, or in the volatile key it generates for itself.
func TestLanMAC(t *testing.T) {
	if got := lanMAC(nil); got != "" {
		t.Errorf("a missing instance produced the address %q", got)
	}

	instance := &api.Instance{}
	instance.Config = map[string]string{"volatile.eth1.hwaddr": "aa:bb:cc:dd:ee:01"}
	instance.Devices = map[string]map[string]string{}

	if got := lanMAC(instance); got != "aa:bb:cc:dd:ee:01" {
		t.Errorf("the volatile address was not found, got %q", got)
	}

	// Pinned wins. Once it is on the device that is the one in use, and the
	// volatile key can still hold whatever was generated before.
	instance.Devices["eth1"] = map[string]string{"hwaddr": "aa:bb:cc:dd:ee:02"}

	if got := lanMAC(instance); got != "aa:bb:cc:dd:ee:02" {
		t.Errorf("the pinned address did not win, got %q", got)
	}
}
