package seat

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/superuser404notfound/Polyseat/internal/incusx"
)

//go:embed assets
var assets embed.FS

// Generation is the version of the provisioning recipe. Bump it whenever a
// change here has to reach seats that were already built; the interface then
// marks those seats as stale and offers to provision them again.
//
// This is the mechanism that fixes the sort of drift found at the end of M4,
// where seat1 carried security.nesting and seat2 did not simply because seat1
// was built earlier.
const Generation = 1

// Player is the unprivileged user inside every seat that owns the session.
const Player = "player"

// Logger receives progress. Provisioning is slow enough that watching it
// matters, so every step reports rather than only its outcome.
type Logger func(format string, args ...any)

// Provisioner builds one seat. It is deliberately a value rather than a method
// set on the manager: provisioning is a long operation with its own context
// and its own log, and keeping it separate makes it obvious that nothing else
// touches the seat while it runs.
type Provisioner struct {
	Client *incusx.Client
	Seat   Seat
	Uplink string
	Image  string
	Log    Logger

	uid int64 // the player's uid inside the container, learned during the run
}

// Step is one named, idempotent piece of provisioning.
type Step struct {
	Name string
	Run  func(p *Provisioner, ctx context.Context) error
}

// Steps is the recipe, in the only order that works.
//
// Two orderings here are not stylistic and were each paid for once:
//
//   - Packages before nvidia.runtime. Once the driver has been injected, its
//     files sit in the container filesystem as real entries and pacman
//     collides with them. Steam in particular can no longer be installed
//     afterwards, because lib32-mesa cannot be written.
//   - The session last, because it needs the addresses of a running container
//     to generate Sunshine's allowed origins.
func Steps() []Step {
	return []Step{
		{"container", (*Provisioner).stepContainer},
		{"network", (*Provisioner).stepNetwork},
		{"packages", (*Provisioner).stepPackages},
		{"sunshine", (*Provisioner).stepSunshine},
		{"steam", (*Provisioner).stepSteam},
		{"user", (*Provisioner).stepUser},
		{"gpu", (*Provisioner).stepGPU},
		{"nvidia userspace", (*Provisioner).stepNvidiaUserspace},
		{"session", (*Provisioner).stepSession},
	}
}

// Run executes the whole recipe.
func (p *Provisioner) Run(ctx context.Context) error {
	for _, step := range Steps() {
		if err := ctx.Err(); err != nil {
			return err
		}

		p.Log("== %s", step.Name)

		if err := step.Run(p, ctx); err != nil {
			return fmt.Errorf("%s: %w", step.Name, err)
		}
	}

	return nil
}

func (p *Provisioner) name() string { return p.Seat.Name }

// run executes a command inside the seat and reports failure with its output.
func (p *Provisioner) run(ctx context.Context, argv ...string) (string, error) {
	return p.Client.Run(ctx, p.name(), argv...)
}

// sh runs a shell fragment inside the seat.
//
// Used only for the handful of steps that are genuinely shell shaped, appending
// to a file conditionally and the like. Nothing from outside the daemon is ever
// interpolated into one of these strings; seat names are validated against a
// narrow pattern before they get here.
func (p *Provisioner) sh(ctx context.Context, script string) (string, error) {
	return p.Client.Run(ctx, p.name(), "sh", "-c", script)
}

// ------------------------------------------------------------------ container

func (p *Provisioner) stepContainer(ctx context.Context) error {
	status, err := p.Client.Status(p.name())
	if err != nil {
		return err
	}

	if status == "" {
		p.Log("creating the container from %s, this downloads an image", p.Image)

		if err := p.Client.Create(ctx, p.name(), p.Image); err != nil {
			return err
		}

		status = "Stopped"
	}

	// The seat has to be running for everything that follows, and it is the
	// daemon that decides when a container runs, so this does not go through
	// the manager's start path.
	if status != "Running" {
		p.Log("starting the container")

		if err := p.Client.Start(ctx, p.name()); err != nil {
			return err
		}
	}

	return p.waitSystemd(ctx)
}

