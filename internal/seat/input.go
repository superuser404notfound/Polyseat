package seat

import (
	"os"
	"path/filepath"
	"strings"
)

// InputDevice is one input device the broker has given to a seat.
//
// Two fields because the two audiences are different. Somebody looking at a
// seat wants to know a controller arrived, and "event22" does not say that;
// somebody working out why a device went to the wrong seat needs the node,
// because that is what every log line and every Incus device is named after.
type InputDevice struct {
	// Node is the kernel's name for it, eventN, and is what the attachment on
	// the container is called.
	Node string `json:"node"`

	// Name is what the device calls itself, with the seat tag taken off. Falls
	// back to the node when the device has gone, which happens: a controller
	// can be unplugged between the daemon reading the container's devices and
	// reading the name, and the honest answer then is the only name still known.
	Name string `json:"name"`
}

// describeInput reads a node's own name off the host.
//
// Read at the moment it is asked for rather than recorded when the device was
// attached. The broker owns attachment and the daemon only reports it, so
// anything cached here would be a second copy of a fact that already has an
// owner, and it would be wrong exactly when a device is swapped.
//
// The seat tag is stripped. Sunshine appends "(seatname)" to the devices it
// creates, which is how the broker attributes them in the first place, and
// repeating it on every line of a seat's own card says nothing.
func describeInput(node, seat string) InputDevice {
	device := InputDevice{Node: node, Name: node}

	if !plainNode(node) {
		return device
	}

	raw, err := os.ReadFile(filepath.Join("/sys/class/input", node, "device", "name"))
	if err != nil {
		return device
	}

	name := stripSeatTag(string(raw), seat)
	if name == "" {
		return device
	}

	device.Name = name

	return device
}

// plainNode reports whether a node name is one path element and nothing else.
//
// The node comes out of an Incus device name and is about to be joined onto a
// path. Nothing should ever put a separator in it, and if something does, this
// is not the code that should find out where it leads. A predicate rather than
// an inline condition so that a test can ask it directly: through describeInput
// it is invisible, because a traversing path fails to open and produces the
// same answer as a guarded one. That is the shape of check that survives being
// deleted.
func plainNode(node string) bool {
	return node != "" && node == filepath.Base(node) && node != "." && node != ".."
}

// stripSeatTag takes this seat's own tag out of a device name.
//
// Split out from the reading so that it can be tested against the shapes the
// tag actually appears in rather than against whatever this machine happens to
// have plugged in.
//
// Only this seat's tag, and only where it appears as its own parenthesised
// word. Another seat's tag is not this seat's to remove, and a device with the
// seat's name inside its own keeps it.
func stripSeatTag(name, seat string) string {
	tag := "(" + seat + ")"

	name = strings.TrimSpace(name)

	// The middle case first, and it is not hypothetical: the absolute half of
	// the pointer comes through as "Mouse passthrough (probe) (absolute)", and
	// cutting only the suffix would leave the tag stranded in the middle.
	name = strings.ReplaceAll(name, " "+tag+" ", " ")

	return strings.TrimSpace(strings.TrimSuffix(name, tag))
}
