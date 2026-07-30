package seat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSysfs builds the shape /sys really has, not a convenient version of it.
//
// The layout is copied from this machine rather than imagined: /sys/class/drm/
// renderD128 is a symlink into the device tree, its "device" entry is a
// relative symlink three levels up to the PCI address, and "device/driver"
// points off into /sys/bus. Getting that wrong in a fixture is how a test ends
// up proving that the code reads a directory nobody has.
func fakeSysfs(t *testing.T, cards []fakeCard) string {
	t.Helper()

	root := t.TempDir()

	for _, card := range cards {
		pci := filepath.Join(root, "devices/pci0000:00/0000:00:01.0", card.pci)

		for _, node := range card.nodes {
			dir := filepath.Join(pci, "drm", node)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}

			// ../../.. from <pci>/drm/<node> is the PCI directory itself.
			if err := os.Symlink("../../../"+card.pci, filepath.Join(dir, "device")); err != nil {
				t.Fatal(err)
			}

			link := filepath.Join(root, "class/drm", node)
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}

			target := filepath.Join("../../devices/pci0000:00/0000:00:01.0",
				card.pci, "drm", node)
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
		}

		if card.vendor != "" {
			err := os.WriteFile(filepath.Join(pci, "vendor"), []byte(card.vendor+"\n"), 0o644)
			if err != nil {
				t.Fatal(err)
			}
		}

		if card.driver != "" {
			drivers := filepath.Join(root, "bus/pci/drivers", card.driver)
			if err := os.MkdirAll(drivers, 0o755); err != nil {
				t.Fatal(err)
			}

			err := os.Symlink("../../../../bus/pci/drivers/"+card.driver,
				filepath.Join(pci, "driver"))
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	return root
}

type fakeCard struct {
	pci    string
	vendor string
	driver string
	nodes  []string
}

func amdCard(pci, card, render string) fakeCard {
	return fakeCard{pci: pci, vendor: pciAMD, driver: "amdgpu",
		nodes: []string{card, render}}
}

func nvidiaCard(pci, card, render string) fakeCard {
	return fakeCard{pci: pci, vendor: pciNVIDIA, driver: "nvidia",
		nodes: []string{card, render}}
}

func TestDetectGPUFindsAnAMDCard(t *testing.T) {
	root := fakeSysfs(t, []fakeCard{amdCard("0000:03:00.0", "card0", "renderD128")})

	gpu, err := DetectGPU(root)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}

	if gpu.Vendor != VendorAMD {
		t.Errorf("vendor is %q, want amd", gpu.Vendor)
	}

	if gpu.RenderNode != "/dev/dri/renderD128" {
		t.Errorf("render node is %q", gpu.RenderNode)
	}

	if gpu.PCI != "0000:03:00.0" {
		t.Errorf("pci is %q, want the address rather than a piece of the path", gpu.PCI)
	}

	if gpu.Driver != "amdgpu" {
		t.Errorf("driver is %q, want amdgpu", gpu.Driver)
	}
}

// The card is only half the answer. A device with no render node cannot encode
// or render for anybody, and the machines that have one are exactly the ones
// where picking it would be a disaster: a server whose management chip appears
// as card0 and whose real GPU is card1.
func TestDetectGPUIgnoresWhatCannotRender(t *testing.T) {
	root := fakeSysfs(t, []fakeCard{
		// A management chip: a card, a vendor, no render node.
		{pci: "0000:01:00.0", vendor: pciAMD, driver: "ast", nodes: []string{"card0"}},
	})

	if gpu, err := DetectGPU(root); err == nil {
		t.Errorf("a card with no render node was accepted as %v", gpu)
	}
}

func TestDetectGPUIgnoresOtherVendors(t *testing.T) {
	root := fakeSysfs(t, []fakeCard{
		{pci: "0000:00:02.0", vendor: "0x8086", driver: "i915",
			nodes: []string{"card0", "renderD128"}},
	})

	if gpu, err := DetectGPU(root); err == nil {
		t.Errorf("an Intel card was reported as %v, and nothing here knows how to build for it", gpu)
	}
}

// With both vendors in one machine NVIDIA wins, because that is the path that
// has actually been run. The point of the test is that the answer is fixed
// rather than whichever the directory happened to list first.
func TestDetectGPUPrefersNVIDIAWhenBothArePresent(t *testing.T) {
	both := []fakeCard{
		nvidiaCard("0000:01:00.0", "card1", "renderD129"),
		amdCard("0000:03:00.0", "card0", "renderD128"),
	}

	for _, order := range [][]fakeCard{both, {both[1], both[0]}} {
		gpu, err := DetectGPU(fakeSysfs(t, order))
		if err != nil {
			t.Fatalf("detect: %v", err)
		}

		if gpu.Vendor != VendorNVIDIA || gpu.RenderNode != "/dev/dri/renderD129" {
			t.Errorf("picked %v, want the NVIDIA card whichever way round they are listed", gpu)
		}
	}
}

func TestGPUAtNamesOneCard(t *testing.T) {
	root := fakeSysfs(t, []fakeCard{
		nvidiaCard("0000:01:00.0", "card0", "renderD128"),
		amdCard("0000:03:00.0", "card1", "renderD129"),
	})

	gpu, err := GPUAt(root, "/dev/dri/renderD129")
	if err != nil {
		t.Fatalf("at: %v", err)
	}

	if gpu.Vendor != VendorAMD {
		t.Errorf("naming the AMD node gave %v, so the override does not override", gpu)
	}

	if _, err := GPUAt(root, "/dev/dri/renderD130"); err == nil {
		t.Error("a node that is not there was accepted")
	}
}

