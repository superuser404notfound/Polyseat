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
	"github.com/superuser404notfound/Polyseat/internal/library"
	"github.com/superuser404notfound/Polyseat/internal/sunshine"
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
const Generation = 31

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

	// GPU is the host's card, detected once by the daemon. It decides which
	// packages the seat gets, which container options are set, what the
	// session's environment is and which encoder Sunshine is told to use.
	//
	// The zero value means NVIDIA, which is not a default so much as the
	// history: every seat built before this field existed was an NVIDIA seat,
	// and a caller that forgets to set it gets exactly what it used to get
	// rather than something new and untested.
	GPU GPU

	// Secrets are the credentials to apply inside the seat. Prepared by the
	// caller, because they have to survive a rebuild: a seat whose container
	// is recreated has to come back with the same Sunshine password, or every
	// device paired with it would have to be paired again.
	Secrets Secrets

	// Library is the shared game pool, nil when the host filesystem cannot
	// share blocks. A seat asking for a library it cannot have is provisioned
	// without one and says so, rather than failing to build at all.
	Library *library.Pool

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
//     afterwards, because lib32-mesa cannot be written. On AMD nothing is
//     injected and the ordering costs nothing, so it is not made conditional.
//   - Proton after the user, because the part of it that sets the seat's
//     default writes into the player's home and needs their uid. Put before,
//     it asks a container that has no such user yet, which fails the whole
//     provisioning run on a seat being built for the first time. Existing
//     seats never showed it: they already had the user.
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
		{"proton", (*Provisioner).stepProton},
		{"flatpak", (*Provisioner).stepFlatpak},
		{"gpu", (*Provisioner).stepGPU},
		{"graphics userspace", (*Provisioner).stepGraphicsUserspace},
		{"library", (*Provisioner).stepLibrary},
		{"session", (*Provisioner).stepSession},
		{"sunshine credentials", (*Provisioner).stepCredentials},
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
		// Each attempt is bounded, or the deadline above is decoration: this
		// call is a read that answers in milliseconds, and when it hung instead
		// the loop never came round to notice that ninety seconds had passed.
		attempt, cancel := context.WithTimeout(ctx, 20*time.Second)
		out, _, err := p.Client.Try(attempt, p.name(), "systemctl", "is-system-running")
		cancel()

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
	_, err := p.Client.Configure(ctx, p.name(), nil, map[string]map[string]string{
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

	// The LAN wins the default route, and it has to win it explicitly.
	//
	// A seat has two interfaces that both offer a way out, and the image
	// configures eth0 with the metric systemd-networkd uses by default. Left
	// alone, eth1 gets the same one, and a seat ends up with two default routes
	// of equal weight: which one a connection takes is then a matter of order,
	// and a reply can come back the way the request did not. Steam was seen
	// logging on happily over the bridge while Big Picture reported no internet
	// connection at all.
	//
	// eth1 is the right winner. It is the seat's own address on the network,
	// the one Moonlight reaches it by, and it goes straight to the LAN gateway
	// rather than through the bridge's translation. eth0 stays reachable as the
	// management path, which only ever needs the route to its own subnet.
	const metric = 100

	network := fmt.Sprintf(
		"[Match]\nName=eth1\n\n[Network]\nDHCP=yes\n\n[DHCPv4]\nRouteMetric=%d\n", metric)

	if p.Seat.Address != "" {
		network = fmt.Sprintf(
			"[Match]\nName=eth1\n\n[Network]\nAddress=%s\n\n[Route]\nGateway=%s\nMetric=%d\n",
			p.Seat.Address, p.Seat.Gateway, metric)
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
// NVIDIA only, which is why they are reached through stackFor rather than used
// directly. Inside an NVIDIA seat the driver always is already there: it comes
// from the host through nvidia.runtime and never from a package. Without these,
// pacman picks a provider for the virtual driver packages and installs
// nvidia-utils and lib32-nvidia-utils, whose files are exactly the ones the
// injection already put in the filesystem. The transaction then dies with
// several hundred lines of "exists in filesystem".
//
// On AMD the opposite holds and using them would be the bug: there the driver
// is a package, so the virtual providers have to resolve normally.
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

// stack is the whole vendor specific answer for this seat's host. See gpu.go.
func (p *Provisioner) stack() stack { return stackFor(p.GPU) }

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

	// Flatpak needs the setuid bubblewrap in a container, and pacman will not
	// swap one for the other on its own.
	//
	// The measurement behind this, because it is not guessable. A seat is an
	// unprivileged container, and mounting a fresh /proc inside a nested
	// namespace is refused there. bwrap only needs that when it also unshares
	// the pid namespace, which is precisely what splits the two cases: Steam's
	// pressure-vessel does not, so Proton was never affected, while flatpak
	// does, so every flatpak application died with
	//     bwrap: Can't mount proc on /newroot/proc: Operation not permitted
	//
	// The obvious fix is security.nesting on the container. This is the smaller
	// one. A setuid binary inside an unprivileged container gains the container's
	// root, which is an ordinary unprivileged uid on the host, whereas nesting
	// relaxes what the whole container may do for the sake of one program.
	//
	// bubblewrap-suid provides bubblewrap, so naming it in the transaction below
	// satisfies flatpak's dependency by itself. Only an already installed plain
	// bubblewrap is in the way, and -Rdd is what gets it out without pacman
	// refusing on behalf of the dependency that is about to be satisfied again
	// in the same run.
	_, err = p.sh(ctx, `if pacman -Qq bubblewrap >/dev/null 2>&1; then `+
		`pacman -Rdd --noconfirm bubblewrap && `+
		`echo "removed the plain bubblewrap, the setuid one follows"; fi; exit 0`)
	if err != nil {
		return err
	}

	p.Log("updating and installing the session packages, this takes a while")

	argv := append([]string{"pacman", "-Syu", "--noconfirm", "--needed"}, p.stack().driverFlags...)
	argv = append(argv, p.stack().packages...)
	argv = append(argv,
		"sway", "swaybg", "foot", "xorg-xwayland",
		// The desktop proper. A seat used to come up as sway with a single
		// terminal in it, which meant a stream you could look at but not use:
		// no way to start a launcher that was not in the Sunshine app list, and
		// no way to install one either.
		"waybar", "fuzzel", "thunar", "gvfs", "wl-clipboard",
		// So a seat can be used from a client that has neither keyboard nor
		// mouse, which is most of them: an Apple TV, a phone, a television.
		// squeekboard draws the letters and polyseat-pad-pointer turns the
		// gamepad into a pointer to press them with. Both have to live in the
		// seat and travel in the video stream, because the client cannot
		// supply either: Moonlight on tvOS sends modifiers rather than text.
		"squeekboard", "python-evdev",
		// Flatpak is how a seat gets software. The player has no sudo by
		// design, and `flatpak --user` needs none, so this is the one route
		// that does not either widen what the seat may do or route every
		// install through the host administrator.
		"flatpak", "bubblewrap-suid", "xdg-desktop-portal-wlr",
		// The other way software arrives, and the one some things have: an
		// emulator is quite often published as an AppImage and as nothing else.
		//
		// fuse2 rather than the fuse3 the image already has, measured rather
		// than read: the AppImage runtime dlopens libfuse.so.2 by name, so with
		// only fuse3 present every classic AppImage stops at
		//     dlopen(): error loading libfuse.so.2
		// which reads like a broken download. /dev/fuse is in the container
		// already and fusermount3 is setuid, so this one package is the whole
		// of what was missing.
		"fuse2",
		// Named rather than left to arrive as somebody else's dependency,
		// because the box art depends on it now: an AppImage often carries its
		// icon only as an SVG, Eden among them, and rsvg-convert is what turns
		// that into the PNG a client will actually draw. It is in a seat either
		// way, since the desktop pulls it in; this is so that it stays there.
		"librsvg",
		// The graphical way in, so that installing something is not a command
		// somebody has to be told. gnome-software costs almost nothing here
		// because a seat already has the toolkit underneath it, and with
		// flatpak present it browses Flathub with pictures and a search field.
		// The alternatives were measured: bazaar 52 MB, discover 212 MB.
		"gnome-software",
		// What a games machine is expected to have on it already. Lutris
		// fetches its own Wine builds, so plain wine is not needed and would
		// cost more than everything else here together.
		"lutris", "firefox",
		// gamescope for scaling, MangoHud for the framerate cap, and Noto
		// because a game with no font for its own text looks broken rather
		// than unstyled.
		//
		// The 32 bit build alongside it because a good many games still are:
		// the cap is applied by preloading a library, and a 32 bit process
		// cannot load the 64 bit one, so without this the games most likely to
		// need a cap would be the ones running without it.
		"gamescope", "mangohud", "lib32-mangohud", "noto-fonts",
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
// On NVIDIA the --assume-installed flags matter just as much. Inside such a
// seat the graphics driver always comes from the host and never from a
// package. Without them pacman picks the first provider of those virtual
// packages, which is a ten year old lib32-nvidia driver that would overwrite
// exactly the injected files. On AMD there are no flags, and lib32-mesa and
// lib32-vulkan-radeon are already in by the time this runs, so the same
// dependencies resolve to what is installed.
func (p *Provisioner) stepSteam(ctx context.Context) error {
	p.Log("installing Steam and the 32 bit userspace")

	argv := append([]string{"pacman", "-S", "--noconfirm", "--needed"}, p.stack().driverFlags...)
	argv = append(argv,
		"steam", "lib32-libglvnd", "lib32-vulkan-icd-loader",
		"ttf-liberation", "zenity")

	_, err := p.run(ctx, argv...)

	return err
}

// --------------------------------------------------------------------- proton

// protonDir is where Steam looks for compatibility tools that did not come
// with it. Steam scans several places; this is the system wide one, chosen over
// a directory in the player's home so that the tool is there for whoever ends
// up in the seat rather than tied to one home directory.
const protonDir = "/usr/share/steam/compatibilitytools.d"

// protonName is what the tool is called on disk. Fixed rather than taken from
// the archive, whose top level directory carries the version and the instruction
// set in its name, so that an upgrade replaces the previous build instead of
// leaving Steam with a menu of every version this seat has ever seen.
const protonName = "proton-cachyos"

// protonStamp records which release is unpacked, written by us rather than read
// out of the archive's own version file. What upstream puts in there is theirs
// to change; the tag is what this step decided on and therefore what it should
// compare against.
const protonStamp = protonDir + "/" + protonName + "/polyseat-release"

// stepProton adds Proton CachyOS to the seat.
//
// Steam ships its own Proton and that keeps working; this is the build most
// people on this distribution would reach for, and without it the seat's
// version list is Valve's and nothing else. It arrives the same way Sunshine
// does, from the project's own release rather than from a repository, which
// keeps a seat on plain Arch from having to trust a second package source for
// one compatibility tool.
//
// Nothing here is fatal. A seat whose Proton could not be fetched still starts,
// still streams and still plays everything Valve's Proton plays, so a GitHub
// that is briefly unreachable is a line in the log rather than a seat that
// failed to build. The one case worth being loud about is a download whose
// checksum does not match, because that is not a hiccup.
func (p *Provisioner) stepProton(ctx context.Context) error {
	if err := p.installProton(ctx); err != nil {
		return err
	}

	// The default is set whether or not anything was installed just now. The
	// common pass is the one that finds the current build already there, and a
	// seat that came out of an older generation, or whose Steam was running the
	// last time this ran, still needs its setting.
	_, code, err := p.Client.Try(ctx, p.name(), "test", "-d", protonDir+"/"+protonName)
	if err != nil {
		return err
	}

	if code != 0 {
		return nil
	}

	return p.stepSteamPlay(ctx)
}

// installProton puts the current release in the seat, or leaves what is already
// there alone.
func (p *Provisioner) installProton(ctx context.Context) error {
	release, err := protonRelease(ctx, p.isaLevel(ctx))
	if err != nil {
		p.Log("! Proton CachyOS could not be looked up, the seat keeps Valve's Proton: %v", err)

		return nil
	}

	stamp, _, err := p.Client.Try(ctx, p.name(), "cat", protonStamp)
	if err != nil {
		return err
	}

	if strings.TrimSpace(stamp) == release.tag {
		p.Log("Proton CachyOS %s already installed", release.tag)

		// The manifest is rewritten even so, and it is the one part of an
		// installation cheap enough to keep rewriting. A seat built before the
		// name was fixed carries the version in it, and this is what moves such
		// a seat over without downloading a third of a gigabyte it already has.
		//
		// Only when Steam is not running, and the two are the same window on
		// purpose. Renaming the tool is exactly what invalidates a setting that
		// names the old one, so doing it while Steam holds config.vdf would
		// leave the seat pointing at a tool that no longer exists, which reads
		// as the default silently reverting to Valve's Proton.
		running, err := p.steamRunning(ctx)
		if err != nil || running {
			return err
		}

		_, _, err = p.Client.Try(ctx, p.name(), "sh", "-c", fmt.Sprintf(
			"cat > %s/%s/compatibilitytool.vdf <<'VDF'\n%s\nVDF\n",
			protonDir, protonName, compatToolManifest(release.tag)))

		return err
	}

	// Fetched here rather than in the seat, and before the archive rather than
	// after it: it is a few dozen bytes, and a release whose checksum cannot be
	// read is one to walk away from before spending a third of a gigabyte on it.
	sum, err := protonChecksum(ctx, release.sum)
	if err != nil {
		p.Log("! the Proton CachyOS checksum could not be read, nothing was installed: %v", err)

		return nil
	}

	p.Log("installing Proton CachyOS %s (%d MB)", release.tag, release.size/1024/1024)

	// Fetched by the seat rather than by the daemon. The archive is a third of
	// a gigabyte, and the way every other download here works would read the
	// whole of it into the daemon's memory and then push a second copy of it
	// through the Incus API.
	//
	// Unpacked beside the target and moved into place, so that a download that
	// dies half way through leaves the previous Proton where it was rather than
	// a half unpacked directory Steam would offer anyway.
	out, code, err := p.Client.Try(ctx, p.name(), "sh", "-c",
		protonScript(release.url, sum, release.tag))
	if err != nil {
		return err
	}

	if code != 0 {
		// Tidied up whatever is left, or the next run finds a stale archive and
		// a half unpacked directory in the way of its own.
		_, _, _ = p.Client.Try(ctx, p.name(), "sh", "-c",
			"rm -rf "+protonDir+"/.polyseat-new "+protonDir+"/proton.tar.xz")

		p.Log("! Proton CachyOS was not installed, the seat keeps Valve's Proton: %s",
			strings.TrimSpace(lastLine(out)))

		return nil
	}

	p.Log("Proton CachyOS %s is in the seat", release.tag)

	return nil
}

// steamRunning reports whether Steam holds its configuration file open.
//
// Everything that changes what Steam has already read waits for this to be
// false. Steam keeps config.vdf in memory and writes the whole of it out when
// it exits, so a change made underneath it is not merely ignored, it is undone.
func (p *Provisioner) steamRunning(ctx context.Context) (bool, error) {
	_, code, err := p.Client.Try(ctx, p.name(), "pgrep", "-x", "steam")
	if err != nil {
		return false, err
	}

	return code == 0, nil
}

// steamConfigPath is where Steam keeps the setting for which compatibility tool
// to run everything else with.
const steamConfigPath = steamRoot + "/config/config.vdf"

// stepSteamPlay makes Proton CachyOS the seat's default.
//
// Otherwise the tool is merely present, and every game still runs under Valve's
// Proton until somebody walks through Steam's settings with a gamepad, which is
// the one interaction this whole project exists to avoid.
//
// Only while Steam is not running, and that is not a nicety. Steam holds this
// file in memory and writes the whole of it out when it exits, so an edit made
// underneath a running Steam is not merely ignored, it is reverted along with
// anything else that changed in between. Provisioning is a reliable moment for
// it: the session has just been rebuilt and nothing has started Steam yet.
func (p *Provisioner) stepSteamPlay(ctx context.Context) error {
	running, err := p.steamRunning(ctx)
	if err != nil {
		return err
	}

	if running {
		p.Log("Steam is running, so the default Proton stays as it is for now")

		return nil
	}

	// A seat whose Steam has never started has no file, which is not a problem
	// to report: it is the ordinary state of a seat being built, and the setting
	// is written into a file Steam then fills in around.
	existing, readErr := p.Client.ReadFile(p.name(), steamConfigPath)
	if readErr != nil {
		existing = nil
	}

	updated, changed, err := SetCompatTool(existing, protonName)
	if err != nil {
		p.Log("! Steam's configuration was left alone because it could not be read: %v", err)

		return nil
	}

	if !changed {
		return nil
	}

	if p.uid == 0 {
		if err := p.readUID(ctx); err != nil {
			return err
		}
	}

	owner := strconv.FormatInt(p.uid, 10)

	if _, err := p.run(ctx, "install", "-d", "-o", owner, "-g", owner, steamRoot+"/config"); err != nil {
		return err
	}

	if err := p.Client.PushFile(p.name(), steamConfigPath, updated, 0o644, p.uid, p.uid); err != nil {
		return err
	}

	p.Log("Proton CachyOS is now what this seat runs Windows games with")

	return nil
}

// lastLine is what to quote from a shell script that failed: the message that
// stopped it, rather than the whole of a download's progress.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	return lines[len(lines)-1]
}

// protonScript fetches, verifies and unpacks one build inside the seat.
//
// The seat does the fetching. The archive is a third of a gigabyte, and the way
// every other download in this file works would read the whole of it into the
// daemon's memory and then push a second copy of it through the Incus API.
//
// Order is the point of the rest. The checksum is checked before anything is
// unpacked, the unpacking goes to a directory beside the target, and only a
// complete unpacking replaces what was there. A download that dies half way
// through therefore leaves the seat with the Proton it already had rather than
// with a partial one that Steam would list and offer anyway.
func protonScript(url, sum, tag string) string {
	return fmt.Sprintf(`set -e
mkdir -p %[1]s
cd %[1]s
rm -rf .polyseat-new proton.tar.xz
curl -fsSL --retry 2 -o proton.tar.xz %[2]q
echo %[3]q'  proton.tar.xz' | sha512sum -c -
mkdir .polyseat-new
tar -xJf proton.tar.xz -C .polyseat-new --strip-components=1
rm -f proton.tar.xz
printf '%%s\n' %[4]q > .polyseat-new/polyseat-release
cat > .polyseat-new/compatibilitytool.vdf <<'VDF'
%[6]s
VDF
rm -rf %[5]q
mv .polyseat-new %[5]q
`, protonDir, url, sum, tag, protonName, compatToolManifest(tag))
}

// compatToolManifest is the file Steam identifies the tool by, written here
// rather than kept as it comes.
//
// The name upstream puts in it carries the version and the instruction set, so
// every update introduces a tool with a new identity. Steam records the chosen
// compatibility tool by that identity, in config.vdf and in every per game
// override, and all of those quietly stop pointing at anything the next time
// this updates. A fixed name is what Valve's own proton_experimental does, for
// the same reason: the identity is the channel, not the build.
//
// The version is kept where it belongs, in the name shown in the menu, so that
// the seat still says which build it is running.
func compatToolManifest(tag string) string {
	return `"compatibilitytools"
{
  "compat_tools"
  {
    "` + protonName + `"
    {
      "install_path" "."
      "display_name" "` + protonDisplayName(tag) + `"
      "from_oslist"  "windows"
      "to_oslist"    "linux"
    }
  }
}`
}

// protonDisplayName turns a release tag into something worth reading in a menu.
// "cachyos-11.0-20260703-slr" is the tag; the middle of it is the version.
func protonDisplayName(tag string) string {
	version := strings.TrimSuffix(strings.TrimPrefix(tag, "cachyos-"), "-slr")

	if version == "" || strings.ContainsAny(version, `"\`) {
		return "Proton CachyOS"
	}

	return "Proton CachyOS " + version
}

// protonChecksum reads the hash out of the published sha512sum file.
//
// The file holds the hash and the name of what it belongs to, and only the hash
// is wanted here, because the archive is saved under a name of this step's
// choosing rather than the one upstream used.
func protonChecksum(ctx context.Context, url string) (string, error) {
	body, err := download(ctx, url)
	if err != nil {
		return "", err
	}

	return parseChecksum(string(body))
}

// parseChecksum takes the hash out of that file and refuses anything that is
// not one.
//
// Separate from fetching it, and strict on purpose. What arrives here goes
// straight into a shell command, and the thing a checksum most often turns into
// when a release is malformed or a proxy interferes is an error page.
func parseChecksum(body string) (string, error) {
	hash, _, found := strings.Cut(strings.TrimSpace(body), " ")
	if !found || len(hash) != 128 {
		return "", fmt.Errorf("%q is not a sha512 checksum", strings.TrimSpace(body))
	}

	if strings.Trim(hash, "0123456789abcdefABCDEF") != "" {
		return "", fmt.Errorf("%q is not a sha512 checksum", hash)
	}

	return hash, nil
}

// isaLevel reports which build of Proton this seat can run.
//
// x86-64-v3 is what the optimised build needs, and it is a set of features
// rather than a single one. Asked of the seat rather than of the host because
// the seat is where it runs, even though on one machine the answer is the same.
// Anything unclear answers with the baseline build, which runs everywhere.
func (p *Provisioner) isaLevel(ctx context.Context) string {
	out, code, err := p.Client.Try(ctx, p.name(), "cat", "/proc/cpuinfo")
	if err != nil || code != 0 {
		return "x86_64"
	}

	if supportsV3(out) {
		return "x86_64_v3"
	}

	return "x86_64"
}

// supportsV3 reads the answer out of /proc/cpuinfo.
//
// Separate from fetching it so that it can be tested against the real file from
// a processor that has the whole set and one that does not, which is the only
// way to find out that a shorter check passes something it should not.
func supportsV3(cpuinfo string) bool {
	flags := ""

	for _, line := range strings.Split(cpuinfo, "\n") {
		if rest, ok := strings.CutPrefix(line, "flags"); ok {
			if _, value, found := strings.Cut(rest, ":"); found {
				flags = " " + strings.TrimSpace(value) + " "

				break
			}
		}
	}

	// The full set the level is defined by. A shorter check would pass on a
	// processor that has AVX2 and is missing something else in the set, and the
	// symptom of that is every game dying on an illegal instruction.
	for _, want := range []string{
		"avx", "avx2", "bmi1", "bmi2", "f16c", "fma", "abm", "movbe", "xsave",
	} {
		if !strings.Contains(flags, " "+want+" ") {
			return false
		}
	}

	return true
}

// protonAsset is one build from a Proton CachyOS release.
type protonAsset struct {
	tag  string
	url  string
	sum  string
	size int64
}

// protonRelease asks GitHub for the current Proton CachyOS and picks the build
// for this instruction set.
//
// Resolved at provisioning time rather than pinned, for the same reason
// Sunshine is: a pinned version rots, and the asset names carry the version.
func protonRelease(ctx context.Context, isa string) (protonAsset, error) {
	const api = "https://api.github.com/repos/CachyOS/proton-cachyos/releases/latest"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return protonAsset{}, err
	}

	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return protonAsset{}, fmt.Errorf("ask GitHub for the Proton CachyOS release: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return protonAsset{}, fmt.Errorf("the Proton CachyOS release could not be looked up: %s", resp.Status)
	}

	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return protonAsset{}, err
	}

	return pickProton(release.TagName, isa, release.Assets)
}

// pickProton chooses the archive and finds its published checksum.
//
// Separate from the request so that the choice can be tested against a real
// release listing. Every release carries several architectures and two
// instruction set levels of the same version, and picking by "contains x86_64"
// would match the v3 build on a processor that cannot run it.
func pickProton(tag, isa string, assets []struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
},
) (protonAsset, error) {
	archive := "-" + isa + ".tar.xz"

	for _, asset := range assets {
		if !strings.HasSuffix(asset.Name, archive) {
			continue
		}

		want := strings.TrimSuffix(asset.Name, ".tar.xz") + ".sha512sum"

		for _, sum := range assets {
			if sum.Name != want {
				continue
			}

			return protonAsset{tag: tag, url: asset.URL, sum: sum.URL, size: asset.Size}, nil
		}

		return protonAsset{}, fmt.Errorf("%s carries no checksum, so it is not being installed", asset.Name)
	}

	return protonAsset{}, fmt.Errorf("release %s carries no %s build", tag, isa)
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

// stepFlatpak gives the player a way to install software.
//
// The player has no sudo, on purpose, so pacman is not available to them and
// anything they want has to come either through the daemon or through
// something that needs no privileges at all. `flatpak --user` is the second
// kind: the installation lives under the player's home, nothing is written
// outside it, and the seat gains no rights it did not have.
//
// The remote is added per user rather than system wide for the same reason.
// Adding it as root would work and would also be the daemon quietly deciding
// what every future seat trusts; this way the trust sits in the home directory
// of the account that uses it.
func (p *Provisioner) stepFlatpak(ctx context.Context) error {
	out, code, err := p.Client.Try(ctx, p.name(), "sudo", "-u", Player, "env",
		"HOME=/home/"+Player,
		"flatpak", "remote-add", "--user", "--if-not-exists",
		"flathub", "https://dl.flathub.org/repo/flathub.flatpakrepo")
	if err != nil {
		return err
	}

	// A seat without Flathub still works, it just cannot install anything new
	// until somebody adds a remote. Not worth failing a whole provisioning run
	// over something that is only a network hiccup most of the time.
	if code != 0 {
		p.Log("! Flathub could not be added, installing new software in this seat will not work yet")
		p.Log("  %s", lastLines(out, 2))

		return nil
	}

	p.Log("Flathub is available to %s, no password needed", Player)

	// Let sandboxed applications reach the shared library.
	//
	// Found by trying it rather than by reading the manifest. M6 promised that
	// a launcher other than Steam could share games through the seat's library
	// directory, and for a flatpak launcher that quietly was not true: Heroic
	// lists ~/Games/Heroic, ~/.steam and /mnt among the paths it may touch, and
	// the library is none of them, so it reported the directory as not
	// existing. Everything about the sharing worked except that the launcher
	// could not see it.
	//
	// A user wide override rather than one per application, because the next
	// launcher somebody installs has the same problem and nothing would tell
	// them why. This is a seat: the games directory is what it is for.
	//
	// The other two put the framerate cap inside the sandbox. A flatpak sees
	// neither the seat's environment nor its home directory, so a launcher
	// installed this way would be the one thing in the seat that ignored the
	// cap, and the launchers people install here are exactly the ones that run
	// games. The variable turns MangoHud's Vulkan layer on and the read only
	// path is where polyseat-fps writes what the cap should be.
	if _, code, err := p.Client.Try(ctx, p.name(), "sudo", "-u", Player, "env",
		"HOME=/home/"+Player,
		"flatpak", "override", "--user",
		"--filesystem="+LibraryMount,
		"--env=MANGOHUD=1",
		"--filesystem=xdg-config/MangoHud:ro"); err != nil {
		return err
	} else if code != 0 {
		p.Log("! flatpak applications may not be able to see %s", LibraryMount)
	}

	p.flatpakMangoHud(ctx)

	return nil
}

// flatpakMangoHud installs the MangoHud Vulkan layer into every runtime the
// seat's flatpak applications use.
//
// The override above says MangoHud is enabled; this is what makes there be a
// MangoHud to enable. The layer the seat has is an Arch package outside the
// sandbox, and a flatpak cannot load it, so without the matching extension the
// variable sets nothing in motion and a game started from Heroic runs at
// whatever framerate it likes.
//
// Per runtime branch rather than a fixed one, because the extension is versioned
// with the runtime and the branch a seat needs is whichever its applications
// pulled in. On a seat that has installed nothing yet there is no runtime and
// nothing to do, which is why this is also called after an install.
//
// Never fails the caller. A seat without the extension streams perfectly well;
// it just does not cap what runs inside a sandbox.
func (p *Provisioner) flatpakMangoHud(ctx context.Context) {
	out, code, err := p.Client.Try(ctx, p.name(), "sudo", "-u", Player, "env",
		"HOME=/home/"+Player,
		"flatpak", "list", "--user", "--runtime", "--columns=application,branch")
	if err != nil || code != 0 {
		return
	}

	for _, branch := range platformBranches(out) {
		_, code, err := p.Client.Try(ctx, p.name(), "sudo", "-u", Player, "env",
			"HOME=/home/"+Player,
			"flatpak", "install", "--user", "-y", "--noninteractive", "flathub",
			"org.freedesktop.Platform.VulkanLayer.MangoHud//"+branch)
		if err != nil {
			return
		}

		if code != 0 {
			p.Log("! the framerate cap will not reach flatpak applications on runtime %s", branch)
			continue
		}

		p.Log("the framerate cap reaches flatpak applications on runtime %s", branch)
	}
}

// platformBranches picks the freedesktop runtime versions out of a flatpak
// listing.
//
// Only that runtime, because the extension exists for it and not for the GNOME
// or KDE ones, which carry it themselves through their own base.
func platformBranches(list string) []string {
	seen := map[string]bool{}

	var branches []string

	for _, line := range strings.Split(list, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "org.freedesktop.Platform" {
			continue
		}

		if seen[fields[1]] {
			continue
		}

		seen[fields[1]] = true
		branches = append(branches, fields[1])
	}

	return branches
}

func (p *Provisioner) readUID(ctx context.Context) error {
	// A read, so it is bounded like the others. This is the call that answered
	// "no such user" while the rest of provisioning had not run yet, and it is
	// also one that would hang for ever on a stalled connection.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

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
	// Said on every run, because which stack a seat was built on is the first
	// thing anybody needs when the encoder line later says libx264, and the
	// provisioning log is the only place it would otherwise not appear.
	p.Log("graphics: %s", p.GPU)

	config := map[string]string{
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

	// The vendor's own keys on top. On NVIDIA that switches the driver
	// injection on; on AMD it switches it off, which is not the same as
	// leaving it out: Incus merges what it is given, so a seat that was built
	// while an NVIDIA card was in this machine would keep the injection and
	// fail to start over a driver that is no longer there.
	for k, v := range p.stack().config {
		config[k] = v
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

		// What Wine uses for the synchronisation primitives Windows programs
		// expect. Without it Proton falls back to esync and fsync, which is
		// what it did before the kernel grew this, and Proton CachyOS is built
		// around having it. required=false because the module is recent enough
		// that a host may simply not have it, and a seat that will not start
		// over a missing sync device would be a poor trade for it.
		"ntsync": {
			"type": "unix-char", "source": "/dev/ntsync",
			"path": "/dev/ntsync", "mode": "0666", "required": "false",
		},
	}

	changed, err := p.Client.Configure(ctx, p.name(), config, devices)
	if err != nil {
		return err
	}

	// nvidia.runtime only takes effect on a fresh start, and a device that was
	// just added is not in a running container either, so a change here costs
	// a restart. Nothing changed means nothing to restart, which is what keeps
	// re-provisioning a healthy seat from interrupting it.
	if changed {
		p.Log("restarting the container so the driver injection takes effect")

		// Ninety seconds rather than thirty: a seat with a session in it takes
		// longer to shut down than an empty container does.
		if err := p.Client.Restart(ctx, p.name(), 90); err != nil {
			return err
		}

		if err := p.waitSystemd(ctx); err != nil {
			return err
		}
	}

	if p.GPU.Vendor == VendorAMD {
		return p.verifyAMD(ctx)
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

// verifyAMD is the AMD half of the check nvidia-smi does for the other vendor.
//
// Two questions, and only the first is fatal. Whether the render node arrived
// in the seat is unambiguous and nothing works without it. Whether the card
// can encode is answered by vainfo, which is the one command that predicts a
// software fallback before anybody tries to play, but its output is a table
// this code does not own: a change in its wording would fail every AMD seat on
// the machine over a card that is perfectly fine. So it warns loudly instead,
// and the authoritative answer stays where it already is, in the encoder line
// the interface shows once the session runs.
func (p *Provisioner) verifyAMD(ctx context.Context) error {
	node := p.GPU.RenderNode
	if node == "" {
		node = "/dev/dri/renderD128"
	}

	if _, code, err := p.Client.Try(ctx, p.name(), "test", "-c", node); err != nil {
		return err
	} else if code != 0 {
		return fmt.Errorf("%s did not arrive in the seat, so the GPU is not there", node)
	}

	// --display drm because there is no Wayland or X display at this point in
	// provisioning, and vainfo's default is to look for one and fail.
	out, code, err := p.Client.Try(ctx, p.name(),
		"vainfo", "--display", "drm", "--device", node)
	if err != nil {
		return err
	}

	if code != 0 {
		p.Log("! vainfo could not open %s, the seat may encode in software: %s",
			node, lastLines(out, 3))

		return nil
	}

	// VAEntrypointEncSlice is the encoder, and the low power variant
	// VAEntrypointEncSliceLP has it as a prefix, so one test covers both. A
	// card whose driver loaded but offers only decoding entry points is
	// exactly the case that looks healthy everywhere else, so the string is
	// worth matching for rather than taking vainfo's exit code as an answer.
	if !strings.Contains(out, "VAEntrypointEncSlice") {
		p.Log("! %s has no VA-API encoder entry point, the seat will encode in software",
			node)

		return nil
	}

	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Driver version") {
			p.Log("%s", strings.TrimSpace(line))

			break
		}
	}

	p.Log("VA-API on %s can encode", node)

	return nil
}

// stepGraphicsUserspace makes the driver usable from inside the seat.
//
// It is a whole step on NVIDIA and nothing at all on AMD, and that asymmetry
// is the point rather than an oversight. NVIDIA arrives as files injected past
// the package manager, so everything the packages would have brought with them
// has to be put back by hand. Mesa arrives as packages, which brought their
// own manifests, their own GBM backend and their own Vulkan ICD with them, and
// anything written here would be a second copy competing with the real one.
func (p *Provisioner) stepGraphicsUserspace(ctx context.Context) error {
	if p.GPU.Vendor == VendorAMD {
		p.Log("Mesa came from packages, nothing to repair")

		return nil
	}

	return p.stepNvidiaUserspace(ctx)
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

// ------------------------------------------------------------------- library

// LibraryMount is where a seat's share of the pooled library appears inside the
// container, for everything that is not Steam.
//
// Steam gets it somewhere else, see steamApps. This one carries shared/, which
// is the launcher agnostic half, and it is also what the app list is generated
// from.
const LibraryMount = "/home/" + Player + "/games"

// libraryDevice is the Incus device name, kept out of the way of the network
// and input devices.
const libraryDevice = "library"

// steamRoot is where Steam keeps itself inside a seat.
const steamRoot = "/home/" + Player + "/.local/share/Steam"

// steamApps is Steam's own library folder, and the pooled directory is mounted
// straight onto it.
//
// This was a second library folder next to Steam's own for a long time, which
// worked and was still wrong. Steam always registers its own directory as
// folder 0, puts it back on every start, and installs there unless somebody
// picks otherwise in a dialog that then remembers the choice per account. So a
// seat came up offering two destinations, defaulted to the private one, and the
// shared library only worked for people who noticed the dropdown. Mounting the
// pool onto Steam's own folder leaves exactly one destination, and it is the
// shared one, from the moment the seat is created.
//
// It is only the library folder that is mounted, not ~/.local/share/Steam:
// Steam keeps its client, its runtimes and its per account data there, and
// putting a shared directory under those would be handing the host files Steam
// expects to own alone.
const steamApps = steamRoot + "/steamapps"

// steamLibraryDevice is the Incus device carrying it.
const steamLibraryDevice = "steamlib"

func (p *Provisioner) stepLibrary(ctx context.Context) error {
	if !p.Seat.Library || p.Library == nil {
		// Detaching rather than ignoring. Turning the shared library off for a
		// seat has to actually take the mount away, or the setting would be a
		// label with nothing behind it.
		if _, err := p.Client.Configure(ctx, p.name(), nil, map[string]map[string]string{
			libraryDevice:      nil,
			steamLibraryDevice: nil,
		}); err != nil {
			return err
		}

		if !p.Seat.Library {
			// Worth spelling out, because the shared library is now Steam's own
			// library folder rather than a second one next to it. Taking it away
			// takes the games out of this seat's Steam with it. Nothing is
			// deleted and turning it back on brings them back, but somebody
			// watching the log deserves to know why the list emptied.
			p.Log("the shared library is off for this seat, so its games are no longer in this seat's Steam")
		} else {
			p.Log("! the shared library is on for this seat but unavailable, see the daemon log")
		}

		return nil
	}

	if p.uid == 0 {
		if err := p.readUID(ctx); err != nil {
			return err
		}
	}

	// The directory is created on the host with the identifiers it will have
	// seen from inside. An unprivileged container stores files under mapped
	// identifiers, so a directory owned by root on the host belongs to nobody
	// in the seat and the player cannot write to it.
	hostUID, hostGID, err := p.Client.MapID(p.name(), p.uid, p.uid)
	if err != nil {
		return err
	}

	member := library.Member{
		Name:  p.Seat.Name,
		Owner: library.Owner{UID: int(hostUID), GID: int(hostGID)},
	}

	if err := p.Library.Ensure(member); err != nil {
		return err
	}

	source := p.Library.SeatRoot(p.Seat.Name)

	p.Log("mounting %s at %s", source, LibraryMount)

	if _, err := p.Client.Configure(ctx, p.name(), nil, map[string]map[string]string{
		libraryDevice: {
			"type":   "disk",
			"source": source,
			"path":   LibraryMount,
		},
	}); err != nil {
		return err
	}

	if err := p.mountSteamLibrary(ctx, source); err != nil {
		return err
	}

	return p.registerLibrary(ctx)
}

// adoptSteamApps takes over whatever Steam already keeps in its own library
// folder, so that mounting the pool onto it hides nothing.
//
// A seat that has run Steam always has something there, even before anybody
// installs a game: the Steam Controller configurations, sourcemods, workshop
// content. A seat somebody has been playing on can have entire games. The mount
// would make all of it invisible at once, so it moves into the pool first,
// where it becomes shared, which is where somebody who turned the shared
// library on wanted their games in the first place.
//
// Copied and then removed rather than moved: mv across a mount point is a copy
// and a delete anyway, and doing it in that order means a failure leaves the
// original where it was. The copy is a reflink where the filesystem allows it,
// which is the same filesystem the pool is on, so it costs no space.
//
// Nothing in the pool is overwritten. Where both sides have the same game, the
// pool's copy is the one the other seats already share, and the private one is
// at best an identical duplicate.
const adoptSteamApps = `set -e
mkdir -p "$2"
if [ -d "$1" ] && [ -n "$(ls -A "$1" 2>/dev/null)" ]; then
	du -sh "$1" | cut -f1
	cp -a -n --reflink=auto "$1/." "$2/"
	rm -rf "$1"
fi
mkdir -p "$1"
`

// mountSteamLibrary puts the pooled directory where Steam keeps its own games.
func (p *Provisioner) mountSteamLibrary(ctx context.Context, source string) error {
	pooled := source + "/steamapps"

	instance, _, err := p.Client.Instance(p.name())
	if err != nil {
		return err
	}

	if instance.Devices[steamLibraryDevice]["source"] == pooled {
		return nil
	}

	// Taking the files over needs the container running. This step is also
	// reached from the interface when somebody ticks the box on a stopped seat,
	// and mounting over files that have not been taken over yet would hide
	// somebody's games rather than share them. So it waits: the seat gets the
	// mount the next time it is provisioned, which is the next time it starts.
	status, err := p.Client.Status(p.name())
	if err != nil {
		return err
	}

	if status != "Running" {
		p.Log("the seat is not running, so Steam's library folder is left as it is for now")

		return nil
	}

	out, code, err := p.Client.Try(ctx, p.name(), "sudo", "-u", Player, "sh", "-c",
		adoptSteamApps, "sh", steamApps, LibraryMount+"/steamapps")
	if err != nil {
		return err
	}

	if code != 0 {
		return fmt.Errorf("taking over Steam's library folder: %s", strings.TrimSpace(out))
	}

	if moved := strings.TrimSpace(out); moved != "" {
		p.Log("moved %s from Steam's own library folder into the shared one", moved)
	}

	p.Log("mounting %s at %s, so the shared library is Steam's own", pooled, steamApps)

	_, err = p.Client.Configure(ctx, p.name(), nil, map[string]map[string]string{
		steamLibraryDevice: {
			"type":   "disk",
			"source": pooled,
			"path":   steamApps,
		},
	})

	return err
}

// registerLibrary sets up the half of the shared library that is not Steam's,
// and takes the old Steam entry back out.
//
// There is nothing left to register with Steam: the pooled directory is Steam's
// own library folder now, so it is found by being where Steam already looks.
// What is left to do is the opposite. Seats built before this have the pool
// registered a second time under /home/player/games, and that entry now reaches
// the same files by a second path, which shows every shared game twice.
func (p *Provisioner) registerLibrary(ctx context.Context) error {
	// Writing into the container needs the container. During provisioning it is
	// always running by this point, but this step is also reached from the
	// interface when somebody ticks the box on a stopped seat, and there the
	// device is all that can be done now. The next provisioning run finishes
	// the job.
	status, err := p.Client.Status(p.name())
	if err != nil {
		return err
	}

	if status != "Running" {
		p.Log("the seat is not running, so the shared directory is set up " +
			"the next time it is provisioned")

		return nil
	}

	if err := p.Client.MakeDir(p.name(), LibraryMount+"/steamapps", 0o755, p.uid, p.uid); err != nil {
		return err
	}

	// The launcher agnostic half. Steam gets a library folder it understands;
	// everything else gets a directory and a note, because there is no format
	// Heroic, Lutris, Bottles and a downloaded installer all agree on.
	if err := p.Client.MakeDir(p.name(), LibraryMount+"/shared", 0o755, p.uid, p.uid); err != nil {
		return err
	}

	readme := "Anything you put in this directory is shared with the other seats.\n" +
		"\n" +
		"One folder per game. Point Heroic, Lutris, Bottles or an installer at\n" +
		"this directory and the game appears in the other seats within a few\n" +
		"minutes, without being downloaded again. It works the other way too.\n" +
		"\n" +
		"Unlike the Steam library next to it, nothing here tells Polyseat when an\n" +
		"install has finished, so it waits until the folder has stopped changing\n" +
		"for a couple of minutes and treats it as done. A download that stalls\n" +
		"for longer than that can be picked up half complete.\n" +
		"\n" +
		"Delete a folder here and it will not be offered to this seat again.\n" +
		"The other seats keep their copies. Nothing here is a licence: a game\n" +
		"still has to be one you are allowed to run.\n"

	if err := p.Client.PushFile(p.name(), LibraryMount+"/shared/README.txt",
		[]byte(readme), 0o644, p.uid, p.uid); err != nil {
		return err
	}

	// The marker that used to make Steam treat this directory as a library
	// folder. It is removed rather than left: the directory is still there and
	// still shared, but it is no longer a Steam library, and a file claiming
	// otherwise is an invitation to add the same games a second time.
	if _, _, err := p.Client.Try(ctx, p.name(), "rm", "-f", LibraryMount+"/libraryfolder.vdf"); err != nil {
		return err
	}

	return p.unregisterOldLibrary(ctx)
}

// unregisterOldLibrary takes the shared library back out of Steam's own list of
// library folders, where earlier versions of Polyseat put it.
//
// Both files, because Steam keeps two. config/libraryfolders.vdf is the one it
// reads today; steamapps/libraryfolders.vdf is the older location, it is the
// one Polyseat used to write, and it now lives in the pooled directory itself.
// Leaving either would put the entry back the next time Steam reconciles them.
func (p *Provisioner) unregisterOldLibrary(ctx context.Context) error {
	for _, path := range []string{
		steamRoot + "/config/libraryfolders.vdf",
		steamApps + "/libraryfolders.vdf",
	} {
		existing, err := p.Client.ReadFile(p.name(), path)
		if err != nil {
			// No file, which is a seat where Steam has never run. Nothing to
			// take out, and Steam writes its own on first start.
			continue
		}

		dropped, changed := library.DropLibraryFolder(existing, LibraryMount)
		if !changed {
			continue
		}

		if err := p.Client.PushFile(p.name(), path, dropped, 0o644, p.uid, p.uid); err != nil {
			return err
		}

		// Steam reads these at startup, so a client running right now keeps the
		// old list until the session is restarted.
		p.Log("removed the second, now duplicate, library entry from %s", path)
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
		home + "/.config/waybar",
		home + "/.config/fuzzel",
		home + "/.config/sunshine",
		home + "/.config/systemd",
		home + "/.config/systemd/user",
		home + "/.config/systemd/user/polyseat-sunshine.service.d",
		home + "/.config/systemd/user/polyseat-sway.service.d",
		// Where AppImages live. Made here rather than when the first one
		// arrives, because it is also a place somebody is meant to find with a
		// file manager: an empty directory called Applications says what to do
		// with it, and a directory that appears only after the daemon has
		// already put something in it says nothing to anybody.
		home + "/Applications",
		// Where a browser saves, and therefore where the scan looks for an
		// AppImage to adopt. Firefox makes it on first use, which is too late:
		// the scan would look at a directory that does not exist for as long as
		// nobody had downloaded anything.
		home + "/Downloads",
	} {
		if err := p.Client.MakeDir(p.name(), dir, 0o755, p.uid, p.uid); err != nil {
			return err
		}
	}

	sway, err := render("assets/sway.config", map[string]string{"Resolution": p.Seat.Resolution})
	if err != nil {
		return err
	}

	bar, err := render("assets/waybar.config", map[string]string{"Seat": p.Seat.Name})
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
		{home + "/.config/waybar/config", bar, 0o644, p.uid},
		{home + "/.config/waybar/style.css", asset("assets/waybar.css"), 0o644, p.uid},
		{home + "/.config/fuzzel/fuzzel.ini", asset("assets/fuzzel.ini"), 0o644, p.uid},
		{home + "/.config/systemd/user/polyseat-sway.service", asset("assets/polyseat-sway.service"), 0o644, p.uid},
		{home + "/.config/systemd/user/polyseat-sunshine.service", asset("assets/polyseat-sunshine.service"), 0o644, p.uid},
		{"/usr/local/bin/polyseat-sunshine-run", asset("assets/sunshine-run.sh"), 0o755, 0},
		{"/usr/local/bin/polyseat-resize", asset("assets/resize.sh"), 0o755, 0},
		{"/usr/local/bin/polyseat-fps", asset("assets/fps.sh"), 0o755, 0},
		{"/usr/local/bin/polyseat-session", asset("assets/session.sh"), 0o755, 0},
		{"/usr/local/bin/polyseat-welcome", asset("assets/welcome.sh"), 0o755, 0},
		{"/usr/local/bin/polyseat-keyboard", asset("assets/keyboard.sh"), 0o755, 0},
		{"/usr/local/bin/polyseat-launcher", asset("assets/launcher.sh"), 0o755, 0},
		{"/usr/local/bin/polyseat-boxart", asset("assets/boxart.py"), 0o755, 0},
		{"/usr/local/bin/polyseat-bigpicture", asset("assets/bigpicture.sh"), 0o755, 0},
		{"/usr/local/bin/polyseat-pad-pointer", asset("assets/pad-pointer.py"), 0o755, 0},
	}

	for _, f := range files {
		if err := p.Client.PushFile(p.name(), f.path, f.content, f.mode, f.uid, f.uid); err != nil {
			return err
		}
	}

	if err := p.tidyLauncher(); err != nil {
		return err
	}

	// Forget which covers could not be found last time.
	//
	// polyseat-boxart remembers a title it could not find a picture for and
	// leaves it alone for a week, so that a game with no artwork anywhere is
	// not looked up every minute for the life of the seat. That is right while
	// the helper stays the same and wrong the moment it learns somewhere new to
	// look, which is exactly what a provisioning run is: a seat that had
	// recorded a miss would keep the blank card for another six days after the
	// fix arrived. Measured, not imagined: both seats here had the miss on file
	// for the one title this was fixed for.
	if _, _, err := p.Client.Try(ctx, p.name(), "sudo", "-u", Player, "sh", "-c",
		"rm -f /home/"+Player+"/.local/share/polyseat/art/*.none"); err != nil {
		return err
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

	// The same value for the session itself, so that everything started inside
	// the seat knows which seat it is in rather than only Sunshine. What made
	// this worth doing was seeing the first terminal in a seat greet somebody
	// with "Polyseat seat: unknown": sway inherited nothing, so neither did its
	// children.
	// The graphics vendor's variables, for the same reason: what they say
	// depends on the card in the host, so they cannot live in a unit file that
	// is the same everywhere. Both units get them, because sway has to render
	// on the card and Sunshine has to encode from it.
	gpuDropin := p.stack().dropIn()

	for _, unit := range []string{"polyseat-sunshine.service.d", "polyseat-sway.service.d"} {
		err = p.Client.PushFile(p.name(),
			home+"/.config/systemd/user/"+unit+"/10-seat.conf",
			[]byte(dropin), 0o644, p.uid, p.uid)
		if err != nil {
			return err
		}

		err = p.Client.PushFile(p.name(),
			home+"/.config/systemd/user/"+unit+"/20-gpu.conf",
			gpuDropin, 0o644, p.uid, p.uid)
		if err != nil {
			return err
		}
	}

	if err := p.WritePointerConfig(ctx); err != nil {
		return err
	}

	if err := p.writeLauncherDefaults(ctx); err != nil {
		return err
	}

	if _, err := p.WriteSunshineConfig(ctx); err != nil {
		return err
	}

	apps, _, err := p.WriteApps(ctx)
	if err != nil {
		return err
	}

	p.Log("Moonlight will offer: %s", strings.Join(apps, ", "))

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

// stepCredentials hands Sunshine the login the daemon chose for it.
//
// The daemon owns these rather than asking somebody to type them in, for the
// same reason it owns everything else generated here: it needs them itself.
// Pairing a device from one interface instead of one per seat means the daemon
// talking to each seat's Sunshine on the user's behalf, and it can only do that
// if it knows how to log in. The interface shows the password, so the seat's
// own Sunshine page stays reachable by hand.
// clutter is what the launcher shows that a seat has no use for.
//
// None of it is installed on purpose. Every one of these arrived as a
// dependency of something that is: gpsd brings xgps, v4l-utils brings the Qt
// capture utilities, hwloc brings lstopo, avahi brings three network browsers,
// and Thunar brings a rename tool and an about box. Twenty two entries, of
// which eight are worth having.
//
// That is not only untidy. The launcher shows a page at a time, sorted by
// name, so junk beginning with A through L pushed the software installer off
// the bottom of the list, and somebody looking for it concluded it was not
// installed.
//
// Sunshine is here for a different reason and the most important one. Its entry
// starts a second Sunshine, in a seat where one is already running as a
// service, which would fight the first one for the encoder and the virtual
// input devices.
var clutter = []string{
	"avahi-discover", "bssh", "bvnc",
	"foot-server", "footclient",
	"lstopo",
	"qv4l2", "qvidcap",
	"xgps", "xgpsspeed",
	"thunar-bulk-rename", "xfce4-about",
	"dev.lizardbyte.app.Sunshine",
}

// tidyLauncher hides those entries for the player.
//
// By writing a user entry of the same name rather than by touching what the
// packages installed. Hidden means "the user removed this" in the desktop entry
// specification, so menus drop it, and the next pacman -Syu does not undo it.
func (p *Provisioner) tidyLauncher() error {
	dir := "/home/" + Player + "/.local/share/applications"

	for _, name := range []string{
		"/home/" + Player + "/.local",
		"/home/" + Player + "/.local/share",
		dir,
	} {
		if err := p.Client.MakeDir(p.name(), name, 0o755, p.uid, p.uid); err != nil {
			return err
		}
	}

	entry := "[Desktop Entry]\nType=Application\nName=%s\nNoDisplay=true\nHidden=true\n"

	for _, name := range clutter {
		body := fmt.Sprintf(entry, name)

		err := p.Client.PushFile(p.name(), dir+"/"+name+".desktop",
			[]byte(body), 0o644, p.uid, p.uid)
		if err != nil {
			return err
		}
	}

	p.Log("hid %d launcher entries a seat has no use for", len(clutter))

	return nil
}

func (p *Provisioner) stepCredentials(ctx context.Context) error {
	if p.Secrets.SunshineUser == "" || p.Secrets.SunshinePassword == "" {
		return fmt.Errorf("no Sunshine credentials were prepared for this seat")
	}

	// Run as the player with HOME set: sunshine writes the credentials next to
	// its configuration, and as root it would write them into the wrong home
	// and leave the seat unable to read its own login.
	_, err := p.run(ctx, "sudo", "-u", Player, "env", "HOME=/home/"+Player,
		"sunshine", "--creds", p.Secrets.SunshineUser, p.Secrets.SunshinePassword)
	if err != nil {
		return err
	}

	p.Log("Sunshine login set to %s", p.Secrets.SunshineUser)

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
func (p *Provisioner) WriteSunshineConfig(ctx context.Context) ([]string, error) {
	if p.uid == 0 {
		if err := p.readUID(ctx); err != nil {
			return nil, err
		}
	}

	addresses, err := p.Client.Addresses(p.name())
	if err != nil {
		return nil, err
	}

	origins := OriginsFor(addresses)

	conf, err := render("assets/sunshine.conf", map[string]string{
		"Origins":    strings.Join(origins, ","),
		"Resolution": p.Seat.Resolution,
		"Encoder":    p.stack().encoder,
		"Adapter":    p.stack().adapter,
	})
	if err != nil {
		return nil, err
	}

	p.Log("allowed web origins: %s", strings.Join(origins, ", "))

	err = p.Client.PushFile(p.name(), SunshineConfigPath, conf, 0o644, p.uid, p.uid)

	return origins, err
}

// The gamepad pointer's speed, in screens per second at full deflection.
//
// The default came from use: 1100 pixels per second was reported as too fast,
// and tying it to the screen instead of to a pixel count was reported as still
// too sensitive on a 1440p phone. The bounds are there because both ends are
// useless, and the fast end is worse: a pointer that crosses the screen in a
// tenth of a second cannot be corrected with the stick that sent it there.
const (
	DefaultPointerSpeed = 0.45
	MinPointerSpeed     = 0.10
	MaxPointerSpeed     = 1.50
)

// writeLauncherDefaults points Lutris at the shared directory, so that installing
// a game there is the default rather than a choice somebody has to know about.
//
// Only when the file is not there. Provisioning runs again on every generation,
// and a player who has set their own default has that in exactly this file:
// replacing it would quietly undo their choice on the next update. A default is
// only a default until somebody disagrees with it.
//
// Lutris keeps this as `system.game_path` in `~/.config/lutris/system.yml`,
// which is `settings.CONFIG_DIR/system.yml` in its own source, read into a
// `{"system": {...}}` mapping. Its own default is `~/Games`, which is private to
// the seat and reaches nobody else.
func (p *Provisioner) writeLauncherDefaults(ctx context.Context) error {
	if p.uid == 0 {
		if err := p.readUID(ctx); err != nil {
			return err
		}
	}

	home := "/home/" + Player
	conf := home + "/.config/lutris/system.yml"

	script := fmt.Sprintf(`set -e
if [ -e %[1]s ]; then exit 0; fi
mkdir -p "$(dirname %[1]s)"
printf '# Written by Polyseat when this seat was built, and only when this file
' > %[1]s
printf '# did not exist. Your own setting wins: change it in Lutris and it stays.
' >> %[1]s
printf 'system:
  game_path: %[2]s
' >> %[1]s
`, conf, LibraryMount+"/shared")

	out, code, err := p.Client.Try(ctx, p.name(), "sudo", "-u", Player, "env",
		"HOME="+home, "sh", "-c", script)
	if err != nil {
		return err
	}

	if code != 0 {
		p.Log("! Lutris could not be pointed at the shared directory: %s", lastLines(out, 2))
	}

	return nil
}

// SessionPath is where a seat records the stream in progress.
const SessionPath = "/home/" + Player + "/.local/share/polyseat/session.json"

// PointerConfigPath is where the gamepad pointer helper looks for its speed.
const PointerConfigPath = "/home/" + Player + "/.config/polyseat/pointer.conf"

// WritePointerConfig puts the seat's pointer speed where the helper reads it.
//
// A file rather than an argument or an environment variable, because the helper
// runs for the whole session and this has to be changeable while somebody is
// holding the controller. It rereads the file when it changes, so moving the
// slider in the web interface is felt within a couple of seconds without
// restarting anything or provisioning again.
func (p *Provisioner) WritePointerConfig(ctx context.Context) error {
	if p.uid == 0 {
		if err := p.readUID(ctx); err != nil {
			return err
		}
	}

	speed := p.Seat.PointerSpeed
	if speed == 0 {
		speed = DefaultPointerSpeed
	}

	body := fmt.Sprintf(`# Generated by polyseatd from the seat's settings. Edits are overwritten.
#
# How much of the screen the gamepad pointer crosses in a second at full
# deflection. polyseat-pad-pointer rereads this while it runs.
speed=%.2f
`, speed)

	return p.Client.PushFile(p.name(), PointerConfigPath, []byte(body), 0o644, p.uid, p.uid)
}

// SunshineConfigPath is where a seat keeps the configuration the daemon writes.
const SunshineConfigPath = "/home/" + Player + "/.config/sunshine/sunshine.conf"

// OriginsFor turns a seat's addresses into the origins Sunshine has to accept.
//
// Both interfaces, because the seat's own page is legitimately reached over
// either: the LAN address from a browser, the bridge address from the daemon.
// Sorted, so that two runs over the same addresses produce the same list and
// the daemon does not see a change where there is none.
func OriginsFor(addresses map[string][]string) []string {
	var origins []string

	for _, addrs := range addresses {
		for _, addr := range addrs {
			origins = append(origins, fmt.Sprintf("https://%s:%d", addr, sunshine.Port))
		}
	}

	sort.Strings(origins)

	return origins
}

// ParseOrigins reads the origins back out of a seat's configuration, so a
// daemon that has adopted a running seat knows what its Sunshine was started
// with rather than assuming.
//
// Anchored at the start of the line, and comments skipped. That is not
// pedantry: the generated file explains itself in a comment that contains the
// words csrf_allowed_origins, and the first version of this matched that
// comment before the setting and parsed the prose after it as an address. Every
// adopted seat then looked like its address had moved.
func ParseOrigins(conf []byte) []string {
	for _, line := range strings.Split(string(conf), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}

		rest, found := strings.CutPrefix(line, "csrf_allowed_origins")
		if !found {
			continue
		}

		value, found := strings.CutPrefix(strings.TrimSpace(rest), "=")
		if !found {
			continue
		}

		value = strings.TrimSpace(value)
		if value == "" {
			return nil
		}

		origins := strings.Split(value, ",")
		for i := range origins {
			origins[i] = strings.TrimSpace(origins[i])
		}

		sort.Strings(origins)

		return origins
	}

	return nil
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
