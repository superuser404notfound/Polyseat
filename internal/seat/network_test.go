package seat

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lxc/incus/v7/shared/api"

	"github.com/superuser404notfound/Polyseat/internal/config"
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

// Bridging a configured uplink changes which name is right without changing the
// configuration that named it, and the kernel is the one that knows. Everything
// that is not a bridge port has to come back unchanged, including the names
// that are not interface names at all.
func TestEnslavedLeavesEverythingElseAlone(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../../etc", "eth0/../wlan0", "nosuchdevice0"} {
		if got := enslaved(name); got != name {
			t.Errorf("enslaved(%q) gave %q, wanted it back unchanged", name, got)
		}
	}
}

// The case it exists for, against whatever bridge this machine happens to have
// a port on. Incus builds one, so on a machine with a seat running there is
// usually something here; on one without, there is nothing to prove it with.
func TestEnslavedFollowsAPortToItsBridge(t *testing.T) {
	bridges, err := filepath.Glob("/sys/class/net/*/brif/*")
	if err != nil || len(bridges) == 0 {
		t.Skip("no bridge with a port on this machine")
	}

	port := filepath.Base(bridges[0])
	bridge := filepath.Base(filepath.Dir(filepath.Dir(bridges[0])))

	if got := enslaved(port); got != bridge {
		t.Errorf("enslaved(%q) gave %q, wanted the bridge %q", port, got, bridge)
	}

	// And the bridge itself is not a port of anything, so it comes back as it
	// is. Without this the function could be "always return the first bridge"
	// and still pass the line above.
	if got := enslaved(bridge); got != bridge {
		t.Errorf("enslaved(%q) gave %q, wanted it back unchanged", bridge, got)
	}
}

// machine replaces the four questions Uplink asks, so that the choice can be
// proved against arrangements this machine is not.
func machine(t *testing.T, route string, wireless []string, wired map[string]bool) {
	t.Helper()

	oldRoute, oldWireless, oldCarrier, oldList, oldMaster :=
		defaultRoute, isWireless, hasCarrier, interfaces, portMaster

	t.Cleanup(func() {
		defaultRoute, isWireless, hasCarrier, interfaces, portMaster =
			oldRoute, oldWireless, oldCarrier, oldList, oldMaster
	})

	// Nothing here is a port of anything unless a test says so. Without this
	// the invented names are looked up in the real /sys, and whether these
	// pass depends on what the machine running them has been bridged onto.
	portMaster = func(name string) string { return name }

	defaultRoute = func() (string, error) {
		if route == "" {
			return "", errors.New("no default route found")
		}

		return route, nil
	}

	isWireless = func(name string) bool {
		for _, w := range wireless {
			if w == name {
				return true
			}
		}

		return false
	}

	hasCarrier = func(name string) bool { return wired[name] }

	interfaces = func() []string {
		var out []string

		for name := range wired {
			out = append(out, name)
		}

		sort.Strings(out)

		return out
	}
}

// The case nearly every machine that reaches the network over wifi is in: a
// wireless default route and an ethernet port that a seat can actually use.
// Choosing it is the difference between a machine that works with nothing typed
// and one whose interface says no seat here can have a network.
func TestUplinkTakesTheWiredCardWhenTheRouteIsWireless(t *testing.T) {
	machine(t, "wlan0", []string{"wlan0"}, map[string]bool{"wlan0": true, "enp4s0": true})

	name, why := Uplink(config.Config{})

	if name != "enp4s0" {
		t.Errorf("chose %q, wanted enp4s0", name)
	}

	for _, want := range []string{"wlan0", "wireless", "enp4s0"} {
		if !strings.Contains(why, want) {
			t.Errorf("the reason does not mention %q: %s", want, why)
		}
	}
}

// A cable is the whole test. A wired card with nothing plugged into it gives a
// seat that comes up, looks healthy and never gets an address, which is worse
// than being told there is no uplink.
func TestUplinkWillNotTakeAWiredCardWithNoCable(t *testing.T) {
	machine(t, "wlan0", []string{"wlan0"}, map[string]bool{"wlan0": true, "enp4s0": false})

	if name, why := Uplink(config.Config{}); name != "" {
		t.Errorf("chose %q with no cable in it: %s", name, why)
	}
}

// Two cards is a decision about which network the seats belong on, and it is
// not this program's to make. It says so and names them rather than guessing.
func TestUplinkRefusesToGuessBetweenTwoWiredCards(t *testing.T) {
	machine(t, "wlan0", []string{"wlan0"},
		map[string]bool{"wlan0": true, "enp4s0": true, "enp5s0": true})

	name, why := Uplink(config.Config{})

	if name != "" {
		t.Errorf("guessed %q", name)
	}

	for _, want := range []string{"enp4s0", "enp5s0", "uplink"} {
		if !strings.Contains(why, want) {
			t.Errorf("the reason does not mention %q: %s", want, why)
		}
	}
}

func TestUplinkTakesTheDefaultRouteWhenASeatCanUseIt(t *testing.T) {
	machine(t, "enp4s0", nil, map[string]bool{"enp4s0": true})

	if name, _ := Uplink(config.Config{}); name != "enp4s0" {
		t.Errorf("chose %q, wanted enp4s0", name)
	}
}

func TestUplinkPrefersTheConfiguration(t *testing.T) {
	machine(t, "wlan0", []string{"wlan0"}, map[string]bool{"wlan0": true, "enp4s0": true})

	name, why := Uplink(config.Config{Uplink: "enp5s0"})

	if name != "enp5s0" {
		t.Errorf("chose %q, wanted the configured enp5s0", name)
	}

	if !strings.Contains(why, "configuration") {
		t.Errorf("the reason does not say it was configured: %s", why)
	}
}

func TestUplinkSaysWhenThereIsNothing(t *testing.T) {
	machine(t, "", nil, nil)

	name, why := Uplink(config.Config{})

	if name != "" {
		t.Errorf("chose %q on a machine with no route at all", name)
	}

	if !strings.Contains(why, "no default route") {
		t.Errorf("the reason does not say why: %s", why)
	}
}
