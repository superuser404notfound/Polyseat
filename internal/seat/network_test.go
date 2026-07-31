package seat

import (
	"os"
	"testing"
)

// isBridge decides whether a seat gets a macvlan or a bridge port, and getting
// it wrong produces a seat that is either invisible on the LAN or unable to see
// the host. Asked against the real /sys, because a fake one would only prove
// that the function reads what the test wrote.
func TestIsBridge(t *testing.T) {
	// incusbr0 is a bridge and it is on every machine this daemon runs on,
	// which is what makes it usable as the positive case here.
	if _, err := os.Stat("/sys/class/net/incusbr0/bridge"); err == nil {
		if !isBridge("incusbr0") {
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
		if isBridge(name) {
			t.Errorf("%q was taken for a bridge", name)
		}
	}
}