// waitSystemd waits for the init inside the container to be up.
//
// `incus launch` returns as soon as the container is running, not as soon as
// its systemd is ready. Without this wait the first systemctl call fails with
// "Failed to connect to system scope bus", which reads like a broken image
// rather than a race.
func (p *Provisioner) waitSystemd(ctx context.Context) error {
	deadline := time.Now().Add(90 * time.Second)

	for time.Now().Before(deadline) {
		out, _, err := p.Client.Try(ctx, p.name(), "systemctl", "is-system-running")
		if err != nil {
			return err
		}

		state := strings.TrimSpace(out)
		if state == "running" || state == "degraded" {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}

	return fmt.Errorf("systemd inside the container did not become ready")
}

// ------------------------------------------------------------------- network

func (p *Provisioner) stepNetwork(ctx context.Context) error {
	// eth0 stays on the Incus bridge and is the management path the daemon
	// talks over. eth1 sits directly in the LAN via macvlan, so Moonlight sees
	// the seat as a host of its own and every seat can use the standard
	// Sunshine ports without any juggling.
	//
	// Known property of macvlan: host and container cannot reach each other
	// over it. That is what eth0 is for, and it is also a large part of why a
	// seat cannot attack the host over the network.
	err := p.Client.Configure(ctx, p.name(), nil, map[string]map[string]string{
		"eth1": {
			"type":    "nic",
			"nictype": "macvlan",
			"parent":  p.Uplink,
			"name":    "eth1",
		},
	})
	if err != nil {
		return fmt.Errorf("attach the macvlan interface to %s: %w", p.Uplink, err)
	}

	network := "[Match]\nName=eth1\n\n[Network]\nDHCP=yes\n"
	if p.Seat.Address != "" {
		network = fmt.Sprintf("[Match]\nName=eth1\n\n[Network]\nAddress=%s\nGateway=%s\n",
			p.Seat.Address, p.Seat.Gateway)
		p.Log("static address %s via %s", p.Seat.Address, p.Seat.Gateway)
	} else {
		p.Log("address by DHCP")
	}

	err = p.Client.PushFile(p.name(), "/etc/systemd/network/50-eth1.network",
		[]byte(network), 0o644, 0, 0)
	if err != nil {
		return err
	}

	if _, err := p.run(ctx, "systemctl", "restart", "systemd-networkd"); err != nil {
		return err
	}

	return p.waitNetwork(ctx)
}

// waitNetwork waits until the container can resolve names, which is the first
// thing every following step needs.
func (p *Provisioner) waitNetwork(ctx context.Context) error {
	deadline := time.Now().Add(60 * time.Second)

	for time.Now().Before(deadline) {
		_, code, err := p.Client.Try(ctx, p.name(), "getent", "hosts", "geo.mirror.pkgbuild.com")
		if err != nil {
			return err
		}

		if code == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}

	return fmt.Errorf("the seat did not get working name resolution")
}

// driverFlags tell pacman that the graphics driver is already there.
//
// Inside a seat it always is: it comes from the host through nvidia.runtime and
// never from a package. Without these, pacman picks a provider for the virtual
// driver packages and installs nvidia-utils and lib32-nvidia-utils, whose files
// are exactly the ones the injection already put in the filesystem. The
// transaction then dies with several hundred lines of "exists in filesystem".
//
// They belong on every pacman call that can resolve dependencies, not only on
// the Steam one. Steam depends on vulkan-driver and lib32-vulkan-driver, so
// once it is installed, even a plain system upgrade tries to satisfy them. That
// is why the first attempt to provision an already built seat failed where
// building a new one had worked.
var driverFlags = []string{
	"--assume-installed", "opengl-driver",
	"--assume-installed", "vulkan-driver",
	"--assume-installed", "lib32-opengl-driver",
	"--assume-installed", "lib32-vulkan-driver",
}

// ------------------------------------------------------------------ packages

func (p *Provisioner) stepPackages(ctx context.Context) error {
	// The seats built during the M1 spike carry the CachyOS repository, which
	// was how Sunshine got in before it came from the upstream release. Leaving
	// it would mean two sources for the same package and seats that differ
	// depending on which one won last, so it goes.
	_, err := p.sh(ctx, `if grep -q '^\[cachyos\]' /etc/pacman.conf; then `+
		`sed -i '/^\[cachyos\]$/,+1d' /etc/pacman.conf; `+
		`pacman -Rns --noconfirm cachyos-keyring cachyos-mirrorlist 2>/dev/null; `+
		`echo "removed the CachyOS repository"; fi; exit 0`)
	if err != nil {
		return err
	}

	// multilib is needed for Steam's 32 bit userspace and has to be in place
	// before that step.
	_, err = p.sh(ctx, `grep -q '^\[multilib\]' /etc/pacman.conf || `+
		`printf '\n[multilib]\nInclude = /etc/pacman.d/mirrorlist\n' >> /etc/pacman.conf`)
	if err != nil {
		return err
	}

	p.Log("updating and installing the session packages, this takes a while")

	argv := append([]string{"pacman", "-Syu", "--noconfirm", "--needed"}, driverFlags...)
	argv = append(argv,
		"sway", "swaybg", "foot", "xorg-xwayland",
		"avahi",
		"pipewire", "pipewire-pulse", "pipewire-audio", "wireplumber",
		"mesa", "vulkan-tools", "mesa-utils",
		"python", "sudo", "which")

	out, err := p.run(ctx, argv...)
	if err != nil {
		return err
	}

	p.Log("%s", lastLines(out, 2))

	return nil
}

// stepSunshine installs Sunshine from the upstream release rather than from a
// distribution repository.
//
// The M1 spike took it from the CachyOS repository, which meant bootstrapping a
// third party keyring inside every seat out of the host's package cache. That
// tied the seat to a CachyOS host, and it is also where the mirror lag problem
// came from. LizardByte publish an Arch package with every release, the seat is
// an Arch container, so this is both simpler and better matched.
func (p *Provisioner) stepSunshine(ctx context.Context) error {
	url, version, err := sunshineRelease(ctx)
	if err != nil {
		return err
	}

	installed, code, err := p.Client.Try(ctx, p.name(), "pacman", "-Q", "sunshine")
	if err != nil {
		return err
	}

	if code == 0 && strings.Contains(installed, version) {
		p.Log("sunshine %s already installed", version)

		return nil
	}

	p.Log("installing sunshine %s", version)

	// Downloaded here and pushed in, rather than handing pacman the URL.
	// pacman applies RemoteFileSigLevel to a URL, which on Arch is Required,
	// and it then fails looking for a .sig next to the package that LizardByte
	// do not publish. A local file falls under LocalFileSigLevel, which is
	// Optional. The download is verified by TLS either way, which is the same
	// assurance the release page itself offers.
	pkg, err := download(ctx, url)
	if err != nil {
		return err
	}

	const path = "/root/sunshine.pkg.tar.zst"

	if err := p.Client.PushFile(p.name(), path, pkg, 0o644, 0, 0); err != nil {
		return err
	}

	if _, err := p.run(ctx, "pacman", "-U", "--noconfirm", "--needed", path); err != nil {
		return err
	}

	_, err = p.run(ctx, "rm", "-f", path)

	return err
}

func download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}

	return io.ReadAll(resp.Body)
}

