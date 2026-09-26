package seat

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

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

// lanReachesHost decides whether the daemon may fall back to a seat's LAN
// address when the management interface has none, so getting it wrong either
// leaves the pairing panel broken on a host where it need not be, or has the
// daemon dial a macvlan that cannot answer it.
func TestLanReachesHost(t *testing.T) {
	cases := []struct {
		name    string
		devices map[string]map[string]string
		want    bool
	}{
		{
			name: "a port on the bridge",
			devices: map[string]map[string]string{
				"eth1": {"type": "nic", "nictype": "bridged", "parent": "br0", "name": "eth1"},
			},
			want: true,
		},
		{
			name: "an isolated seat, macvlan on that same bridge",
			devices: map[string]map[string]string{
				"eth1": {"type": "nic", "nictype": "macvlan", "parent": "br0", "name": "eth1"},
			},
			want: false,
		},
		{
			name: "a host whose uplink is a plain interface",
			devices: map[string]map[string]string{
				"eth1": {"type": "nic", "nictype": "macvlan", "parent": "enp7s0", "name": "eth1"},
			},
			want: false,
		},
		{
			// Filed under another key, which findNIC exists for: the
			// interface name is what the seat sees.
			name: "the LAN device under a key of its own",
			devices: map[string]map[string]string{
				"net1": {"type": "nic", "nictype": "bridged", "parent": "br0", "name": "eth1"},
			},
			want: true,
		},
		{
			name: "no LAN interface at all",
			devices: map[string]map[string]string{
				"eth0": {"type": "nic", "network": "incusbr0"},
			},
			want: false,
		},
	}

	if lanReachesHost(nil) {
		t.Error("an instance that could not be read was taken for a reachable one")
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			instance := &api.Instance{}
			instance.ExpandedDevices = c.devices

			if got := lanReachesHost(instance); got != c.want {
				t.Fatalf("lanReachesHost = %v, want %v", got, c.want)
			}
		})
	}
}

// ManagementPath is the rule the pairing panel and the report both read, so
// the two cannot describe the same seat differently.
func TestManagementPath(t *testing.T) {
	bridged := &api.Instance{}
	bridged.ExpandedDevices = map[string]map[string]string{
		"eth1": {"type": "nic", "nictype": "bridged", "parent": "br0", "name": "eth1"},
	}

	isolated := &api.Instance{}
	isolated.ExpandedDevices = map[string]map[string]string{
		"eth1": {"type": "nic", "nictype": "macvlan", "parent": "br0", "name": "eth1"},
	}

	both := map[string][]string{"eth0": {"10.233.136.54"}, "eth1": {"10.20.30.94"}}
	lanOnly := map[string][]string{"eth1": {"10.20.30.94"}}

	cases := []struct {
		name      string
		addresses map[string][]string
		instance  *api.Instance
		iface     string
		address   string
	}{
		{
			name:      "the management interface wins whenever it has an address",
			addresses: both,
			instance:  bridged,
			iface:     mgmtDeviceName,
			address:   "10.233.136.54",
		},
		{
			// The reported seat: eth0 present and empty, eth1 a port on the
			// bridge, so the host is on the same segment and can be used.
			name:      "no address on the management interface, LAN on a bridge",
			addresses: lanOnly,
			instance:  bridged,
			iface:     lanDeviceName,
			address:   "10.20.30.94",
		},
		{
			name:      "an isolated seat has no second way in",
			addresses: lanOnly,
			instance:  isolated,
		},
		{
			// What the cheap first ask looks like: no instance, so nothing can
			// be said about the LAN interface and only eth0 counts.
			name:      "no instance to judge the LAN interface by",
			addresses: lanOnly,
		},
		{
			name:     "a seat with no address at all",
			instance: bridged,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			iface, address := ManagementPath(c.addresses, c.instance)

			if iface != c.iface || address != c.address {
				t.Fatalf("ManagementPath = %q %q, want %q %q",
					iface, address, c.iface, c.address)
			}
		})
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

// findNIC has to match on the interface name inside the container rather than
// on the key the device is filed under, because a default profile somebody else
// wrote is free to disagree about the key and a second device called eth0 is a
// container that will not start.
func TestFindNIC(t *testing.T) {
	cases := []struct {
		name    string
		devices map[string]map[string]string
		want    string
	}{
		{
			name: "the usual key",
			devices: map[string]map[string]string{
				"eth0": {"type": "nic", "network": "incusbr0"},
			},
			want: "eth0",
		},
		{
			name: "filed under another name",
			devices: map[string]map[string]string{
				"net0": {"type": "nic", "name": "eth0", "nictype": "bridged", "parent": "br0"},
			},
			want: "net0",
		},
		{
			name: "a key called eth0 that is something else entirely",
			devices: map[string]map[string]string{
				"eth0": {"type": "disk", "path": "/srv"},
			},
			want: "",
		},
		{
			name: "only the LAN interface",
			devices: map[string]map[string]string{
				"eth1": {"type": "nic", "nictype": "macvlan", "parent": "enp7s0", "name": "eth1"},
			},
			want: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key, device := findNIC(c.devices, mgmtDeviceName)

			if key != c.want {
				t.Fatalf("key %q, want %q", key, c.want)
			}

			if (device == nil) != (c.want == "") {
				t.Fatalf("device %v for key %q", device, key)
			}
		})
	}
}

