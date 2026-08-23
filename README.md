# Polyseat

**Several people playing on one Linux PC at the same time.** Each in their own
isolated session with their own Steam account, their own controller and their
own screen, streamed to their own Moonlight client. The machine's regular
desktop keeps running undisturbed while they play.

A **seat** is an Incus system container with its own headless Sway, its own
Sunshine instance, its own PipeWire and its own Steam account. One GPU serves
all of them, through NVENC on NVIDIA and VA-API on AMD. Polyseat implements
neither a compositor, nor an
encoder, nor a streaming protocol: the heavy lifting is done by Incus,
Sway/wlroots, Sunshine, PipeWire, udev and systemd, and Polyseat is the
orchestrator on top. It builds seats, wires them up collision-free, assigns
input devices, shares the game library, and repairs what drifts.

![Polyseat: one PC runs the daemon, the input broker and the shared game library; each seat is a container with headless sway and Sunshine, streaming to its own Moonlight client](docs/overview.svg)

## Getting started

**What the machine needs:**

| | |
|---|---|
| **Host** | Arch-based. The installer queries `pacman` rather than pretending to be portable. |
| **Packages** | `incus`, `bpftrace`, `python`, `go`, plus `nvidia-container-toolkit` on NVIDIA. The installer works out which card is in the machine first and installs whichever of them are missing. |
| **GPU** | **NVIDIA**, with the driver installed and answering. `nvidia-utils` carries the two libraries NVENC needs, `libcuda.so.1` and `libnvidia-encode.so.1`, and the container toolkit injects them into every seat. `lib32-nvidia-utils` as well, or 32 bit games will not find the GPU. The `cuda` package is the toolkit and is **not** needed.<br><br>**AMD** works differently and is simpler: `amdgpu` on the host is the whole requirement, and Mesa goes into each seat as an ordinary package, so no host driver update can leave a seat behind. Encoding is VA-API, the only hardware path AMD has on Linux. **This path has never been run on real hardware**, see [docs/amd.md](docs/amd.md) for what was verified and what was not.<br><br>Either way the installer refuses rather than warns if the driver is missing, because a seat without it comes up, streams in software and looks perfectly healthy. |
| **Filesystem** | btrfs, or XFS created with `reflink=1`, **and only for the shared game library**. `ext4` cannot share blocks, and neither can tmpfs or a network filesystem. Seats still work on those; the shared library simply stays off and every seat downloads its own games. The installer tests it and says which it found. |
| **Network** | One wired interface, so each seat is a host of its own on the LAN and can use the standard Sunshine ports. Seats take a macvlan from it, or a port on it where it is a bridge, which is what `host/lan-bridge.sh` makes it and what local multiplayer between the host and a seat needs. Wireless cannot do either: 802.11 carries one MAC address per association. |

**1. Install it, once per machine.** There are two ways in and the package is
the shorter one. It is attached to every release:

```
sudo pacman -U https://github.com/superuser404notfound/Polyseat/releases/download/v0.3.2/polyseat-0.3.2-1-x86_64.pkg.tar.zst
sudo polyseat-prepare
sudo systemctl enable --now polyseatd
```

`polyseat-prepare` is a second command because a package is not allowed to be
one command. It may place files; it may not initialise Incus, write to
`/etc/subuid` or put your account in a group. So that half is a command you run
once, and it says what it finds either way and changes nothing that is already
right.

Or from a checkout, which does both halves in one go and is what to use to run a
particular commit rather than a release, which is what testing on unusual
hardware means:

```
git clone --branch v0.3.2 https://github.com/superuser404notfound/Polyseat.git
cd Polyseat
sudo host/install.sh
sudo systemctl enable --now polyseatd
```

A tag rather than `main`, because `main` is where the next version is being
written and a machine that streams to other people is not the place to find out
what changed today. Leave the `--branch` off to follow it anyway.

Not the AUR. New accounts cannot be registered at present, so there is nothing
to publish from, and `paru -S polyseat` finds nothing: anything that does turn
up under that name is not this. It matters less than it sounds, because the AUR
distributes recipes rather than packages and installing from one means building
it yourself, which is what the checkout above already does.