// sunshineRelease asks GitHub for the current release and its Arch package.
//
// Resolved at provisioning time rather than pinned: a pinned version rots, and
// the asset name carries the version, so there is no stable "latest" URL to
// use instead.
func sunshineRelease(ctx context.Context) (url, version string, err error) {
	const api = "https://api.github.com/repos/LizardByte/Sunshine/releases/latest"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return "", "", err
	}

	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("ask GitHub for the Sunshine release: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("the Sunshine release could not be looked up: %s", resp.Status)
	}

	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", "", err
	}

	for _, asset := range release.Assets {
		if strings.HasPrefix(asset.Name, "sunshine-") && strings.HasSuffix(asset.Name, "-x86_64.pkg.tar.zst") {
			return asset.URL, strings.TrimPrefix(release.TagName, "v"), nil
		}
	}

	return "", "", fmt.Errorf("release %s carries no Arch package", release.TagName)
}

// --------------------------------------------------------------------- steam

// stepSteam installs Steam and the 32 bit userspace.
//
// Steam belongs in the base installation rather than in a later add-on: once
// nvidia.runtime has been enabled, the injection leaves real entries behind in
// the container filesystem which never go away, and lib32-mesa can no longer be
// installed over them.
//
// The --assume-installed flags matter just as much. Inside a seat the graphics
// driver always comes from the host and never from a package. Without them
// pacman picks the first provider of those virtual packages, which is a ten
// year old lib32-nvidia driver that would overwrite exactly the injected files.
func (p *Provisioner) stepSteam(ctx context.Context) error {
	p.Log("installing Steam and the 32 bit userspace")

	argv := append([]string{"pacman", "-S", "--noconfirm", "--needed"}, driverFlags...)
	argv = append(argv,
		"steam", "lib32-libglvnd", "lib32-vulkan-icd-loader",
		"ttf-liberation", "zenity")

	_, err := p.run(ctx, argv...)

	return err
}

