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

- **Arch, or something based on it.** The installer speaks `pacman` rather than
  pretending to be portable.
- **An NVIDIA or AMD card with a working driver.** NVIDIA also needs
  `lib32-nvidia-utils`, or 32 bit games will not find the GPU. AMD needs only
  `amdgpu` on the host. The installer refuses rather than warns if the driver
  does not answer, because a seat without it comes up, streams in software and
  looks perfectly healthy. The AMD path has **never been run on real hardware**:
  [docs/amd.md](docs/amd.md) says what was verified and what was not.
- **A wired network connection.** Each seat is a host of its own on the LAN, and
  Wi-Fi cannot do that: it carries one MAC address per connection.
- **btrfs, or XFS with `reflink=1`** — but only for the shared game library. On
  ext4 the seats still work, the sharing simply stays off and every seat
  downloads its own games.

The packages Polyseat itself needs, the installer works out and installs.

## Getting started

**1. Install it, once per machine.**

```
curl -LO https://github.com/superuser404notfound/Polyseat/releases/latest/download/polyseat-x86_64.pkg.tar.zst
sudo pacman -U polyseat-x86_64.pkg.tar.zst
sudo polyseat-prepare
sudo systemctl enable --now polyseatd
```

Downloaded first and installed second, because `pacman -U` on a URL wants a
signature that these packages do not carry yet. The link has no version in it
and never will: it is always the newest release, and `pacman -Qi polyseat` says
which one arrived.

`polyseat-prepare` is a separate command because a package is not allowed to do
what it does: install the missing packages, bring Incus up, check the driver,
and put your account in the `input` group. Both commands can be run again over
themselves without undoing anything.

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

**3. Add a seat and press provision.** The daemon does the rest: image,
packages, driver, Steam, desktop, session. It takes a few minutes and the card
shows each step as it happens.

**4. Pair a device.** Open *Devices and pairing* on the seat's card, point
Moonlight at the address shown at the top of that card, and type the PIN
Moonlight gives you into that field. The same panel lists what is already
paired and can unpair it. Every seat is paired here, rather than in a Sunshine
page of its own.

That is all. Repeat 3 and 4 for the second person.

## Updating

The interface says when a newer version is out and offers two buttons: one
installs it, one restarts the daemon. Installing is safe in the middle of
somebody's game, because replacing the binary leaves the running process alone.
The restart is what makes the new version take effect, and it is refused while
somebody is streaming and says whose game it would have ended.

By hand it is the same two steps:

```
curl -LO https://github.com/superuser404notfound/Polyseat/releases/latest/download/polyseat-x86_64.pkg.tar.zst
sudo pacman -U polyseat-x86_64.pkg.tar.zst
sudo systemctl restart polyseatd
```

**Seats are not touched by an update.** If a new version builds them
differently, the interface names the ones that are behind and offers one button
to bring them up to date, at a moment you pick rather than in the middle of
somebody's game.

That update button is the one thing in the interface that reaches root.
`"web_update": false` in `/etc/polyseat/polyseatd.json` turns it off, and
`"update_needs_password": true` makes it ask for the interface password when it
is pressed. What it never does is let the browser say what to install: it takes
the release the daemon found itself, from this project's own downloads, and
checks it against the checksum that release states.

The check for a new version is one request to GitHub every six hours, it sends
nothing about the machine, and it installs nothing on its own.
`"update_check": false` turns it off. [`CHANGELOG.md`](CHANGELOG.md) is where
the differences between versions are written down, and the version actually
serving is at the bottom of the interface.

## Removing it

```
sudo systemctl stop polyseatd
sudo pacman -Rns polyseat
```

**Your seats stay.** This takes out the daemon, its unit, the udev rule and the
helpers, and touches neither the containers nor `/var/lib/polyseat`. Install it
again and the seats come back as they were.

Stopping the daemon first is yours to do, because removing a package does not
end a process that is already running.

**To take the seats with it**, use `host/install.sh --purge` from a checkout. It
asks first, stops the daemon before touching anything, and keeps the shared game
library so the games do not have to be downloaded again; `--library` removes
that too. Incus and the other packages are not Polyseat's to remove and stay
where they are.

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
multiplayer between the host and a seat needs the first, which is what
`polyseat-lan-bridge` sets up; whether a particular seat takes part is a
checkbox on its card. Turned off, the seat reaches the gateway and the other
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
the open question this project would most like answered, and
[docs/amd.md](docs/amd.md) is where it is most open.

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