The install does everything in one go: missing packages, the idmap range that
every container start needs, bringing Incus up and initialising it if nobody
has, checking that the graphics driver really answers, loading the `uhid` module
so the observer has something to watch from the first boot onwards, putting your
account in the `input` group, and then it builds the daemon and places the input
helpers under `/usr/local/lib/polyseat`, the udev rule that keeps seat devices
off the host desktop, and one systemd unit. It creates no seat. It can be run
again after an update without undoing anything, which is what *Updating* further
down is.

The half of that a package would not be allowed to do lives in
`host/prepare.sh`, which `install.sh` runs for you and which the package would
install as `polyseat-prepare`. The split is real work and it stays, because the
package is finished and only unpublished.

Two things it reports rather than changes, because they are yours to decide:
whether the filesystem holding the game library can share blocks, which it asks
the filesystem by cloning a file rather than trusting its name, and whether
there is a wired interface for the seats to take a macvlan from.

**2. Open `https://<this machine>:47800` and choose a password.** Nobody has
claimed the machine yet, so the page asks you to set one rather than to type
one, the way Sunshine's own interface does. Do it before anybody else does:
until it is set, whoever reaches the page can set it.

The interface answers on the whole network, so seats can be managed from the
same phone that runs Moonlight. The certificate is self signed, so the browser
asks once, exactly like Sunshine's own interface does. To keep it on this
machine instead, set `listen` to `127.0.0.1:47800` in
`/etc/polyseat/polyseatd.json`.

**3. Add a seat and press provision.** The daemon downloads the image, installs
the packages, repairs the NVIDIA userspace that the driver injection leaves
incomplete (nothing to repair on AMD, where Mesa arrives as a package),
generates the Sunshine configuration and starts the session. It takes a few
minutes and the card shows each step as it happens.

**4. Pair a device.** Open *Devices and pairing* on the seat's card, point
Moonlight at the address shown at the top of that card, and type the PIN
Moonlight gives you into that field. The same panel lists what is already
paired, can unpair it, and shows the seat's own Sunshine login for when you want
to go there directly. Pairing happens here for every seat rather than in one
Sunshine page per seat on a port of its own: the daemon owns each seat's
Sunshine login and talks to it on your behalf.

That is all. Repeat 3 and 4 for the second person.

## What it does

**Seats are built in one click and play in parallel.** `polyseatd` takes a seat
from nothing to a running session: container, network, driver userspace, Steam,
desktop, input broker. It supervises the brokers, follows the Incus event stream
instead of polling, and converges seats that were built by an older recipe, so a
change to the recipe reaches the seats that already exist, with one button that
brings every one of them up to date.

**Input goes exactly where it belongs.** Each client's keyboard, mouse and
gamepad reach that client's session and nothing else. No crossover between
seats, and the host desktop never sees any of them: a controller plugged in for
seat 2 does not steer the host's Steam. Ownership is established structurally,
from what the kernel says created a device, rather than from the name the device
claims to have.

**A game installed once is playable in every seat**, without being downloaded
again, and that includes the games that were already on the machine. Nothing has
to be chosen for this to happen: the shared library is the only library a seat's
Steam has, so signing in and pressing install puts the game where the other
seats can reach it. Each seat keeps its own private, fully writable copy; the
daemon replicates game directories between them with reflinks, so the copies
share their blocks on disk. Taking this machine's 69 GB library into the pool took 0.8 seconds and
cost 432 KB. The host's own Steam library is found and taken into the pool
without being asked, as long as there is exactly one of them and it can share
blocks with the pool; two of them is a choice with consequences and stays a
question. It keeps working after an update, because that library is watched
rather than imported once, and a seat that is behind is brought forward as soon
as nothing in it is using the shared files.

**Launchers other than Steam work too.** Each seat has a `shared/` directory
where one folder is one game; point Heroic, Lutris or Bottles at it and the game
appears in the other seats by itself.

**Every seat carries Proton CachyOS** alongside Valve's own, from the project's
GitHub releases rather than from a package repository, so a seat stays a plain
Arch container that trusts no extra source. It is set as the seat's default
compatibility tool, it keeps itself up to date, and it waits for a seat that is
neither streaming nor holding the files open before it replaces anything.