// ---------------------------------------------------------------------- user

func (p *Provisioner) stepUser(ctx context.Context) error {
	_, err := p.sh(ctx, "id -u "+Player+" >/dev/null 2>&1 || "+
		"useradd -m -s /bin/bash -G video,input,audio "+Player)
	if err != nil {
		return err
	}

	// Without lingering the user's systemd instance goes away whenever nothing
	// is logged in, which for a seat is always.
	if _, err := p.run(ctx, "loginctl", "enable-linger", Player); err != nil {
		return err
	}

	return p.readUID(ctx)
}

func (p *Provisioner) readUID(ctx context.Context) error {
	out, err := p.run(ctx, "id", "-u", Player)
	if err != nil {
		return err
	}

	uid, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return fmt.Errorf("read the uid of %s: %w", Player, err)
	}

	p.uid = uid

	return nil
}

// ----------------------------------------------------------------------- gpu

func (p *Provisioner) stepGPU(ctx context.Context) error {
	config := map[string]string{
		"nvidia.runtime":             "true",
		"nvidia.driver.capabilities": "all",

		// The daemon decides when a seat runs, so Incus must not also start it.
		// Otherwise a seat comes up at boot without its input broker and the
		// two race each other.
		"boot.autostart": "false",

		// Set explicitly rather than left at the default, because leaving it
		// implicit is exactly how seat1 and seat2 came to differ. Nothing in a
		// seat needs nested containers; Steam's own sandbox uses user
		// namespaces, which work without this.
		"security.nesting": "false",
	}

	devices := map[string]map[string]string{
		// mode=0666 so the player can open the render node. Without it the
		// nodes arrive as root:root 0660.
		"gpu": {"type": "gpu", "mode": "0666"},

		// required=false throughout: these are host devices, and a seat that
		// refuses to start because the host has no /dev/uhid is worse than a
		// seat that starts without gamepads.
		"uinput": {
			"type": "unix-char", "source": "/dev/uinput",
			"path": "/dev/uinput", "mode": "0666", "required": "false",
		},

		// /dev/uhid is not optional in practice: Sunshine creates gamepads
		// through inputtino as HID devices, not through uinput. Without it
		// Sunshine cheerfully logs "Gamepad 0 will be Xbox One controller"
		// while no device ever appears, and the client's controller simply
		// does nothing.
		"uhid": {
			"type": "unix-char", "source": "/dev/uhid",
			"path": "/dev/uhid", "mode": "0666", "required": "false",
		},
	}

	if err := p.Client.Configure(ctx, p.name(), config, devices); err != nil {
		return err
	}

	// nvidia.runtime only takes effect on a fresh start.
	p.Log("restarting the container so the driver injection takes effect")

	if err := p.Client.Restart(ctx, p.name(), 30); err != nil {
		return err
	}

	if err := p.waitSystemd(ctx); err != nil {
		return err
	}

	out, err := p.run(ctx, "nvidia-smi", "-L")
	if err != nil {
		return fmt.Errorf("the GPU did not arrive in the seat: %w", err)
	}

	// nvidia-smi exits 0 even when it found nothing, printing "No devices
	// found". Checking the exit code alone once reported a healthy GPU on a
	// container that had none.
	if !strings.HasPrefix(strings.TrimSpace(out), "GPU") {
		return fmt.Errorf("no GPU inside the seat: %s", strings.TrimSpace(out))
	}

	p.Log("%s", strings.TrimSpace(out))

	return nil
}

