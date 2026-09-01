# Polyseat

**Several people playing on one Linux PC at the same time.** Each in their own
session with their own Steam account, their own controller and their own screen,
streamed to their own Moonlight client. The machine's regular desktop keeps
running undisturbed while they play.

A **seat** is a container with its own desktop, its own Steam account and its
own Sunshine, streamed to one Moonlight client. One graphics card serves all of
them. Polyseat implements neither a compositor, nor an encoder, nor a streaming
protocol: Incus, Sway, Sunshine and PipeWire do that work, and Polyseat is the
orchestrator on top. It builds the seats, wires them up collision-free, sends
each person's input to the right one, shares the game library, and repairs what
drifts.

![Polyseat: one PC runs the daemon, the input broker and the shared game library; each seat is a container with headless sway and Sunshine, streaming to its own Moonlight client](docs/overview.svg)

## What the machine needs

- **Arch, Debian or Fedora**, or anything based on one of those. The host
  scripts work out which from `/etc/os-release` and speak `pacman`, `apt` or
  `dnf` accordingly. This binds only the *host*: a seat is an Arch container on
  every one of the three, so which distribution the machine runs changes nothing
  about the seats or the games in them.

  **Arch is the only one that has been run on real hardware.** Debian and
  Fedora are new in 0.9.0 and are covered by tests that replace the package
  manager with a stub, which proves the right commands are issued and not that
  they do what is expected. If you are on one of those, you are the first:
  [docs/installation.md](docs/installation.md) says exactly what was verified
  and what was not.
- **An NVIDIA or AMD card with a working driver.** NVIDIA also needs the 32 bit
  half of that driver's userspace, or 32 bit games will not find the GPU — and
  Steam's own client is one of them. The installer works out what that package
  is called here rather than naming it, since all three distributions name it
  differently, and tells you the command. AMD needs only `amdgpu` on the host.

  The installer refuses rather than warns if the driver does not answer, because
  a seat without it comes up, streams in software and looks perfectly healthy.
  The AMD path has **never been run on real hardware**:
  [docs/amd.md](docs/amd.md) says what was verified and what was not.
- **An ethernet port for the seats.** Each seat is a host of its own on the LAN,
  and Wi-Fi cannot do that: 802.11 carries one MAC address per station. The
  machine itself is free to stay on wifi — what the seats want from the card is
  a segment to be hosts on, not the way to the internet, so a cable in a second
  port is enough and a USB adapter counts. Only a wifi-only machine is out:
  [docs/installation.md](docs/installation.md) says why.
- **btrfs, or XFS with `reflink=1`** — but only for the shared game library. On
  ext4 the seats still work, the sharing simply stays off and every seat
  downloads its own games.

The packages Polyseat itself needs, the installer works out and installs. Two
of them are not in every distribution's own repositories — Incus is in Debian 13
and not in 12, and `nvidia-container-toolkit` is in neither — so on those the
installer says where they come from and stops, rather than adding a repository
on your behalf. Which repositories a machine trusts is not an installer's
decision.

## Getting started

**1. Install it, once per machine.**

Arch, and anything based on it:

```
curl -LO https://github.com/superuser404notfound/Polyseat/releases/latest/download/polyseat-x86_64.pkg.tar.zst
sudo pacman -U polyseat-x86_64.pkg.tar.zst
sudo systemctl enable --now polyseatd
```

Debian, Ubuntu and the rest:

```
curl -LO https://github.com/superuser404notfound/Polyseat/releases/latest/download/polyseat_amd64.deb
sudo apt install ./polyseat_amd64.deb
sudo systemctl enable --now polyseatd
```

Fedora:

```
curl -LO https://github.com/superuser404notfound/Polyseat/releases/latest/download/polyseat.x86_64.rpm
sudo dnf install ./polyseat.x86_64.rpm
sudo systemctl enable --now polyseatd
```

Downloaded first and installed second, because these packages carry no signature
yet and every one of the three treats a file it fetched itself more strictly
than one already on disk. `./` in front of the filename on the last two is not
decoration: without it apt and dnf read the name as a package to go and look for.

None of the three links has a version in it and none ever will: each is always
the newest release, and `pacman -Qi polyseat`, `apt show polyseat` or
`rpm -qi polyseat` says which one arrived.

