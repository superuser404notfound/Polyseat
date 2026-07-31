package seat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// describeInput turns the kernel's number for a device into what the device
// calls itself, which is the only form that answers "did the controller
// arrive". Asked of the real /sys, because a fake one would only prove that the
// function reads what the test wrote.
func TestDescribeInput(t *testing.T) {
	// Any real input device on this machine. Which one does not matter; that it
	// is a real one does.
	nodes, err := filepath.Glob("/sys/class/input/event*/device/name")
	if err != nil || len(nodes) == 0 {
		t.Skip("no input devices on this machine")
	}

	raw, err := os.ReadFile(nodes[0])
	if err != nil {
		t.Skip("cannot read an input device name here")
	}

	want := strings.TrimSpace(string(raw))
	if want == "" {
		t.Skip("the first input device has no name")
	}

	node := filepath.Base(filepath.Dir(filepath.Dir(nodes[0])))

	got := describeInput(node, "someseat")

	if got.Node != node {
		t.Errorf("node is %q, want %q", got.Node, node)
	}

	if got.Name != want {
		t.Errorf("name is %q, want %q", got.Name, want)
	}

	// A device that is not there. The node is all that is left to say and
	// saying it beats an empty row.
	if got := describeInput("event999999", "someseat"); got.Name != "event999999" {
		t.Errorf("a device that has gone reported %q rather than its node", got.Name)
	}

	// The node is joined onto a path, and it comes out of an Incus device name.
	// Asked of the guard directly: through describeInput a traversing path just
	// fails to open, so the result is the same whether the guard is there or
	// not, and a check that cannot fail is not a check.
	for _, bad := range []string{"", "../../../etc", ".", "..", "a/b", "/etc"} {
		if plainNode(bad) {
			t.Errorf("%q was accepted as a device node", bad)
		}

		if got := describeInput(bad, "someseat"); got.Name != bad {
			t.Errorf("%q was followed somewhere and produced %q", bad, got.Name)
		}
	}

	if !plainNode("event7") {
		t.Error("an ordinary node name was refused")
	}
}

// TestDescribeInputStripsTheSeatTag. Sunshine appends the seat name to the
// devices it creates, which is how the broker attributes them at all. Repeating
// it on every line of that seat's own card says nothing, and the tag can sit in
// the middle rather than at the end.
func TestDescribeInputStripsTheSeatTag(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"Mouse passthrough (probe)", "Mouse passthrough"},
		{"Mouse passthrough (probe) (absolute)", "Mouse passthrough (absolute)"},
		{"polyseat:pointer (probe)", "polyseat:pointer"},

		// Another seat's tag is not this seat's to remove, and a device that
		// genuinely has the word in its own name keeps it.
		{"Keyboard passthrough (teste)", "Keyboard passthrough (teste)"},
		{"probe controller", "probe controller"},

		// Its own word, or not at all. Cut wherever it appears and a name that
		// happens to contain the string loses a piece out of its middle.
		{"XY(probe)Z", "XY(probe)Z"},

		// Nothing left after stripping, so the node is the only name there is.
		{"(probe)", ""},
	}

	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			got := stripSeatTag(c.raw, "probe")

			if got != c.want {
				t.Errorf("%q became %q, want %q", c.raw, got, c.want)
			}
		})
	}
}