// stepNvidiaUserspace repairs what nvidia.runtime does not bring along.
//
// libnvidia-container mirrors the host's driver libraries into the container,
// but not the glvnd manifest that points EGL at the NVIDIA vendor in the first
// place, not the GBM backend symlink, and not the EGL platform libraries, which
// on Arch come from separate packages and are not part of the driver at all.
// Without all of it EGL lands on Mesa and Sunshine silently falls back to
// software encoding, reporting "Found H.264 encoder: libx264 [software]" while
// looking perfectly healthy.
//
// Order matters here too: packages first, manifests second. The packages ship
// their own JSON files, and writing those by hand beforehand blocks the
// installation with a file conflict.
func (p *Provisioner) stepNvidiaUserspace(ctx context.Context) error {
	argv := append([]string{"pacman", "-S", "--noconfirm", "--needed"}, driverFlags...)
	argv = append(argv, "egl-gbm", "egl-wayland", "egl-x11")

	if _, err := p.run(ctx, argv...); err != nil {
		return err
	}

	// On the host /usr/lib/gbm/nvidia-drm_gbm.so is only a symlink to
	// libnvidia-allocator. The library is injected, the symlink is not.
	if _, err := p.sh(ctx, "mkdir -p /usr/lib/gbm && "+
		"ln -sf ../libnvidia-allocator.so.1 /usr/lib/gbm/nvidia-drm_gbm.so"); err != nil {
		return err
	}

	// On the host this belongs to nvidia-utils. Installing that package inside
	// the seat would be wrong, since it would put its own libraries up against
	// the injected ones, so the manifest is generated instead.
	glvnd := "{\n" +
		"    \"file_format_version\" : \"1.0.0\",\n" +
		"    \"ICD\" : {\n" +
		"        \"library_path\" : \"libEGL_nvidia.so.0\"\n" +
		"    }\n" +
		"}\n"

	err := p.Client.PushFile(p.name(), "/usr/share/glvnd/egl_vendor.d/10_nvidia.json",
		[]byte(glvnd), 0o644, 0, 0)
	if err != nil {
		return err
	}

	// The Vulkan manifest carries the driver's API version, so it is copied
	// from the host rather than generated. That also keeps it in step when the
	// host driver is updated and the seat is provisioned again.
	icd, err := os.ReadFile("/usr/share/vulkan/icd.d/nvidia_icd.json")
	if err != nil {
		p.Log("! no Vulkan manifest on the host, Vulkan will not work inside the seat: %v", err)
	} else {
		err = p.Client.PushFile(p.name(), "/usr/share/vulkan/icd.d/nvidia_icd.json",
			icd, 0o644, 0, 0)
		if err != nil {
			return err
		}
	}

	// Reported rather than fatal: the authoritative check is which encoder
	// Sunshine picks once the session is up, and that is what the interface
	// shows.
	out, code, err := p.Client.Try(ctx, p.name(), "eglinfo", "-B")
	if err != nil {
		return err
	}

	if code == 0 && strings.Contains(out, "EGL vendor string: NVIDIA") {
		p.Log("EGL reports NVIDIA")
	} else {
		p.Log("! EGL could not be confirmed here, check the encoder once the session runs")
	}

	return nil
}

// ------------------------------------------------------------------- session