// managementUsable is the judgement the two reported bugs turn on: a seat whose
// eth0 the host cannot reach is a seat that streams to Moonlight and cannot be
// paired from the page, and the interface says it is not running instead.
func TestManagementUsable(t *testing.T) {
	networks := map[string]*api.Network{
		"incusbr0": {
			Name:       "incusbr0",
			Type:       "bridge",
			Managed:    true,
			NetworkPut: api.NetworkPut{Config: map[string]string{"ipv4.address": "10.233.136.1/24"}},
		},
		"lanmacvlan": {Name: "lanmacvlan", Type: "macvlan", Managed: true},
		"somebodys":  {Name: "somebodys", Type: "bridge"},
		"addressless": {
			Name:       "addressless",
			Type:       "bridge",
			Managed:    true,
			NetworkPut: api.NetworkPut{Config: map[string]string{"ipv4.address": "none"}},
		},
		"noleases": {
			Name:    "noleases",
			Type:    "bridge",
			Managed: true,
			NetworkPut: api.NetworkPut{Config: map[string]string{
				"ipv4.address": "10.1.2.1/24",
				"ipv4.dhcp":    "false",
			}},
		},
	}

	lookup := func(name string) (*api.Network, error) { return networks[name], nil }

	cases := []struct {
		name   string
		device map[string]string
		want   bool
	}{
		{
			name:   "a managed bridge",
			device: map[string]string{"type": "nic", "network": "incusbr0"},
			want:   true,
		},
		{
			name:   "a port on a bridge the host made",
			device: map[string]string{"type": "nic", "nictype": "bridged", "parent": "br0"},
			want:   true,
		},
		{
			name:   "a macvlan, which cannot reach its own host",
			device: map[string]string{"type": "nic", "nictype": "macvlan", "parent": "enp7s0"},
			want:   false,
		},
		{
			name:   "a managed network that is a macvlan",
			device: map[string]string{"type": "nic", "network": "lanmacvlan"},
			want:   false,
		},
		{
			name:   "an unmanaged network, which a device may not name",
			device: map[string]string{"type": "nic", "network": "somebodys"},
			want:   false,
		},
		{
			name:   "a network that is not there at all",
			device: map[string]string{"type": "nic", "network": "gone"},
			want:   false,
		},
		{
			name:   "no device: the profile hands out no eth0",
			device: nil,
			want:   false,
		},
		{
			// Reported: a seat holding an eth0 that never gets a lease looks
			// from the daemon exactly like a seat with no eth0 at all, and
			// this judgement used to call it fine and leave it alone.
			name:   "a managed bridge with no address of its own",
			device: map[string]string{"type": "nic", "network": "addressless"},
			want:   false,
		},
		{
			name:   "a managed bridge that hands out no leases",
			device: map[string]string{"type": "nic", "network": "noleases"},
			want:   false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			usable, why, err := managementUsable(c.device, lookup)
			if err != nil {
				t.Fatalf("managementUsable: %v", err)
			}

			if usable != c.want {
				t.Fatalf("usable = %v, want %v (%s)", usable, c.want, why)
			}

			// The reason is what a log line carries, so an unusable device
			// without one is a line that says nothing.
			if !usable && strings.TrimSpace(why) == "" {
				t.Error("unusable and no reason given")
			}
		})
	}
}