That is the whole of the command line. The machine itself is not ready yet, and
that part happens in the page: a package is not allowed to do it, and the daemon
is. Preparing it and removing Polyseat again are both buttons.

Not in the AUR, and anything that turns up there under this name is not this.
Who does what, and why it is split this way:
[docs/installation.md](docs/installation.md). Building a particular commit from
a checkout instead: [CONTRIBUTING.md](CONTRIBUTING.md).

**2. Open `https://<this machine>:47800` and choose a password.** Nobody has
claimed the machine yet, so the page asks you to set one rather than to type
one. Do it before anybody else does: until it is set, whoever reaches the page
can set it. The browser will ask about the certificate once, exactly as
Sunshine's own interface makes it ask.

The interface answers on the whole network, so seats can be managed from the
same phone that runs Moonlight. To keep it on this machine only, set `listen`
to `127.0.0.1:47800` in `/etc/polyseat/polyseatd.json`.

On a host with a desktop of its own there is a **Polyseat** entry in the
application menu as well, placed by the installer and by all three packages. It
opens the same page at `https://localhost:47800`, so the port is one fewer thing
to have written down.

**3. Press *Prepare this host*.** It is the first thing on the page, above
the seats, until this machine has one. The button installs the missing packages,
writes the idmap range every container start needs, brings Incus up and
initialises it, checks that the graphics driver answers, and puts your account
in the `input` group. It reports each step as it happens, and every step checks
before it changes anything, so pressing it again on a machine that is already
ready changes nothing — which is also how to check that it is.

Afterwards it moves under *Host*, at the top of the page, along with removing
Polyseat and asking GitHub for a newer version.

`sudo polyseat-prepare` is the same thing from a terminal, down to the same
file, if you would rather watch it there. Either way, Polyseat restarts into its
own interface a few seconds after Incus comes up, and the page follows it.

**4. Add a seat and press provision.** The daemon does the rest: image,
packages, driver, Steam, desktop, session. It takes a few minutes and the card
shows each step as it happens.

**5. Pair a device.** Open *Devices and pairing* on the seat's card, point
Moonlight at the address shown at the top of that card, and type the PIN
Moonlight gives you into that field. The same panel lists what is already
paired and can unpair it. Every seat is paired here, rather than in a Sunshine
page of its own.

That is all. Repeat 4 and 5 for the second person.

## Updating

The interface says when a newer version is out and offers two buttons: one
installs it, one restarts the daemon. Installing is safe in the middle of
somebody's game, because replacing the binary leaves the running process alone.
The restart is what makes the new version take effect, and it is refused while
somebody is streaming and says whose game it would have ended.

By hand it is the same two steps, with whichever of the three files this
machine installed from:

```
curl -LO https://github.com/superuser404notfound/Polyseat/releases/latest/download/polyseat-x86_64.pkg.tar.zst
sudo pacman -U polyseat-x86_64.pkg.tar.zst
sudo systemctl restart polyseatd
```

```
curl -LO https://github.com/superuser404notfound/Polyseat/releases/latest/download/polyseat_amd64.deb
sudo apt install ./polyseat_amd64.deb
sudo systemctl restart polyseatd
```

```
curl -LO https://github.com/superuser404notfound/Polyseat/releases/latest/download/polyseat.x86_64.rpm
sudo dnf install ./polyseat.x86_64.rpm
sudo systemctl restart polyseatd
```

**Seats are not touched by an update.** If a new version builds them
differently, the interface names the ones that are behind and offers one button
to bring them up to date, at a moment you pick rather than in the middle of
somebody's game.

Three things in the interface reach root, and this is one: installing a release,
preparing the machine, and removing Polyseat. `"web_update": false` in
`/etc/polyseat/polyseatd.json` turns off the first two, `"web_uninstall": false`
turns off the third, and `"update_needs_password": true` makes the first two ask
for the interface password when they are pressed. Removing asks for it either
way.

What the update never does is let the browser say what to install: it takes the
release the daemon found itself, from this project's own downloads, and checks
it against the checksum that release states.