**A seat can be on the same network segment as the host, or behind a line.**
Seats are hosts of their own on the LAN either way. `host/lan-bridge.sh` turns
the uplink into a bridge, which is what local multiplayer between the host and a
seat needs, since those games find each other by broadcasting and a macvlan
cannot hear its own parent. Whether a particular seat takes part in that is a
checkbox on its card: turned off it goes back behind the line, reaching the
gateway and the other seats but not this machine, and this machine not reaching
it.

**A seat is something you can sit down in front of.** Connecting lands on a
desktop with an application launcher, a bar and a file manager, not on a bare
terminal. Moonlight's app list is generated from what the seat really has, with
box art, so Steam Big Picture, any installed launcher and the installed games
themselves are one pick away before a stream even starts. The desktop's own
launcher is generated from the same scan, so a game is in both menus without
anybody making a shortcut for it. Software goes in from
either end with no password and no root: the player types `flatpak install ...`
in the seat, or somebody installs it into that seat from the Polyseat web
interface and watches the progress bar. **AppImages count as software here too**,
which matters because a good many emulators are published that way and no other:
paste the address into the web interface, or download the file inside the seat
with Firefox and leave it in `~/Downloads`, and either way it lands in
`~/Applications` and appears in both menus by itself.

**Every client gets the picture it asked for.** The seat's output is virtual, so
it simply becomes the size the client wants, at the refresh rate the client
wants. The framerate is capped from outside rather than by turning vsync on,
which is what RTSS does on Windows: the games stay uncapped and pay no vsync
latency, and one setting covers native games, Proton, flatpak launchers and
emulators alike. Measured in a seat: 14866 fps uncapped becomes 60.00 fps with a
60 Hz client, at 0.03 ms of frametime jitter against 0.40 ms for vsync alone.

**A controller is enough.** Streaming from an Apple TV or a phone means no
keyboard and no mouse, and neither Moonlight nor Steam can supply them for a
launcher's login form. So the seat carries both: an on-screen keyboard, and a
pointer driven by the gamepad, left stick to move and right stick to scroll,
with the buttons where the desktop pad tools put them: A clicks, X right clicks,
B is Escape, Y and a short press of Start are Enter. It turns itself on when the
desktop is in front and hands the controller back to a fullscreen game, and
holding two of Select, Start and Guide for a second overrides that by hand - the
pad buzzes to say it took. How fast it moves is a slider on the seat's card that
takes effect while somebody is holding the controller.

Confirmed on real hardware, most recently on 2026-07-31. The logs of each step
live in [`spike/`](spike/) and record what works, what does not, and why.

## Day to day

**Who is on which seat** is on the seat's own card while somebody is streaming:
what they picked, at what size and framerate, from which address, and since
when.

**Reading what happened.** Each seat card carries its own log, and the useful
lines in it come from the input broker. It says for every device whether its
owner was verified structurally, correlated, or merely claimed by name:

```
+ event29    Keyboard passthrough (seat2)
  creator verified: ID_INPUT=1 ID_INPUT_KEY=1 ID_INPUT_KEYBOARD=1
! event260   refused: name claims (seat1) but the kernel says 'seat2' created it
```

The other line worth watching is the encoder. A seat whose EGL landed on
software rendering still starts, still streams and still looks healthy; it just
encodes on the CPU. The card shows `nvenc` or `vaapi` and the codecs it can
offer when it is right, and says so plainly when it is not. Which card the
whole machine was built for is in the header, beside the host name.

For the host itself:

```
sudo polyseatd -report      # everything about this installation, in one go
host/check-hardening.sh     # console and device exposures
journalctl -fu polyseatd
```

**`polyseatd -report` is what to put in a bug report.** Version, distribution,
kernel, card and driver, Incus, whether the library filesystem can really share
blocks, the uplink and whether it is a bridge, every seat and which recipe built
it, and the last 200 journal lines. It runs without the daemon, which is the
point: it is wanted most on a machine where the daemon will not start. It reads
and changes nothing, and it opens no password, key or certificate. It does carry
this machine's host name, its seat names and their private addresses, and it
says so at the top, so read it before pasting it somewhere public.

**Updating.** The interface says when a newer version has been published. Taking
it depends on which way it went in.

From the package, the new one over the old one and then a restart at a moment
you choose:

```
sudo pacman -U https://github.com/superuser404notfound/Polyseat/releases/download/v0.3.2/polyseat-0.3.2-1-x86_64.pkg.tar.zst
sudo systemctl restart polyseatd
```

The restart is separate on purpose, and pacman will say so too. Replacing a
binary does not disturb the process already using it, so the new version is on
disk and the old one is still serving until you say otherwise. Nobody's game
ends in the middle.

From a checkout it is one command:

```
sudo host/update.sh          # --check to look without changing anything
```

It refuses a checkout with uncommitted work in it, and it waits for a moment
when nobody is streaming, because installing restarts the daemon and that takes
every seat's input broker with it. `--now` skips the waiting and `--tag v0.1.0`
goes to a particular release, older ones included.

By hand is the same thing and stays supported:

```
git fetch --tags
git checkout v0.3.2
sudo host/install.sh
```

Either way the daemon is rebuilt and restarted, which is the step that makes an
update take effect at all: building a new binary does not disturb the process
using the old one. Seats that exist are not touched. If the new version builds
them differently the interface names the ones that are behind and offers one
button to bring them up to date, at a moment you pick rather than in the middle
of somebody's game.

Which version is actually serving is at the bottom of the interface, in
`polyseatd -version`, and in the journal at every start. It is the tag it was
built from, so a build from an untagged commit says so and is told about no
updates: there is nothing sensible to compare it with.

The check itself is one request to GitHub every six hours, it sends nothing
about the machine, and it never installs anything. `"update_check": false` in
`/etc/polyseat/polyseatd.json` turns it off.
[`CHANGELOG.md`](CHANGELOG.md) is where the differences between versions are
written down.

**Removing it** leaves your seats alone. From the package that is
`sudo pacman -Rns polyseat`, and from a checkout
`sudo host/install.sh --uninstall`. Either takes out the daemon, its unit, the
udev rule and the helpers, and touches neither the containers nor
`/var/lib/polyseat`. Install it again and the seats come back as they were.

Neither stops the daemon for you, and pacman says so on the way out. A removed
package is a binary that is no longer on disk, not a process that has ended, so
`sudo systemctl stop polyseatd` is yours to give when the seats are not in use.

To take the seats with it, `sudo host/install.sh --purge`, which asks first and
keeps the shared game library so the games do not have to be downloaded again;
add `--library` to remove that too. It stops the daemon before touching anything
it owns, which is the point of having it: deleting a container while the daemon
is still reading inside it leaves Incus with a stop that never finishes. Neither
command removes the packages or Incus, which are not Polyseat's to remove.

## What it does not do

It does not share licences: a seat can only play what its own account owns. It
is built for trusted local users on one machine, not for handing a seat to a
stranger over the internet. And it does not hide a missing reflink filesystem
behind full copies: the daemon says so plainly instead.

## Reading further

Architecture and the reasoning behind every decision:
[`docs/architecture.md`](docs/architecture.md). What the isolation actually
guarantees, measured rather than assumed: [`docs/security.md`](docs/security.md).
Who installs what, and where the line between installer, daemon and interface
runs: [`docs/installation.md`](docs/installation.md). What changed between
versions: [`CHANGELOG.md`](CHANGELOG.md).

**Running it on hardware that is not the author's is the most useful thing
anybody can do for this project**, and reporting back either way is the point.
How, and what the code and tests here look like:
[`CONTRIBUTING.md`](CONTRIBUTING.md). Reporting a security problem privately,
and what is already known and deliberately accepted:
[`SECURITY.md`](SECURITY.md).

## Roadmap

| | | |
|---|---|---|
| **M0** | Input spike: does the container architecture hold up? | ✅ |
| **M1** | One seat: Sway + Sunshine + NVENC + Moonlight | ✅ |
| **M2** | Input broker: keyboard, mouse and pad reach the seat | ✅ |
| **M3** | A seat that actually plays: Steam, Proton, pad, audio | ✅ |
| **M4** | Two seats in parallel, input strictly separated | ✅ |
| **M5** | Daemon + GUI: create, start, pair and monitor seats | ✅ |
| **M6** | Shared game library: install once, play in every seat | ✅ |
| **M7** | A usable seat: desktop, app list, software, resolution and framerate per client | ✅ |

## License

GPL-3.0-or-later. See [LICENSE](LICENSE).
