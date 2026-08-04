package seat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// nvidiaNodes are the character devices that have to exist on the host before a
// seat's container starts.
//
// /dev/nvidia0 is the card itself and the one that goes missing. The control
// node is listed with it because a machine where that is absent too has a
// different problem, and saying which of the two is gone is the difference
// between a useful message and "no GPU".
//
// Only card 0, because a seat gets the host's card and Polyseat does not hand
// out one card per seat. When it does, this becomes a per seat question.
var nvidiaNodes = []string{"/dev/nvidiactl", "/dev/nvidia0"}

// modprobeBin is a variable so the test can point it at a program it wrote.
var modprobeBin = "nvidia-modprobe"

// nodeGuard makes sure the card can be handed to a container that is about to
// start.
//
// The nodes are not created at boot. The driver makes them when something first
// opens the card, and on a machine that autostarts seats twelve seconds after
// boot that first something is the container start itself. libnvidia-container
// mirrors the host's /dev/nvidia* into the seat and mirrors what exists at the
// instant it looks, so it can lose a race it started: measured on this machine
// at boot, /dev/nvidiactl was created at 21:40:06.174 and /dev/nvidia0 two
// milliseconds later, and both seats got the first and not the second.
//
// A seat without /dev/nvidia0 is the worst kind of broken, because it looks
// fine. The container runs, the libraries are all there, nvidia-smi inside it
// says "No devices found", EGL cannot enumerate a device, Sway cannot make a
// renderer and its unit restarts until systemd gives up. Nothing in that says
// "the card is missing".
type nodeGuard struct {
	// Vendor short circuits the whole thing on AMD, where the seat uses the
	// ordinary DRI nodes that udev creates at boot and nothing has to be
	// coaxed into existence.
	Vendor Vendor

	// Nodes is what has to be there, normally nvidiaNodes.
	Nodes []string

	// Exists is os.Stat in production and a map in the test.
	Exists func(string) bool

	// Create is expected to return only once the nodes are there, which is what
	// nvidia-modprobe does: it makes them itself rather than asking the driver
	// to and returning.
	Create func(context.Context) error

	// Log goes to the seat's own log, since this happens while starting one.
	Log func(string, ...any)
}

// run is a no-op on all the ordinary occasions, which is every start after the
// first one on a machine that has been up for a while.
func (g nodeGuard) run(ctx context.Context) error {
	if g.Vendor != VendorNVIDIA {
		return nil
	}

	missing := g.missing()
	if len(missing) == 0 {
		return nil
	}

	g.Log("the card has no %s yet, creating it before the container starts",
		strings.Join(missing, " and "))

	if err := g.Create(ctx); err != nil {
		return fmt.Errorf("%s does not exist and could not be created: %w",
			strings.Join(missing, " and "), err)
	}

	// Asked again rather than trusted, because a Create that succeeds without
	// having done anything would put the seat back where it started, and the
	// symptom is one nobody would connect to this.
	if still := g.missing(); len(still) > 0 {
		return fmt.Errorf("%s still does not exist after %s ran, "+
			"a seat started now would have no GPU at all",
			strings.Join(still, " and "), modprobeBin)
	}

	return nil
}

func (g nodeGuard) missing() []string {
	var out []string

	for _, node := range g.Nodes {
		if !g.Exists(node) {
			out = append(out, node)
		}
	}

	return out
}

// nvidiaModprobe creates the device nodes.
//
// This is the tool the driver ships for exactly this job and it is installed
// setuid, which is why the daemon can call it rather than making the nodes
// itself. -c 0 is the card, and it makes the control node with it.
//
// Not -u as well, which reads like "and the unified memory nodes too" and means
// "operate on the unified memory module instead of the NVIDIA one". Measured
// here with /dev/nvidia0 deleted by hand: "-c 0 -u" exited 0 and created
// nothing, "-c 0" created it. Those nodes need no help anyway, since the driver
// makes them when the module loads, which on this machine was eight seconds
// before the daemon even started.
func nvidiaModprobe(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, modprobeBin, "-c", "0").CombinedOutput()
	if err == nil {
		return nil
	}

	// Worth naming the package: on a host with an NVIDIA card and no
	// nvidia-utils, nothing else in a seat would work either, and this is the
	// first place that notices.
	//
	// Two errors for one condition. A bare name that PATH does not resolve
	// gives ErrNotFound, an absolute path that is not there gives ErrNotExist,
	// and which of the two arrives depends only on how modprobeBin is spelled.
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s is not installed, it comes with nvidia-utils", modprobeBin)
	}

	if trimmed := bytes.TrimSpace(out); len(trimmed) > 0 {
		return fmt.Errorf("%s: %w: %s", modprobeBin, err, trimmed)
	}

	return fmt.Errorf("%s: %w", modprobeBin, err)
}

// awaitGPU is the guard as the manager uses it.
func (m *Manager) awaitGPU(ctx context.Context, name string) error {
	return nodeGuard{
		Vendor: m.gpu.Vendor,
		Nodes:  nvidiaNodes,
		Exists: func(path string) bool {
			_, err := os.Stat(path)

			return err == nil
		},
		Create: nvidiaModprobe,
		Log:    func(f string, a ...any) { m.logf(name, f, a...) },
	}.run(ctx)
}
