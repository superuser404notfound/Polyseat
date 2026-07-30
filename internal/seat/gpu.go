package seat

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Vendor is the graphics stack a seat is built against.
//
// It is a property of the host rather than of a seat: every seat on one
// machine gets the same card, so the daemon reads it once at startup and hands
// it to every provisioning run. Giving each seat its own card is a different
// feature and would belong in the seat record, not here.
type Vendor string

const (
	// VendorNVIDIA is the proprietary driver, injected into the container from
	// the host by libnvidia-container. Encoding is NVENC.
	VendorNVIDIA Vendor = "nvidia"

	// VendorAMD is amdgpu on the host and Mesa inside the seat. Encoding is
	// VA-API, which on Linux is the only hardware path AMD has: AMF exists on
	// Windows only.
	VendorAMD Vendor = "amd"
)

// PCI vendor identifiers as sysfs reports them.
const (
	pciNVIDIA = "0x10de"
	pciAMD    = "0x1002"
)

// GPU is everything the seats need to know about the host's card.
type GPU struct {
	// Vendor decides the whole shape of provisioning: which packages go into
	// the seat, which container options are set, which environment the session
	// runs with and which encoder Sunshine is told to use.
	Vendor Vendor

	// RenderNode is the path both the host and the seat know it by, for
	// example /dev/dri/renderD128. Incus passes the device through under the
	// same name it has on the host, which is measured rather than assumed: a
	// seat here shows card1 and renderD128, exactly the host's names, with no
	// /dev/dri/by-path directory to fall back on. That is why this can be
	// written straight into Sunshine's adapter_name.
	RenderNode string

	// PCI is the address of the card, kept for the log line. Two cards of the
	// same vendor are otherwise indistinguishable in a message.
	PCI string

	// Driver is the kernel module bound to it, "nvidia" or "amdgpu". Read
	// rather than derived from Vendor, because it is the one field that
	// disagrees when something is wrong: an AMD card that came up on the
	// vesa or simpledrm fallback still has vendor 0x1002 and would look
	// healthy here.
	Driver string
}

// String is what the daemon log and the interface show.
func (g GPU) String() string {
	if g.Vendor == "" {
		return "no supported GPU"
	}

	return fmt.Sprintf("%s (%s, %s, %s)", g.Vendor, g.Driver, g.PCI, g.RenderNode)
}

// DetectGPU finds the card the seats will use.
//
// It walks the render nodes rather than the cards. A render node is exactly
// what a seat needs, so a device that has none cannot serve one: a server's
// management chip, a virtual KMS device and the simpledrm fallback all appear
// as cards and none of them can encode or render for anybody. Walking cards
// would let any of them be picked over the real GPU.
//
// root is the sysfs mount point, a parameter only so the tests can point it at
// a tree they built themselves. Production passes "/sys".
//
// With cards from both vendors present NVIDIA wins, because that is the path
// that has been run in anger; the message says the other one was seen, and
// config's gpu_render_node overrides the choice.
func DetectGPU(root string) (GPU, error) {
	nodes, err := filepath.Glob(filepath.Join(root, "class/drm/renderD*"))
	if err != nil {
		return GPU{}, err
	}

	// Sorted, because a glob's order is the directory's order and two boots
	// picking different cards out of the same machine is exactly the kind of
	// drift this project has been bitten by before.
	sort.Strings(nodes)

	var found []GPU

	for _, node := range nodes {
		gpu, ok := readGPU(node)
		if !ok {
			continue
		}

		found = append(found, gpu)
	}

	switch len(found) {
	case 0:
		return GPU{}, fmt.Errorf("no NVIDIA or AMD render node under %s/class/drm", root)
	case 1:
		return found[0], nil
	}

	for _, gpu := range found {
		if gpu.Vendor == VendorNVIDIA {
			return gpu, nil
		}
	}

	return found[0], nil
}

// GPUAt describes one render node by path, for the config override.
//
// The override names a device rather than a vendor because a device is the
// unambiguous thing: on a machine with two cards, "amd" still does not say
// which one, and the vendor follows from the node anyway.
func GPUAt(root, node string) (GPU, error) {
	gpu, ok := readGPU(filepath.Join(root, "class/drm", filepath.Base(node)))
	if !ok {
		return GPU{}, fmt.Errorf("%s is not an NVIDIA or AMD render node", node)
	}

	return gpu, nil
}

// readGPU reads one /sys/class/drm/renderD* entry.
func readGPU(node string) (GPU, bool) {
	vendor, err := os.ReadFile(filepath.Join(node, "device/vendor"))
	if err != nil {
		return GPU{}, false
	}

	gpu := GPU{RenderNode: "/dev/dri/" + filepath.Base(node)}

	switch strings.TrimSpace(string(vendor)) {
	case pciNVIDIA:
		gpu.Vendor = VendorNVIDIA
	case pciAMD:
		gpu.Vendor = VendorAMD
	default:
		return GPU{}, false
	}

	// Both of these are decoration for the log, so a failure to read either
	// leaves the field empty rather than discarding a card that is otherwise
	// perfectly usable.
	if target, err := os.Readlink(filepath.Join(node, "device")); err == nil {
		gpu.PCI = filepath.Base(target)
	}

	if target, err := os.Readlink(filepath.Join(node, "device/driver")); err == nil {
		gpu.Driver = filepath.Base(target)
	}

	return gpu, true
}