The check for a new version is one request to GitHub every six hours, it sends
nothing about the machine, and it installs nothing on its own. *Host* has a
button that asks now rather than waiting for the next one, and says when the
daemon last managed to ask, because "nothing newer" and "nothing heard" are
different answers. `"update_check": false` turns all of it off. [`CHANGELOG.md`](CHANGELOG.md) is where
the differences between versions are written down, and the version actually
serving is at the bottom of the interface.

## Removing it

*Host* in the interface, then **Remove Polyseat**. It asks for the password
again, because it is the one button here that pressing a second time does not
undo. At a terminal it is the same file:

```
sudo polyseat-uninstall
```

**Your seats stay**, unless the box that says otherwise is ticked. This takes out
the daemon, its unit, the udev rule, the helpers and the package, and touches
neither the containers nor `/var/lib/polyseat`. Install it again and the seats
come back as they were.

**To take the seats with it**, tick *Delete the seats as well*, or
`sudo polyseat-uninstall --seats`. The shared game library is kept even then,
because the seats' copies come back from it in a second by sharing blocks where
downloading them again does not; `--library`, or the second box, removes that
too.

The daemon is stopped before anything it owns is touched, and that order is the
reason this is a command rather than a paragraph: deleting a container while the
daemon is still supervising it is what leaves Incus with a stop that never
finishes.

Incus, bpftrace and python stay where they are. They are not Polyseat's to
remove, which is why the package goes with `pacman -R` rather than `-Rns` on
Arch: on a machine where pacman pulled Incus in as a dependency, the `s` would
take somebody's container manager away. `apt` leaves dependencies alone by
default and `dnf` does not, so on Fedora the same restraint is asked for
outright rather than assumed.

The interface stops answering a moment in, because the daemon serving it is the
first thing to stop. That is what it looks like when it worked, and the rest of
the run is in the journal: `journalctl -u polyseat-uninstall`.

## What it does

**Seats are built in one click and play in parallel.** `polyseatd` takes a seat
from nothing to a running session: container, network, driver, Steam, desktop,
input. It also brings seats that were built by an older version forward, with
one button that updates every one of them.

**Input goes exactly where it belongs.** Each client's keyboard, mouse and
gamepad reach that client's session and nothing else. No crossover between
seats, and the host desktop never sees any of them: a controller plugged in for
seat 2 does not steer the host's Steam. Which seat a device belongs to comes
from what the kernel says created it, not from the name the device claims.

**A game installed once is playable in every seat**, without being downloaded
again, and that includes the games that were already on the machine. Nothing has
to be chosen for it: the shared library is the only library a seat's Steam has,
so signing in and pressing install is enough. Each seat keeps its own fully
writable copy, and the copies share their blocks on disk, so taking this
machine's 69 GB library into the pool took 0.8 seconds and 432 KB. It keeps
working after a game updates, and a seat that is behind is brought forward as
soon as nothing in it is using the files.

**Launchers other than Steam work too.** Each seat has a `shared/` directory
where one folder is one game; point Heroic, Lutris or Bottles at it and the game
appears in the other seats by itself.

**Every seat carries Proton CachyOS** alongside Valve's own, set as the default,
keeping itself up to date, and waiting for a seat that is neither streaming nor
using the files before it replaces anything.

**A seat can share the network with the host, or stay behind a line.** Local
multiplayer between the host and a seat needs the first, which is a button in
the interface under "The uplink", or `sudo polyseat-lan-bridge` at a terminal;
whether a particular seat takes part is a checkbox on its card. Turned off, the seat reaches the gateway and the other
seats, but not this machine, and this machine not it.

**A seat is something you can sit down in front of.** Connecting lands on a
desktop with a launcher, a bar and a file manager, not on a bare terminal.
Moonlight's app list is generated from what the seat really has, with box art,
so Steam Big Picture and the installed games are one pick away before a stream
even starts. Software goes in from either end, with no password and no root: the
player types `flatpak install ...` in the seat, or somebody installs it into
that seat from the web interface and watches the progress bar. **AppImages count
too**, which matters because many emulators are published that way and no other:
paste the address into the web interface, or drop the file in `~/Downloads`
inside the seat, and it appears in both menus by itself.