// The two interfaces a seat waits for carry different things, and a log line
// that names one without saying what it costs sends nobody anywhere.
func TestConsequences(t *testing.T) {
	for _, iface := range []string{lanDeviceName, mgmtDeviceName} {
		lines := consequences([]string{iface})

		if len(lines) != 1 {
			t.Fatalf("%s: %d lines, want 1", iface, len(lines))
		}

		if !strings.Contains(lines[0], iface) {
			t.Errorf("%s: the line does not name the interface: %s", iface, lines[0])
		}
	}

	both := consequences([]string{lanDeviceName, mgmtDeviceName})
	if len(both) != 2 {
		t.Fatalf("both missing gave %d lines, want 2", len(both))
	}

	if len(consequences(nil)) != 1 {
		t.Error("an empty list should still say something")
	}
}

// NamedAddresses is read by a person looking at a report, so the same seat has
// to read the same way twice.
func TestNamedAddresses(t *testing.T) {
	addresses := map[string][]string{
		"eth1": {"10.20.30.94"},
		"eth0": {"10.233.136.54"},
		"eth2": nil,
	}

	const want = "eth0 10.233.136.54, eth1 10.20.30.94"

	for i := 0; i < 3; i++ {
		if got := NamedAddresses(addresses); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}

	if got := NamedAddresses(nil); got != "" {
		t.Errorf("no addresses gave %q", got)
	}
}

// fakeBridges is an Incus with networks and nothing else, for managementBridge.
type fakeBridges struct {
	mu       sync.Mutex
	networks map[string]*api.Network
	creates  int

	// racer, when set, makes the bridge appear from somewhere else just as
	// this daemon asks for it, the way a second caller would.
	racer bool
}

func (f *fakeBridges) Network(name string) (*api.Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.networks[name], nil
}

func (f *fakeBridges) CreateBridge(name, _ string) error {
	f.mu.Lock()
	f.creates++
	_, exists := f.networks[name]
	f.mu.Unlock()

	// Long enough for every other caller to have looked and found nothing,
	// which is the window the real race lives in.
	time.Sleep(20 * time.Millisecond)

	f.mu.Lock()
	defer f.mu.Unlock()

	f.networks[name] = &api.Network{Name: name, Type: "bridge", Managed: true,
		NetworkPut: api.NetworkPut{Config: map[string]string{"ipv4.address": "10.99.0.1/24"}}}

	if exists || f.racer {
		return errors.New("The network already exists")
	}

	return nil
}

// Several seats autostarting on a host without a bridge all arrange their
// management interface at once. One bridge has to come of it, and every seat
// has to end up on it.
func TestSeatsStartingTogetherMakeOneBridge(t *testing.T) {
	f := &fakeBridges{networks: map[string]*api.Network{}}

	var wg sync.WaitGroup

	errs := make(chan error, 4)

	for range 4 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			name, err := managementBridge(f)
			if err == nil && name != PolyseatBridge {
				err = fmt.Errorf("got %q", name)
			}

			errs <- err
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("a seat got no bridge: %v", err)
		}
	}

	if f.creates != 1 {
		t.Errorf("the bridge was made %d times, want once", f.creates)
	}
}

// And a bridge made by somebody else between the look and the create is the
// bridge that was wanted, not a failure.
func TestABridgeSomebodyElseJustMadeIsTaken(t *testing.T) {
	f := &fakeBridges{networks: map[string]*api.Network{}, racer: true}

	name, err := managementBridge(f)
	if err != nil {
		t.Fatalf("the bridge that appeared was not taken: %v", err)
	}

	if name != PolyseatBridge {
		t.Errorf("got %q, want %s", name, PolyseatBridge)
	}
}