// stack is what one vendor needs, in one place.
//
// Gathered into a value rather than spread through the provisioning steps as
// if-else, because the steps are the thing that is easy to get half right: the
// seat that gets AMD packages but keeps NVIDIA's GBM_BACKEND starts, streams
// and encodes in software, which is the failure mode this project has spent
// the most time on. With the whole answer in one struct, a vendor is either
// described or it is not.
type stack struct {
	// config are the Incus instance keys for a seat on this vendor.
	config map[string]string

	// packages go into the seat alongside the vendor neutral set.
	packages []string

	// driverFlags tell pacman the graphics driver is already present. Only
	// NVIDIA needs them, see driverFlags below.
	driverFlags []string

	// env is the systemd drop-in the session units get. Written as a drop-in
	// rather than into the units so that the units themselves stay the same
	// file on every machine.
	env []string

	// encoder is what Sunshine is told to use. Left to Sunshine's own probing
	// it would usually land on the right one, but "usually" is how a seat ends
	// up on libx264 without anybody noticing.
	encoder string

	// adapter is written to Sunshine's adapter_name. Empty means the option is
	// left out entirely.
	adapter string
}

// stackFor returns the whole answer for one card.
func stackFor(gpu GPU) stack {
	switch gpu.Vendor {
	case VendorAMD:
		return stack{
			config: map[string]string{
				// Set to false rather than left out. Incus merges what it is
				// given, so a seat that was built on this machine while an
				// NVIDIA card was in it would otherwise keep the injection
				// switched on, and libnvidia-container would fail the seat's
				// start over a driver that is no longer there.
				"nvidia.runtime": "false",
			},

			// Mesa is already in the vendor neutral set, and since Arch folded
			// libva-mesa-driver into it (mesa 1:24.2.7-1 replaces it), that
			// one package carries radeonsi_drv_video.so, which is the VA-API
			// encoder itself. What is missing from the neutral set is Vulkan
			// for the games and the 32 bit half of both, because a good many
			// games are still 32 bit and would otherwise run on llvmpipe.
			//
			// libva-utils is here for vainfo alone: it is the one command that
			// answers "will this card encode?" before anybody tries to play.
			packages: []string{
				"vulkan-radeon", "lib32-vulkan-radeon",
				"lib32-mesa", "libva-utils",
			},

			// No pacman flags at all. On NVIDIA the driver arrives as injected
			// files that a package would collide with; on AMD the driver is a
			// package, so the virtual providers must resolve normally. Telling
			// pacman the driver is already installed here would leave the seat
			// with no driver at all.
			driverFlags: nil,

			// GBM_BACKEND and __GLX_VENDOR_LIBRARY_NAME are deliberately
			// absent: they are the NVIDIA vendor's names, and setting them on
			// Mesa points GBM at a backend that is not there.
			//
			// LIBVA_DRIVER_NAME is deliberately absent too. libva derives the
			// driver from the kernel driver name and gets radeonsi right on
			// its own; pinning it would be one more thing to be wrong on the
			// pre GCN cards that still want r600.
			env: []string{
				// Which card the compositor renders on. Redundant on a machine
				// with one card and the whole answer on a machine with two,
				// which is precisely the machine somebody testing this will
				// have.
				"WLR_RENDER_DRM_DEVICE=" + gpu.RenderNode,
			},

			// VA-API is the only hardware encoder AMD has on Linux. AMF is a
			// Windows library and Sunshine's amf option cannot be reached from
			// here at all.
			encoder: "vaapi",

			// Named explicitly because Sunshine picks the first card it finds
			// otherwise, and a machine with two would be a coin toss.
			adapter: gpu.RenderNode,
		}

	default:
		return stack{
			config: map[string]string{
				"nvidia.runtime":             "true",
				"nvidia.driver.capabilities": "all",
			},
			packages:    nil,
			driverFlags: driverFlags,
			env: []string{
				"GBM_BACKEND=nvidia-drm",
				"__GLX_VENDOR_LIBRARY_NAME=nvidia",
			},
			encoder: "nvenc",
			adapter: "",
		}
	}
}

// dropIn renders the environment as a systemd drop-in.
func (s stack) dropIn() []byte {
	var b strings.Builder

	b.WriteString("# Polyseat: the graphics stack this machine has.\n")
	b.WriteString("# Written by polyseatd, changes are overwritten.\n")
	b.WriteString("[Service]\n")

	for _, line := range s.env {
		b.WriteString("Environment=" + line + "\n")
	}

	return []byte(b.String())
}