func (p *Provisioner) stepSession(ctx context.Context) error {
	if p.uid == 0 {
		if err := p.readUID(ctx); err != nil {
			return err
		}
	}

	home := "/home/" + Player

	for _, dir := range []string{
		home + "/.config",
		home + "/.config/sway",
		home + "/.config/sunshine",
		home + "/.config/systemd",
		home + "/.config/systemd/user",
		home + "/.config/systemd/user/polyseat-sunshine.service.d",
	} {
		if err := p.Client.MakeDir(p.name(), dir, 0o755, p.uid, p.uid); err != nil {
			return err
		}
	}

	sway, err := render("assets/sway.config", map[string]string{"Resolution": p.Seat.Resolution})
	if err != nil {
		return err
	}

	files := []struct {
		path    string
		content []byte
		mode    int
		uid     int64
	}{
		{home + "/.config/sway/config", sway, 0o644, p.uid},
		{home + "/.config/systemd/user/polyseat-sway.service", asset("assets/polyseat-sway.service"), 0o644, p.uid},
		{home + "/.config/systemd/user/polyseat-sunshine.service", asset("assets/polyseat-sunshine.service"), 0o644, p.uid},
		{"/usr/local/bin/polyseat-sunshine-run", asset("assets/sunshine-run.sh"), 0o755, 0},
	}

	for _, f := range files {
		if err := p.Client.PushFile(p.name(), f.path, f.content, f.mode, f.uid, f.uid); err != nil {
			return err
		}
	}

	// The seat tag inside the device names, which is what makes per seat input
	// attribution possible at all.
	//
	// Sunshine reads XDG_SEAT and appends the seat name to its virtual input
	// devices as soon as the seat is not "seat0", turning "Keyboard
	// passthrough" into "Keyboard passthrough (seat1)". Without it every seat's
	// devices carry identical names. A drop-in rather than part of the unit,
	// because the value differs per seat.
	dropin := fmt.Sprintf("[Service]\nEnvironment=XDG_SEAT=%s\n", p.Seat.Name)

	err = p.Client.PushFile(p.name(),
		home+"/.config/systemd/user/polyseat-sunshine.service.d/10-seat.conf",
		[]byte(dropin), 0o644, p.uid, p.uid)
	if err != nil {
		return err
	}

	if err := p.WriteSunshineConfig(ctx); err != nil {
		return err
	}

	// The session units are deliberately not enabled. If they were, the seat
	// would bring itself up the moment its container boots, and Sunshine would
	// read a configuration written before the seat had an address. The daemon
	// starts them instead, in an order it controls. Disabling rather than
	// merely not enabling, because the seats built during the spikes have them
	// enabled and this is what converges them.
	if _, _, err := p.Client.Try(ctx, p.name(), "sudo", "-u", Player, "env",
		fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", p.uid),
		fmt.Sprintf("DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/%d/bus", p.uid),
		"systemctl", "--user", "disable", "polyseat-sway.service"); err != nil {
		return err
	}

	// mDNS, so Moonlight finds the seat by itself.
	if _, code, err := p.Client.Try(ctx, p.name(), "systemctl", "enable", "--now", "avahi-daemon"); err != nil {
		return err
	} else if code != 0 {
		p.Log("! avahi could not be started, Moonlight will need the address typed in")
	}

	return nil
}

// waitAddresses waits until the LAN interface has an address, because
// Sunshine's allowed origins are derived from it.
func (p *Provisioner) waitAddresses(ctx context.Context) error {
	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		addresses, err := p.Client.Addresses(p.name())
		if err == nil && len(addresses["eth1"]) > 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}

	return fmt.Errorf("the seat's LAN interface got no address")
}

// WriteSunshineConfig generates Sunshine's configuration from the seat's
// current addresses.
//
// Called both while provisioning and on every start, because the allowed CSRF
// origins depend on the addresses. Under DHCP those change, and when they do
// the web interface stops accepting saves with an error that gives no hint
// where it comes from.
func (p *Provisioner) WriteSunshineConfig(ctx context.Context) error {
	if p.uid == 0 {
		if err := p.readUID(ctx); err != nil {
			return err
		}
	}

	addresses, err := p.Client.Addresses(p.name())
	if err != nil {
		return err
	}

	var origins []string

	for _, addrs := range addresses {
		for _, addr := range addrs {
			origins = append(origins, "https://"+addr+":47990")
		}
	}

	sort.Strings(origins)

	conf, err := render("assets/sunshine.conf", map[string]string{
		"Origins": strings.Join(origins, ","),
	})
	if err != nil {
		return err
	}

	p.Log("allowed web origins: %s", strings.Join(origins, ", "))

	return p.Client.PushFile(p.name(),
		"/home/"+Player+"/.config/sunshine/sunshine.conf", conf, 0o644, p.uid, p.uid)
}

// ------------------------------------------------------------------- helpers

func asset(name string) []byte {
	data, err := assets.ReadFile(name)
	if err != nil {
		// Embedded at build time, so a failure here is a broken binary.
		panic(err)
	}

	return data
}

func render(name string, data any) ([]byte, error) {
	tmpl, err := template.New("t").Parse(string(asset(name)))
	if err != nil {
		return nil, err
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return strings.Join(lines, "; ")
}