**Files go in the same way.** A save, a set of mods, keys, a ROM, an emulator
somebody already downloaded: drop the file or the whole folder on the seat's
card in the web interface and it lands in the player's `~/Downloads` inside the
seat, keeping the folder it came in. That is the one direction nothing else here
covered, and the answers before it were a network share to set up on both ends
or a trip through somebody's cloud, for files sitting on the same disk as the
daemon. From `~/Downloads` the player moves them where they belong with the file
manager the seat already has, and an AppImage does not even need that.

**Every client gets the picture it asked for.** The seat's screen is virtual, so
it simply becomes the size and refresh rate the client wants. The framerate is
capped from outside rather than by turning vsync on, so games stay uncapped and
pay no vsync latency, and one setting covers native games, Proton, flatpaks and
emulators alike. Measured in a seat: 14866 fps uncapped becomes 60.00 fps with a
60 Hz client, at 0.03 ms of frametime jitter against 0.40 ms for vsync alone.

**A controller is enough.** Streaming from an Apple TV or a phone means no
keyboard and no mouse, so the seat carries both: an on-screen keyboard, and a
pointer driven by the gamepad, left stick to move and right stick to scroll. It
turns itself on when the desktop is in front and hands the controller back to a
fullscreen game; holding two of Select, Start and Guide for a second overrides
that by hand, and the pad buzzes to say it took.

Everything above was confirmed on real hardware, on one machine: an Arch host
with an RTX 4080, most recently on 2026-07-31. The logs of each step live in
[`spike/`](spike/). Whether any of it holds on a machine that is not that one is
the open question this project would most like answered, and there are now three
places it is open: an AMD card ([docs/amd.md](docs/amd.md)), a Debian host and a
Fedora host. The last two are new in 0.9.0 and, like the first, are reasoned
about and tested as far as they can be tested without the hardware — which is
not the same as working.

## Day to day

**Who is on which seat** is on the seat's own card while somebody is streaming:
what they picked, at what size and framerate, from which address, and since
when.

**Each seat card carries its own log.** Two lines in it are worth watching. The
input broker says, for every device, whether its owner was really established or
only claimed:

```
+ event29    Keyboard passthrough (seat2)
  creator verified: ID_INPUT=1 ID_INPUT_KEY=1 ID_INPUT_KEYBOARD=1
! event260   refused: name claims (seat1) but the kernel says 'seat2' created it
```

The other is the encoder. A seat that ended up encoding on the CPU still starts,
still streams and still looks healthy, so the card shows `nvenc` or `vaapi` when
it is right and says so plainly when it is not.

For the host itself:

```
sudo polyseatd -report      # everything about this installation, in one go
polyseat-check-hardening    # console and device exposures
journalctl -fu polyseatd
```

**`polyseatd -report` is what to put in a bug report.** Version, distribution,
kernel, card and driver, Incus, the filesystem, the network, every seat, and the
last 200 journal lines. It runs without the daemon, which is the point: it is
wanted most on a machine where the daemon will not start. It reads and changes
nothing and opens no password or key, but it does carry this machine's host
name, its seat names and their private addresses, and says so at the top. Read
it before pasting it somewhere public.

## What it does not do

It does not share licences: a seat can only play what its own account owns. It
is built for trusted local users on one machine, not for handing a seat to a
stranger over the internet. And it does not hide a missing reflink filesystem
behind full copies: the daemon says so plainly instead.

**Anti-cheat is the ordinary Linux answer, not a worse one.** A seat is a
container and not a virtual machine: it shares the host's kernel, so there is no
hypervisor for anything to notice. What is left is the situation every Linux
player already has, where a game with kernel-level anti-cheat does not run at
all and one whose anti-cheat works under Proton should behave in a seat as it
does on the desktop. Should, because no anti-cheat title has been run here.
Nobody has been banned for playing in a seat as far as this project knows, and
nobody has confirmed the opposite either, so treat it as untested rather than as
safe. The one thing a seat does differently is that keyboard, mouse and pad are
virtual devices, and that is true of any Sunshine and Moonlight setup rather
than something Polyseat adds.

## Reading further

Architecture and the reasoning behind every decision:
[`docs/architecture.md`](docs/architecture.md). What the isolation actually
guarantees, measured rather than assumed:
[`docs/security.md`](docs/security.md). Who installs what:
[`docs/installation.md`](docs/installation.md). What changed between versions:
[`CHANGELOG.md`](CHANGELOG.md).

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