func TestDetectGPUOnAnEmptyMachine(t *testing.T) {
	if _, err := DetectGPU(t.TempDir()); err == nil {
		t.Error("a machine with no /sys/class/drm at all reported a card")
	}
}

// The failure this whole file exists to prevent: an AMD seat that carries
// NVIDIA's environment starts, streams and encodes in software.
func TestAMDStackCarriesNothingOfNVIDIA(t *testing.T) {
	amd := stackFor(GPU{Vendor: VendorAMD, RenderNode: "/dev/dri/renderD128"})

	env := strings.Join(amd.env, "\n")
	for _, forbidden := range []string{"GBM_BACKEND", "__GLX_VENDOR_LIBRARY_NAME"} {
		if strings.Contains(env, forbidden) {
			t.Errorf("the AMD session sets %s, which names a driver that is not there", forbidden)
		}
	}

	if !strings.Contains(env, "WLR_RENDER_DRM_DEVICE=/dev/dri/renderD128") {
		t.Errorf("the AMD session does not say which card to render on: %q", env)
	}

	// The mirror image of the same mistake, and the more expensive one: with
	// these flags pacman is told the driver is already installed, and on AMD
	// it is not, so the seat gets no driver at all.
	if len(amd.driverFlags) != 0 {
		t.Errorf("the AMD stack passes %v to pacman, which would leave the seat with no driver",
			amd.driverFlags)
	}

	if amd.config["nvidia.runtime"] != "false" {
		t.Error("the AMD stack does not switch the driver injection off, so a seat that " +
			"was built while an NVIDIA card was in this machine keeps it")
	}

	// Vulkan for the games and the 32 bit half of it, since a good many games
	// are still 32 bit and would otherwise land on llvmpipe.
	packages := strings.Join(amd.packages, " ")
	for _, want := range []string{"vulkan-radeon", "lib32-vulkan-radeon", "lib32-mesa"} {
		if !strings.Contains(packages, want) {
			t.Errorf("the AMD stack does not install %s", want)
		}
	}
}

func TestNVIDIAStackIsWhatItAlwaysWas(t *testing.T) {
	// The zero value too, because that is what a caller who forgets the field
	// gets, and it has to be the old behaviour rather than something new.
	for _, gpu := range []GPU{{}, {Vendor: VendorNVIDIA, RenderNode: "/dev/dri/renderD128"}} {
		s := stackFor(gpu)

		if s.encoder != "nvenc" {
			t.Errorf("%v encodes with %q", gpu, s.encoder)
		}

		if s.config["nvidia.runtime"] != "true" {
			t.Errorf("%v does not switch the driver injection on", gpu)
		}

		if len(s.driverFlags) == 0 {
			t.Errorf("%v passes no --assume-installed, so pacman will collide with the injected files", gpu)
		}

		if !strings.Contains(strings.Join(s.env, "\n"), "GBM_BACKEND=nvidia-drm") {
			t.Errorf("%v lost GBM_BACKEND, which is what sends EGL to the vendor", gpu)
		}

		// Naming a card is right where it can be wrong and noise where it
		// cannot: NVIDIA does not select by DRM node.
		if s.adapter != "" {
			t.Errorf("%v sets adapter_name to %q", gpu, s.adapter)
		}
	}
}

// The generated file rather than the struct, because the struct being right and
// the template ignoring it is a real way to get this wrong.
func TestSunshineConfigSaysWhichEncoder(t *testing.T) {
	for _, tc := range []struct {
		gpu     GPU
		want    string
		adapter string
	}{
		{GPU{Vendor: VendorNVIDIA}, "encoder = nvenc", ""},
		{
			GPU{Vendor: VendorAMD, RenderNode: "/dev/dri/renderD129"},
			"encoder = vaapi",
			"adapter_name = /dev/dri/renderD129",
		},
	} {
		s := stackFor(tc.gpu)

		conf, err := render("assets/sunshine.conf", map[string]string{
			"Origins":    "https://10.20.30.71:47990",
			"Resolution": "1920x1080",
			"Encoder":    s.encoder,
			"Adapter":    s.adapter,
		})
		if err != nil {
			t.Fatalf("render: %v", err)
		}

		got := string(conf)

		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: the configuration does not contain %q", tc.gpu.Vendor, tc.want)
		}

		if tc.adapter == "" {
			// Not merely absent: an empty "adapter_name =" would be a setting
			// Sunshine reads and a card it cannot find.
			for _, line := range strings.Split(got, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "adapter_name") {
					t.Errorf("%s: %q, want no adapter line at all", tc.gpu.Vendor, line)
				}
			}

			continue
		}

		if !strings.Contains(got, tc.adapter) {
			t.Errorf("%s: the configuration does not contain %q", tc.gpu.Vendor, tc.adapter)
		}
	}
}

// The drop-in is a systemd file and has to parse as one.
func TestGPUDropInIsAUnitFile(t *testing.T) {
	for _, gpu := range []GPU{
		{Vendor: VendorNVIDIA},
		{Vendor: VendorAMD, RenderNode: "/dev/dri/renderD128"},
	} {
		got := string(stackFor(gpu).dropIn())

		if !strings.Contains(got, "[Service]\n") {
			t.Errorf("%s: no [Service] section, so systemd ignores the whole file:\n%s",
				gpu.Vendor, got)
		}

		body := got[strings.Index(got, "[Service]\n")+len("[Service]\n"):]
		for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
			if !strings.HasPrefix(line, "Environment=") || !strings.Contains(line, "=") {
				t.Errorf("%s: %q is not a setting systemd understands", gpu.Vendor, line)
			}
		}

		if strings.TrimSpace(body) == "" {
			t.Errorf("%s: the drop-in sets nothing", gpu.Vendor)
		}
	}
}
