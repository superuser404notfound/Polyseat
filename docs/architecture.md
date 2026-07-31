# Architecture

This document records **what** is being built and, more importantly, **why it is
built this way** - so that later decisions do not run against insights that have
already been paid for.

## Constraints

Almost everything else follows from these:

- **Everyone plays on a Moonlight client**, nobody sits at the host's console.
  There are therefore no physical controllers on the host that would need
  assigning - only the virtual pads Sunshine creates inside each seat.
- **The host desktop keeps running normally** and must not be disturbed by any
  seat. Which desktop it is does not matter: a seat brings its own compositor
  and its own `/dev/input`, so the only thing the host desktop is asked for is
  to not pick up the devices a seat creates, and that is decided one layer below
  it, in udev and logind, which every desktop shares. Developed on KDE/Wayland.
- **Fixed seats per person.** No dynamic pool: Anna has her seat, it always has
  the same address, she sets up Moonlight once.
- **Seats run permanently** (idle ≈ 400 MB). On-demand start is a later feature,
  not a design constraint.
- **SDR**, no HDR. On Linux/wlroots/NVIDIA, HDR is the most expensive wish on
  the list and buys nothing for the start.
- **N seats**, not a fixed two. Realistically the hardware caps this at 2-3
  actively playing seats (see Capacity).

## Why containers - and why Incus

A seat needs its own `$HOME` (Steam's single-instance lock, separate accounts),
its own audio, its own session. One Unix user per seat would solve that.
Containers additionally solve what a user does not: a **private, empty
`/dev/input`**. Isolation then arises structurally instead of through udev rules
fighting a globally visible device tree.

Incus rather than Podman or systemd-nspawn, for three concrete reasons:

1. **`unix-char` with `required=false` supports hotplug into running
   containers.** That is exactly what the input broker needs: client connects →
   pad appears → node must go into the *running* seat. Podman cannot add devices
   to running containers. That alone decides it.
2. **`nvidia.runtime=true`** injects the host's driver libraries via
   libnvidia-container. On a rolling release this is essential - otherwise the
   container userspace drifts against the host kernel module after every
   `nvidia-utils` update. With nspawn you would have to rebuild
   libnvidia-container by hand. On AMD the key is set to `false` and the `gpu`
   device alone is the whole arrangement: Mesa is a package inside the seat,
   nothing crosses the boundary but the render node, and there is no version to
   keep in step. See [amd.md](amd.md), which is honest about not having been
   run on a real AMD card.
3. **System containers** bring their own systemd, their own users, their own
   PipeWire. A seat *is* a small machine instead of simulating one. On top of
   that, `limits.cpu` / `limits.memory` per seat and a btrfs storage pool.

VMs with GPU passthrough are out: a single consumer GPU cannot be meaningfully
split across several VMs.

## What the constraints eliminate

Because nobody sits at the host and nobody plugs in physical pads:

- **No broker for physical devices.** There are only virtual pads.
- **No audio passthrough.** PipeWire runs entirely inside the container; sound
  leaves it only as a stream. No `/dev/snd` in the container, no fights over the
  default sink, host audio structurally untouched.
- **Host peripherals are unreachable**, because they are never mapped into a
  container.

That leaves exactly one problem on the host: the seats' virtual pads must not
show up on the KDE desktop.

## The input chain - the core risk

`uinput` is **not namespaced**. A pad that Sunshine creates in seat 3 is
registered globally by the kernel; the host's udev creates the node in the host
devtmpfs. The container has a minimal `/dev`, so nothing happens there at first
- that is the isolation we want, but it also means the node has to be handed
back actively.

The chain has two halves:

**Half 1 - get the node into the right seat.**

1. **Seat tag in the device name.** No patching required: Sunshine reads
   `XDG_SEAT` and appends the seat name itself as soon as the seat is not
   `seat0` - "Keyboard passthrough" becomes "Keyboard passthrough (seat1)".
2. **Host udev rule** matches the tag, hides the device from KDE/libinput and
   notifies the broker.
3. **Broker** runs `incus config device add <seat> padN unix-char …` and
   `remove` on disconnect.

That is the chain as it was designed, and the name is no longer what decides
step 2. It turned out to be both forgeable and incomplete, and what replaced it
is structural: the creating process is read from the kernel, through
`UI_GET_SYSNAME` for uinput and a kprobe on uhid, the observer the daemon
supervises alongside each broker. The name survives only as a fast path in the
udev rule, which closes the exposure window for the devices it does know.
Measured, including both forgery attempts refused, in
[`security.md`](security.md).

**Half 2 - enumeration inside the container.** Incus containers have no working
udev. The node is there, but Steam and SDL *enumerate* gamepads through libudev
rather than by scanning `/dev/input`. Ways out: `SDL_JOYSTICK_DISABLE_UDEV=1`,
and/or a fake-udev shim that intercepts libudev calls. Wolf (games-on-whales)
has a component for exactly this problem - the concept is reusable even without
adopting Wolf as a product.

**This is why it is the very first spike.** If half 2 does not hold, the
container architecture collapses and we end up with one Unix user per seat.

### Result of M0 (2026-07-27): it holds

Measured, not assumed - log in [`spike/m0-input/README.md`](../spike/m0-input/README.md).

- A pad created inside the container appears on the host, while inside the
  container `/dev/input` does not exist at all. The isolation really does arise
  structurally.
- `unix-char` hotplug gets the node into the running container.
- SDL recognises the pad there as a controller.
- A udev rule on `ATTRS{name}=="polyseat:*"` reliably keeps the pads off the
  host desktop.

**One condition:** a pad attached *while* a process is already running goes
unnoticed - libudev enumerates via `/sys` (visible in the container), but the
udev monitor hangs off netlink uevents (which never reach the container). With
`SDL_JOYSTICK_DISABLE_UDEV=1` SDL polls `/dev/input` directly and notices
hotplug reliably. **That variable therefore belongs in every seat's
environment.** A fake-udev shim is not needed for SDL - whether Steam, with its
own bundled SDL and Steam Input, behaves the same way is the first open question
of M1.

Note that Sunshine does not create gamepads through uinput but through
**`/dev/uhid`** (via inputtino). Both devices therefore belong in every seat -
without uhid, keyboard and mouse appear normally but a pad never does.

## Layout

```
┌─ Host: CachyOS, KDE desktop keeps running ───────────────┐
│                                                          │
│  polyseatd  - Go, one systemd unit, runs as root         │
│   ├─ HTTPS/JSON API + server sent events, password       │
│   │    protected, self signed certificate                │
│   ├─ Incus Go client  → create/start/configure seats,    │
│   │                      lifecycle events instead of     │
│   │                      polling                         │
│   ├─ Provisioner      → the whole seat recipe, idempotent│
│   ├─ Supervisor       → one input broker per running     │
│   │                      seat, one uhid observer         │
│   └─ Web interface    → embedded in the binary           │
│                                                          │
└──────────────────────────────────────────────────────────┘
              │ Incus API
   ┌──────────┼──────────┬───────────┐
 seat:rooky  seat:anna  seat:guest  …
 (each: headless Sway + Sunshine + PipeWire + Steam)
```

Per seat: headless Sway (`WLR_BACKENDS=headless`, `LIBSEAT_BACKEND=noop`) as the
session shell, because Sunshine can capture there via
`wlr-screencopy`/`export-dmabuf` - KMS capture is dead on the proprietary NVIDIA
driver, and on AMD it wants `cap_sys_admin` on the Sunshine binary, which is not
something a container is going to be given. Same setting on both vendors, for
two different reasons. Optionally gamescope nested per game for scaling and FPS caps.

**Split frame encoding is turned on rather than left to the driver.** Cards from
the RTX 4080 up carry two NVENC units and Sunshine can spread one frame across
both, which by its own description significantly reduces host processing latency
for a marginal loss of compression. The driver's own rule only does it at 4K and
above, which is the wrong rule for a seat: a client at 1080p or 1440p gets the
same benefit and would otherwise leave half the encoder idle. A card with one
unit ignores the setting, and it is written only into a seat built for NVENC.

## GUI instead of CLI

The heart is a **daemon with an API**; the GUI is a client of it. A web UI, not
a native one: seats should be configurable from the couch or from a phone, Go is
strong at HTTP and weak at native GUIs, and Sunshine itself works the same way.
Wrapping it in a native window later (Wails) stays possible without splitting
the codebase.

It answers on the whole network rather than on localhost, because the point is
to manage seats from the same phone that runs Moonlight. That is only defensible
with a password and TLS in front of it, so it has both; what they are and what
they are worth is in [`security.md`](security.md).

The most important UX goal: **one interface for all seats.** Without it you
juggle N Sunshine web UIs on N ports with N pairing dialogs.

That is met. The daemon sets each seat's Sunshine login while provisioning it,
which is what lets it drive that seat's own API on the user's behalf: submitting
a PIN, listing paired devices, unpairing one. It uses the same calls Sunshine's
own page makes, read out of the bundle it ships.

Two things about that path are worth writing down. It goes over the **Incus
bridge, never the LAN address**. A seat on a macvlan cannot talk to its own
host, so the address Moonlight uses produces a timeout that looks like Sunshine
being down. On a bridged uplink that particular obstacle is gone, and the path
stays the same anyway: which of the two arrangements a seat is in is a setting,
and a daemon that reached its seats one way here and another way there would
work until somebody ticked a box. And the seat's
Sunshine password is **generated once and kept**, because paired devices are
stored against it and a rebuilt container has to come back with the same one.

There is no CLI at all, not even a thin one. The daemon takes three flags and
none of them operate anything: `-config`, `-listen`, `-version`. A second way in
would mean a second author for the generated files, which is exactly what the
next section forbids. When the interface will not start, the thing to read is
`journalctl -u polyseatd`.

## Principle: the daemon owns the configuration

Incus profiles, Sunshine configs, udev rules and systemd units are **generated
artifacts, never inputs**. Edit them by hand and you lose the change on the next
write - in exchange, the state is always explainable and reproducible. Without
this rule, GUI-centred management inevitably drifts out of sync.

## How the daemon is built

Four decisions worth writing down, each of them made by something that went
wrong first.

**Events, never polling.** The daemon learns what containers are doing from the
Incus lifecycle stream. It polls only *inside* a container it knows is running,
every ten seconds, to read what the session is doing, and never while a seat is
stopping. The M2 broker prototype polled `incus exec` twice a second regardless
of state; an exec landed inside a shutdown and the Incus daemon hung in
"Stopping instance" with the container already dead.

The first version of the event handler reacted to every lifecycle event, and
Incus emits one for every exec. Each read of a seat caused the next read: a
hundred events in ten seconds, all of them the daemon watching itself. Only the
four actions that change what a seat is get through now.

**One owner for the seat lifecycle.** The broker and the uhid observer used to
be systemd units. That put the lifecycle in two places: systemd knew when a
broker should run, the daemon knew when a seat was up, and neither could see the
other. The daemon supervises both as child processes now, which is what lets it
stop a broker *before* the container it talks to, rather than hoping the
ordering works out.

**Provisioning is a list of idempotent steps.** Not a script that runs once, but
a recipe that converges. Running it against a seat that already exists is the
normal case, and it is also how a seat built by hand comes under the daemon: the
daemon adopts an existing container rather than refusing it. A generation number
marks seats built by an older recipe, which is the direct answer to the drift
found at the end of M4, where `seat1` carried `security.nesting` and `seat2` did
not for no better reason than the order they were built in.

**The session is started by the daemon, not by the container.** The session
units exist but are deliberately not enabled. If a seat brought itself up when
its container booted, Sunshine would read a configuration written before the
seat had an address, and its allowed web origins are derived from exactly that
address. Starting an already running seat is a no-op that only makes sure the
broker is there, so restarting the daemon never interrupts a game.

## Library pool

A game installed once is available in every seat, without being downloaded
again. That includes games that were already on the host before Polyseat
existed.

**The mechanism is reflink, not sharing.** Every seat has its own private, fully
writable Steam library; the daemon replicates game directories between them with
the `FICLONE` ioctl, which copies metadata and leaves the data blocks shared.
Measured on this machine: importing the host's 69 GB library into the pool took
0.8 seconds and cost 432 KB. `filefrag` shows the pool's copy and the original
at the same physical offsets with the `shared` flag on both.

An earlier plan here was one writable snapshot per seat. That was wrong for the
goal, and the note is worth keeping: snapshots diverge. A seat installing a game
would keep it to itself, which is the opposite of what the pool is for.

Mounting one directory into every seat was rejected too, for three reasons that
are each fatal alone:

- Two Steam clients writing one `steamapps` corrupt it, and no lock reaches
  across containers.
- A read-only shared library makes Steam refuse to update and say so constantly.
- OverlayFS copies a whole file up on first write, so patching a 60 GB game
  costs 60 GB per seat.

With reflinks none of that applies, because at the POSIX level nothing is
shared. Each Steam sees an ordinary library it owns outright. Copies diverge
only when a seat updates a game, and then only by the changed blocks.

**Taking part is per seat**, because this is the only place that mounts host
storage into a seat. A new seat has it on, since somebody adding a second seat
to a machine that already has games is asking for exactly this; a seat built
before M6 keeps what it had, which is off. Ticking the box applies straight away:
the disk device is hotplugged and the games are cloned in within seconds, no
provisioning run needed. Turning it off takes the mount away again, and since
the shared directory is Steam's own library folder rather than a second one,
that takes the shared games out of that seat's Steam with it. Nothing is
deleted and turning it back on brings them back.

**Layout.** Under `library_dir`, `pool/steamapps/` is the canonical copy and
`seats/<name>/` is the one directory mounted into that seat. It arrives twice:
`seats/<name>/steamapps` at `/home/player/.local/share/Steam/steamapps`, which
is Steam's own library folder and the subject of the next section, and
`seats/<name>` at `/home/player/games`, which carries `shared/` for the other
launchers and is what the app list is generated from. Both are the same files by
two paths, which is why the idle probe below looks for both. The rest of Steam's
directory, the client itself and the per account data, stays inside the
container. `compatdata/`, `shadercache/` and `downloading/` are Steam's to put
in its library folder, so they land in `seats/<name>/` on the host, which is a
directory per seat and is never taken into the pool: only game directories and
their manifests are.

**What the daemon does, every minute.** Anything fully installed in a seat and
quiet for two minutes is taken into the pool; anything in the pool is offered to
every seat that does not have it. `StateFlags` must read exactly 4, which is
what keeps a half finished download from being shared. `LastOwner` and
`LastPlayed` are cleared on the way, `InstalledDepots` is not: that block is
what lets the receiving Steam conclude the files are current rather than
download them again.

It never deletes. A title uninstalled inside a seat is remembered as declined
rather than restored on the next pass, which is the difference between a feature
and something that keeps putting games back on a disk somebody was clearing.

**Only ever forward.** Build ids are compared as numbers, and the pool takes a
copy only when it has none or when the library offering it is strictly newer.
The first version compared for inequality in either direction, so a seat one
patch behind quietly overwrote the pool's newer copy and handed that older build
to everybody else. Comparing as text has the same shape of bug: build 9 sorts
after build 10.

**Updates propagate rather than drift.** A seat whose copy is behind the pool is
brought forward, but only when nothing in that seat is using the shared library.
That is asked of the seat directly: a `/proc` walk looks for either mount
path in any process's mappings, open descriptors or working directory, because
`lsof` is not in a seat and a game that is running has its files open. Either
path, because a Steam game running out of the shared library has it open as
Steam's own `steamapps` and never mentions `/home/player/games` at all. A seat that is busy
keeps its copy and the waiting update is reported, so the interface shows
something pending instead of nothing happening. Overwriting a game under a
running client corrupts an install rather than improving one.

**That walk is three commands, and it used to be fifteen hundred.** The first
version looped over `/proc` in the shell: a `grep` per process and a `readlink`
per open descriptor. Measured on an idle seat with 58 processes and 1369 open
descriptors, that came to 990 ms of the seat's own CPU, once a minute, for a
question whose answer is almost always no, and it was spent while somebody was
streaming out of that seat. One `grep` over every `maps` file at once and two
`find -lname` passes answer the same three questions in 14 ms on the same seat.

**The host's own library is watched, not imported once.** An imported library is
remembered and re-read on every pass. Without that, a game the host updates
afterwards never reaches the seats and every one of them downloads that update
for itself.

**And it is adopted rather than waited for.** The pool works between seats from
the first day, so a host whose own games never join looks like a working
installation, while the games already downloaded stay downloaded twice. Every
pass therefore looks for a Steam library on the host and takes it, subject to
four conditions that are all about not making a decision somebody would have to
undo:

- Nothing is adopted once a library is tracked. The pass runs every minute, so
  the automatic choice is a choice made once, at the point where nobody has
  answered the question yet.
- A library somebody stopped watching is never taken back. Without the note in
  `state.json` a removal would be undone a minute later, forever, and the person
  removing it could not win.
- Exactly one candidate, or none is taken. The first library tracked is the one
  games from the seats are cloned into, so with two of them the choice decides
  whose Steam directory the daemon writes into.
- The candidate has to share blocks with the pool, measured with a 4 KiB probe
  rather than inferred. `FICLONE` returns `EXDEV` across filesystems and the
  clone falls back to a byte copy, so a machine that adopted a library on
  another disk by itself would duplicate it in full, quietly, on a timer. The
  device number would be the wrong test: on btrfs every subvolume has its own
  and clones across them work.

The refusals are logged once each rather than every minute, and the interface
names any library on the host that the pool is not watching, because "the daemon
looked at this and held back" is otherwise indistinguishable from "the daemon
never looked".

**The host is a member, not only a source.** It appears in the pool under the
name `host`, which no seat may be called, and the traffic goes both ways: games
it has are taken into the pool, and games installed in a seat are cloned into
its Steam library. It used to be a source and nothing else, and the effect was
that a game installed in a seat could only be played on the host by downloading
it a second time, which is the exact cost this exists to avoid. Everything a
seat gets applies to it unchanged: the manifest is neutralised on the way in so
the host's Steam claims the copy for whoever is signed in there, uninstalling on
the host is remembered as a refusal instead of being undone on the next pass,
and an update waits while something is using the files. What answers that last
question here is a walk of `/proc` in the daemon rather than a shell fragment in
a container, restricted to processes belonging to the owner of the library,
which is what keeps it cheap on a host that also runs every seat's processes.

The daemon still never writes into the other libraries it watches. Only one can
receive, because the same game cloned into two folders of one Steam client is
installed twice as far as that client is concerned and it has no good way to
decide which copy is real. The interface names the one that does.

A title cloned in while the host's Steam is running is not noticed until it
starts again. Steam reads the manifests in its library folder at startup;
nothing tells it to look now, and the alternative, only ever cloning while Steam
is closed, would mean a seat's new game never arrives on a machine somebody
leaves Steam open on.

## Where a game installs by default

A seat used to offer two Steam library folders, its own private one and the
pooled one labelled Polyseat, and only the second reached the other seats. Which
one the install dialog preselected was therefore the difference between a game
everybody can play and a game that stays where it was put, and it preselected
the wrong one. What follows is why the obvious repairs do not work, and what is
done instead.

What Steam keeps is `LastInstallFolderIndex`, directly under
`UserLocalConfigStore` in `userdata/<account>/config/localconfig.vdf`, the index
being the folder's position in `libraryfolders.vdf`. That was established by
setting it once by hand and seeing which file changed, because it is in none of
the places worth guessing: not `libraryfolders.vdf`, not `config.vdf`, not
`registry.vdf`, and the string appears in none of Steam's binaries in a seat.

**Writing it is not a solution**, which is why nothing here does. The file belongs
to an account that does not exist until somebody signs in, and by then Steam is
running and holds it in memory, writing it out only when it exits. So the earliest
a written value can take effect is the second time Steam starts, and somebody who
signs in and installs straight away, which is what people do, gets the wrong
library anyway.

Listing the shared folder first, so that the fallback of index 0 points at it,
does not work either. Both halves were measured in a seat by reading the star in
Steam's own storage settings: with the key removed Steam does fall back to index
0, and it also rewrites `libraryfolders.vdf` at every start and puts its own
directory back at the front. A swap survives exactly until the next launch.

**What is done is to stop having two libraries.** The seat's pooled directory is
mounted at Steam's own `steamapps`, so the one library folder Steam has is the
shared one, from the moment the seat is created. There is no default to set and
nothing to choose, and it holds for every account that ever signs in to that
seat rather than for the one whose `localconfig.vdf` was written.

Measured in a seat afterwards: `config/libraryfolders.vdf` lists exactly one
folder, `/home/player/.local/share/Steam`, with all the pooled games under it,
and Steam leaves it at one across restarts even though `/home/player/games`
is still mounted and still full of the same files.

Two things this has to carry. Steam's own `steamapps` is never quite empty, even
before anybody installs anything: it holds the Steam Controller configurations,
`sourcemods` and `workshop`, 1.3 MB on a seat that had just been built, and a
seat somebody has been playing on can have whole games there. Mounting over it
would hide all of that at once, so provisioning moves it into the pool first,
by reflink, without overwriting anything the pool already has. And seats built
before this have the pool registered a second time under `/home/player/games`;
that entry now reaches the same files by a second path and would show every
shared game twice, so it is taken back out of both files Steam keeps it in, with
the remaining folders renumbered so that one somebody added themselves is not
silently lost.

The cost is that taking part in the pool stops being a switch that can be turned
off without the games going with it. Turning it off leaves that seat's Steam
empty until it is turned back on. Nothing is deleted, and it is the honest
reading of what the switch now means.

Lutris has no such problem. Its `game_path` in `~/.config/lutris/system.yml` is
written when a seat is built, before anybody signs in to anything, and only when
that file does not exist yet.

## Launchers other than Steam

Steam hands the pool a completion signal and a version number. No other launcher
offers anything comparable and there is no format they agree on, so inventing a
manifest for them would mean inventing a standard nobody writes to.

What every launcher does produce is a directory. So each seat has a second
place, `/home/player/games/shared/`, where one folder is one game: put a game
there and it reaches the other seats, and the daemon never needs to know which
launcher made it. Point Heroic, Lutris, Bottles or a downloaded installer at it.

The two signals Steam gives are replaced by facts read off the tree. Finished
becomes "nothing in it has changed for a couple of minutes", which is honest but
weaker: a download that stalls for longer than that can be picked up half
complete, and there is no way to tell from outside. Version becomes the newest
modification time inside the tree, which is why cloning preserves file times.
Without that a copy would always look newer than its own original and the two
would carry each other back and forth forever.

**This needs a filesystem that can share blocks.** btrfs and XFS created with
`reflink=1` can; ZFS only through block cloning in OpenZFS 2.2 and only at
dataset granularity; ext4 cannot at all. The daemon probes by cloning a real
block at startup rather than trusting the filesystem's name, and refuses to open
a pool where that fails. Refusing is deliberate: a pool that quietly made full
copies would fill the disk and only announce itself once there was no room left
to fix it in.

The licensing reality remains, and no amount of this touches it: the files being
present does not give a seat's Steam account the right to run them. Where the
account owns the game, Steam finds the files, validates and plays without
downloading. Where it does not, Steam refuses. The saving is real for the two
common cases, two people who both own a game and one account signed in on
several seats.

## What a seat looks like from the inside

For a long time a seat was sway with one terminal in it. That streamed
perfectly and was close to unusable: there was no way to start a launcher that
was not already in the Sunshine app list, and no way to install one either,
because the player has no sudo. Three things fixed it, and they are deliberately
separate because they answer to different clients.

**The app list is the menu.** `apps.json` is generated by the daemon rather than
left as the file Sunshine ships. This is the only menu a client without a
keyboard has, so it is the one that has to be right: it is navigable with a
gamepad before a stream starts, and it holds Desktop, Steam Big Picture and an
entry for every launcher the seat actually has. It is rebuilt on every start,
because nothing in a seat tells the daemon that somebody installed something an
hour ago. Entries added by hand through Sunshine's own interface are kept; the
stock `Low Res Desktop`, which runs `xrandr` against an HDMI output no headless
container has, is dropped.

**The games are in the list too**, not only the launchers. Picking a launcher
in Moonlight means waiting for it to start and then steering through its
interface with a thumbstick; picking the game means picking the game. Steam's
own manifests say what is installed and Lutris will print a list, so both are
read, with their artwork where they have it. Steam's tools are kept out by
name, and only by name: the manifest has no field that says tool, `LastOwner`
would have been one except that the library pool zeroes it when it clones a
title, and `DownloadType` does not separate them. So the list is narrow and
biased towards showing, because a tool in a menu is a wasted line and a hidden
game is somebody unable to play with no way to find out why.

That scan is on its own minute long timer rather than the ten second sweep,
because asking Lutris means starting Lutris, and nobody needs to learn within
ten seconds that a game was uninstalled.

**Files are not an account**, and forgetting that made the list actively
misleading. The shared library puts a game into every seat that takes part, so
a seat where nobody had ever signed in to Steam was offering its neighbour's
games in Moonlight, where picking one did nothing at all. Steam's titles are
only offered where Steam has an account, which is what the per user directory
and the account list say. This is narrower than ownership, which is not
knowable from outside Steam, and it removes the case that is certainly wrong.

Artwork is fetched for a title the seat has no cover for, since a seat only
caches what it has displayed in Steam and the library delivers games nobody
has opened there. The plain address answers 404 for a good many titles, whose
covers are published under a directory named after a hash instead, and the hash
is in nothing a seat has; Steam's own store service will hand it over, without a
key. What is missing after all that is remembered for a week rather than asked
for every minute, and a provisioning run forgets those, because a helper that
has learned a new place to look must not sit out the week on last week's answer.

Each card is named after the artwork it was drawn from rather than after the
title. Covers arrive late by nature, and while a card kept its name a better
picture left the app list looking unchanged: nothing told Sunshine to reload, so
the client kept the picture it had cached, and the file on disk was right while
the screen was wrong.

**The desktop's own launcher gets the same games**, from the same scan and with
the same cards as icons. That is a second menu, for whoever is already streaming
the desktop, and it was showing Steam, Firefox and a file manager while the
installed games were nowhere: a desktop entry for a game exists only when
somebody asks Steam or Lutris for a shortcut. Where somebody has, theirs is left
alone and Polyseat writes nothing, because that launcher lists every entry it
finds and two files would be two rows. Matched on what the entry starts rather
than on what it is called: the two are written by different hands and only the
game underneath is the same. Remove their shortcut and the generated one is back
within the minute.

**Sunshine reads that file once**, when it starts, for the list it serves to
clients. Its web interface rereads it on every request, and asking that one
instead is how writing the file was taken for the whole of the update: every
check the daemon made agreed with the file while Moonlight went on showing what
Sunshine had loaded hours earlier. A game uninstalled in a seat stayed in the
list until the seat restarted.

Writing an app through Sunshine's own API does make it reload, and it reloads
the file rather than trusting what it holds, so posting an entry back unchanged
says "read that again" without altering anything. That is what the daemon does
after every write, in preference to restarting Sunshine, which would drop
whatever somebody is streaming. Sunshine then rewrites the file in an
arrangement of its own, which is why the daemon compares these lists by meaning
rather than byte for byte: otherwise it would find a difference on every pass
and write the same list back for ever.

**Every entry Polyseat generates is marked as its own**, which took two goes to
get right. Keeping entries it did not recognise looked like politeness towards
somebody who had added an app through Sunshine's interface, and it made removal
impossible: an uninstalled game stops being generated, so it stops being
recognised, so it was preserved as somebody's handiwork and stayed in the list
forever. A file written before the marker existed is converged once by keeping
nothing, since nothing in it says who wrote what.

**The desktop is for everything else.** sway with an application launcher, a
bar, a file manager and stock sway keybindings, and a first terminal that prints
the keys and the install commands instead of a prompt on its own. Somebody who
knows sway has nothing to learn; somebody who does not is told.

**Software goes in from either end.** `flatpak --user` needs no privileges at
all, which is what makes it the right mechanism here rather than a convenient
one: the player installs into their own home with no password, and the daemon's
install button in the web interface runs the same command as the same user, so
there is one list of installed software and not two. Flathub is added per user
for the same reason. The cost is one setuid bwrap per seat, and why that is the
smaller cost is set out in [`security.md`](security.md).

Three routes, because they answer to different people. `gnome-software` is in
the seat for whoever is sitting in it, browsing Flathub with pictures and a
search field; it costs almost nothing to add because a seat already has the
toolkit underneath it, where bazaar would have been 52 MB and discover 212 MB.
The web interface is for setting a seat up for somebody before handing it over.
The command line is for neither and stays anyway.

**AppImages are the other kind, and they need a different mechanism because
they have no index.** There is no Flathub for them: an AppImage is one file on
somebody's release page, and the only thing that knows it exists is the
directory it was put in. So the seat side is a directory listing rather than a
package manager. `~/Applications` is what is listed, `~/Downloads` is swept into
it once a minute, and each file's name and icon are read out of the file itself
with `--appimage-extract`, cached against its size and modification time so that
a scan costs a listing rather than an unpack. From there they join the same path
as everything else and become entries in Moonlight and in the seat's launcher.

Three facts about this were measured in a seat rather than assumed. The
container already has `/dev/fuse` and a setuid `fusermount3`, so nothing about
the container had to change; what was missing was **fuse2**, because the
AppImage runtime dlopens `libfuse.so.2` by name and a seat with only fuse3
stopped every classic AppImage at `dlopen(): error loading libfuse.so.2`.
`--appimage-extract` needs no FUSE at all, which is why reading the metadata
works even where running the thing would not. And `curl -#` draws its progress
bar on standard error with no terminal attached, so unlike flatpak, whose
progress needs a pseudo terminal, a download reports itself for free.

The magic bytes are checked before anything else happens to a file, in both the
daemon and the scan. The scan **runs** each file to read its metadata, so a
shell script somebody renamed to `.AppImage` would otherwise be a program the
daemon starts once a minute; the check is what keeps the two apart, and a test
asserts it by failing loudly when the payload runs.

A **sandbox has to be told about the shared library**, and that was found by
trying it rather than by reading a manifest. M6 said a launcher other than
Steam could share games through the seat's library directory, and for a flatpak
launcher it quietly was not so: Heroic may touch `~/Games/Heroic`, `~/.steam`
and `/mnt`, and the library is none of them, so it reported the directory as
not existing. Everything about the sharing worked except that the launcher
could not see it. A user wide `flatpak override` fixes it for every application
at once, which is the right shape, because the next launcher somebody installs
has the same problem and nothing would tell them why.

**What every seat carries** is Steam, Lutris, Firefox, gamescope, MangoHud and
the Noto fonts. Firefox is not an indulgence: signing in to GOG or Amazon
happens in a browser, and that is the case the on-screen keyboard exists for.
Lutris fetches its own Wine builds, so plain `wine` is not installed and would
have cost more than the rest together. Everything heavier stays one click away
instead: Heroic alone is close to a gigabyte with its runtime, per seat, and a
seat belonging to somebody who only plays on Steam should not carry it.

**Proton CachyOS is one of the things every seat carries**, alongside the Proton
that comes with Steam. It arrives the way Sunshine does, from the project's own
GitHub release rather than from a repository, which keeps a seat on plain Arch
from having to trust a second package source for one compatibility tool. The
release publishes a baseline and an x86-64-v3 archive of the same version, and
the seat is asked which one its processor can run: the whole feature set the
level is defined by, not AVX2 alone, because a processor that has AVX2 and is
missing one of the others would take the optimised build and die on an illegal
instruction in every game.

The archive is fetched by the seat rather than by the daemon. It is a third of
a gigabyte, and the way the other downloads here work would hold the whole of it
in the daemon's memory and then push a second copy through the Incus API. Its
published sha512 is checked before anything is unpacked, the unpacking goes to a
directory beside the target, and only a complete unpacking replaces what was
there, so a download that dies half way through leaves the seat with the Proton
it already had rather than a partial one Steam would list and offer anyway.
None of it is fatal: a seat whose Proton could not be fetched still plays
everything Valve's Proton plays, so a GitHub that is briefly unreachable is a
line in the log rather than a seat that failed to build.

**It updates itself**, because a build that exists to carry fixes early is worth
nothing pinned to whatever was current on the day a seat was provisioned. The
daemon looks for a newer release every six hours and shortly after it starts.
The replacement is an unlink and a rename, so it waits for a seat with nobody
streaming out of it and with nothing holding the directory open, asked with the
same `/proc` probe the library uses. A game running under that Proton keeps the
files it already opened, which is precisely what makes it worth avoiding: it
would open the next one it needs some minutes later and find it gone, with
nothing in any log connecting the two.

**It is also made the seat's default**, because a compatibility tool that is
merely present changes nothing: every game still runs under Valve's Proton until
somebody walks through Steam's settings with a gamepad, which is the interaction
this whole project exists to avoid. Steam records the choice in `config.vdf`,
four blocks deep, and there is no command that sets it, so the file is edited.
Edited rather than rewritten: that file also holds the account the seat is
signed in as, so the change is one inserted span or one replaced value and
everything the code does not understand comes out byte for byte as it went in.

Somebody else's choice wins. A setting that names a tool which is not ours stays,
because a seat where the player picked Proton Experimental on purpose is not a
seat with a broken setting. What does get rewritten is a setting naming one of
our own builds under an older, versioned name.

That versioned name is the reason the tool's own manifest is rewritten too.
Upstream names it after the version and the instruction set, so every update
introduces a tool with a new identity and every setting that named the old one
quietly stops pointing at anything. The name is fixed to `proton-cachyos`
instead, which is what Valve's own `proton_experimental` does and for the same
reason: the identity is the channel, not the build. The version moves to the
display name, so the menu still says which build is running.

Both of those wait for Steam not to be running, and it is the same window on
purpose. Steam keeps `config.vdf` in memory and writes the whole of it out when
it exits, so a change made underneath it is not ignored but undone. Renaming the
tool is also exactly what invalidates a setting naming the old one, so doing
that half while Steam holds the file would leave a seat pointing at a tool that
no longer exists, which reads as the default silently reverting. Provisioning is
the reliable moment for both: the session has just been rebuilt and nothing has
started Steam yet.

`/dev/ntsync` is passed into the seat for it. That is the kernel interface Wine
uses for the synchronisation primitives Windows programs expect, Proton CachyOS
is built around having it, and without it Proton falls back to esync and fsync.
Optional like the other host devices: a kernel too old to have it should cost a
seat its fastest synchronisation, not its ability to start.

## The client with no keyboard and no mouse

Which is most of them. A seat streams to an Apple TV, a phone, a television,
and the person holding the controller has no other input device at all. The app
list and Steam Big Picture are navigable that way, so starting a game was
covered; signing in to a store in a launcher was not, and that is the case that
decides the design.

**The client cannot supply it.** Moonlight on tvOS has an open request for
entering text into the session and an open bug for keyboard passthrough; what
it does send is modifiers rather than letters. Steam's own keyboard covers
Steam, and non-Steam applications only if they were added to Steam and launched
through it, with several open bugs on Linux about keys that do not type. Neither
reaches a browser window inside a launcher.

So both halves live in the seat and travel in the video stream like everything
else. squeekboard draws the keyboard, which was written for phones and turns out
to work on sway unchanged. `polyseat-pad-pointer` turns the gamepad into a
pointer to press it with, reachable from Super+K, the bar, and a gamepad button,
because whoever needs it has the fewest ways to ask.

**Pointer mode follows what is in front, and that is the whole safety story.** A
helper that turned a thumbstick into a mouse while a game was running would make
every game unplayable, so the compositor is asked instead of guessed at: a
fullscreen application in front means the controller belongs to it and the mode
goes off, and back on the desktop it goes on. That is what the Windows tools do
from the foreground window, and sway can answer it exactly rather than by
heuristic. Two of Select, Start and Guide held together for a second still
override it by hand, a chord because single buttons are taken and held rather
than tapped because Select with Start is an input a game may well want outright;
the pad buzzes when it takes, which is the only confirmation available, since
nothing appears on screen and the pointer shows itself only once the stick
moves. Sunshine's virtual pads carry force feedback back to the client and
Moonlight passes it to the real controller, so the buzz reaches the hands that
held the chord.

**Any two of the three, because which of them arrive is the client's decision.**
The first version of the hold named Select and Start, and through an Apple TV it
could not be pressed at all: Moonlight builds the Guide button out of that pair,
tvOS having kept the real one for itself, so what a recording in a seat shows is
BTN_START with BTN_MODE held for 1.95 seconds and BTN_SELECT never arriving.
Tapping the two had worked, which is what made this look like a change that
broke a working chord rather than a chord that had never survived the client.
Counting two of the three buttons no game plays with covers every order the
client produces.

The override holds until something goes fullscreen or stops being: that covers a
windowed game, and it means a forgotten override cannot leave somebody holding a
stick that does nothing. The gamepad is never grabbed, so games see it exactly
as before. Left stick points and right stick scrolls, which is the way round the
Windows tools do it and worth matching.

**The buttons are the ones those tools already trained.** A clicks, X right
clicks, B is Escape, Y and a short press of Start are Enter; JoyXoff's primary
bindings are the same arrangement, and B for cancel is what every menu on the
machine does anyway. Start is the interesting one, because it is also half the
chord: it cannot act when it goes down, since that is the moment somebody may be
starting to hold it, so it acts on release and only if nothing joined it. The
same shape would be needed for anything else put on a chord button.

**Watch the evdev names while reading that code.** `BTN_NORTH` is the X button
and `BTN_WEST` is the Y button - they read like positions and are the old
`BTN_X` and `BTN_Y`. Checked against inputtino, which builds the pad Sunshine
hands to a seat. The mapping was once right in the code and backwards in the
seat's help text for exactly this reason, and nobody holding a controller can
tell which of the two is lying, so a test now reads the help text and compares
it against what the helper actually does.

**How fast it moves is a fraction of the screen, not a number of pixels.** A
seat's output becomes whatever size the connected client asked for, so a fixed
1100 pixels per second, which is what this started with, threw the pointer
across a phone streaming 720p and crawled on a 4K television. It now crosses the
screen height in a second and two thirds whatever the resolution: measured by
feeding a synthetic gamepad in at full deflection and summing the relative
motion coming out of the pointer device, 647 pixels per second at 1080p, 433 at
720p and 1289 at 2160p. The number itself came from the first person to use it
saying it was too fast.

**And it is a per seat setting**, because it is a matter of whose hand is on the
stick: the number that suits somebody on a television is too much for somebody on
a phone, and two people can be playing at once. The daemon writes it into
`~/.config/polyseat/pointer.conf` and the helper rereads that file when it
changes, so moving the slider is felt within a couple of seconds without
restarting the session or provisioning again. That mattered enough to build:
this is a setting somebody adjusts while holding the controller and watching the
result, and one that needed a second, unnamed step would look like it did
nothing. A value the helper cannot use is ignored rather than argued with, since
a pointer at the wrong speed is a nuisance and a helper that exits over a
malformed line leaves somebody on a television with no pointer and nothing on
screen saying why.

Two connections to sway, because a subscribed one only delivers events and
cannot be asked a question in between, and the tree is asked rather than the
event read, because an event says what changed and not what is in front
afterwards: closing a fullscreen window and revealing another one is a single
event about the window that went away.

One quirk cost a bug. Sway reports `fullscreen_mode` 1 on workspaces themselves,
inherited from i3, and focuses the workspace when no window holds focus, which
is the ordinary state of a seat sitting on its launcher. Measured in seat1: the
only focused node in the whole tree was the workspace. Reading that as an answer
turned the pointer off on an empty desktop, which is exactly when it is wanted,
so only `con` and `floating_con` count.

**A gamepad comes and goes and the session does not.** It appears when somebody
starts streaming and the broker attaches it, and it is gone again when they
stop, so one seat sees several over an evening. Scanning once at startup, which
is what this did first, meant the helper worked until the first person stopped
playing and was dead to everybody after that. It rescans every two seconds, and
when the last pad disappears it releases whatever was held and switches the
mode off, so the next person does not inherit a pointer they never asked for.

Written rather than configured, and that was a deliberate change of mind. The
obvious answer is an existing remapper, and two were tried. sc-controller ships
a desktop profile and an on-screen keyboard, and its keyboard no longer starts
at all under current Python. antimicrox is maintained and works, but its profile
is an XML format that would have had to be guessed at and could not be verified
without a controller in hand. A hundred lines that do exactly this can be tested
instead: a synthetic gamepad is fed into it and what comes out of the pointer
device is read back, including the case that matters most, which is that nothing
comes out while the mode is off.

## Resolution per client

The seat's output is virtual, so unlike a monitor it can simply become the size
that is wanted. Sunshine puts the connecting client's width, height and
framerate in the environment of its `global_prep_cmd`, and a headless sway
output takes a new mode at runtime, so the whole feature is one script: adopt
the client's size on the way in, put the seat's configured mode back on the way
out.

Two details are load bearing. The script finds the sway socket itself, because
Sunshine runs as a user unit and the user manager has no `SWAYSOCK`. And it
never fails: a prep command that returns non-zero stops the stream from starting
at all, so a seat at the wrong resolution has to be the worst outcome. Bad input
is reported and ignored rather than guessed at.

## Framerate per client

The output's refresh rate paces anything that waits for vblank, and that is not
the same as capping the framerate. A game with vsync off renders as fast as the
card allows: measured in a seat, 1519 frames per second against a client asking
for 60. Everything above the client's rate is heat and a longer queue rather
than a frame anybody sees. Turning vsync on to stop it is the wrong trade,
because latency is what a stream has least of to spare.

So the games stay uncapped and the limit is applied from outside, which is what
RTSS does on Windows. The Linux equivalent is MangoHud: a Vulkan layer, plus a
preloaded shim for OpenGL, both reading one configuration file. `polyseat-fps`
writes the client's framerate into that file on the way in and takes it out
again on the way out, so one file caps a native game, a game under Proton, a
flatpak launcher and an emulator without any of them being configured. Measured
in a seat: 1519 fps uncapped, 58.3 with a 60 fps client connected, and a game
that was already respecting vsync loses nothing worth measuring.

**Two lines go into that file beside the cap, and they are about age rather than
count.** `fps_limit_method=early` changes when the limiter waits: MangoHud's
default renders the frame the moment the previous one was presented and sleeps
out the rest of the interval, so what goes out is already almost an interval old
before anything has encoded it. Sleeping first and rendering last costs the same
heat and the same framerate. `vulkan_present_mode=mailbox` is the same idea one
step further along: a FIFO swapchain queues frames and waits for them to drain,
and nobody sees the far end of that queue over a stream, so the newest frame is
kept and the rest dropped. Both are written only alongside a cap. Mailbox never
blocks the game, so with the cap gone it is nothing that paces a seat, and a
game left running after a stream ended would go straight back to the thousands
of frames a second the cap exists to prevent.

Three things carry it into place, because there are three ways an application
gets started in a seat. Sunshine's app list carries the two variables in its
`env` block, which Sunshine applies to what it launches and not to itself: the
same library loaded into Sunshine would be limiting the encoder, and loaded into
sway it would be limiting the desktop. Each game's own launcher entry carries
them again, in its `Exec` line, for the games somebody starts from the desktop.
And flatpaks get a user wide override, because a sandbox sees neither the seat's
environment nor its home directory, along with the MangoHud layer extension for
whichever runtime version they use.

**The cap goes on games and on nothing else, and that was learned the hard
way.** It used to reach the desktop from both ends at once: fuzzel had
`launch-prefix=/usr/bin/mangohud`, so everything started from the launcher was
wrapped, and the launcher is opened by a Sunshine prep command, so it inherited
the app environment and passed the preload on to everything, a terminal
included. Firefox dies of that immediately, every time: measured in a seat,
SIGSEGV during EGL setup, a minidump written, nothing on screen. Which made the
browser unusable from the one menu somebody holding a gamepad can reach, and
pointed at nothing. Steam survived only because MangoHud blacklists
`steamwebhelper` by name. So the prefix is gone, the launcher unsets
`LD_PRELOAD` before it starts, and the cap rides on the entries that name a
game. `MANGOHUD` itself stays set everywhere, because it only enables a Vulkan
layer and costs nothing outside a Vulkan application. Measured through the entry
form with glxgears: 21039 fps uncapped, 59.94 with a 60 fps cap. What is no
longer capped is a game started from an entry Polyseat did not write.

One thing had to be fixed before any of it worked. squeekboard registers a
virtual keyboard with the compositor when it starts but only gives it a layout
the first time it is shown, and until then sway hands out a zero length keymap.
MangoHud maps that without checking the length and dereferences the failure, so
every Vulkan application in the seat died with SIGSEGV before drawing a frame,
and opening the on-screen keyboard during a game killed the game. The session
therefore shows the keyboard once at startup and puts it away again, before
anybody has connected and while there is nobody to see it.

## Never reload the app list under somebody

Telling Sunshine to reread its app list ends the stream in progress. Not politely:
it emits no `CLIENT DISCONNECTED` and runs none of the `undo` commands, so the
seat is left at the client's resolution with the framerate still capped, and the
interface then reports that as the truth because it is the truth. Two complaints,
one cause: a Moonlight session ending by itself, and a resolution that stayed
after the client had gone.

The code carried a comment claiming a reload interrupts nothing. That was an
assumption from the fact that it is not `/api/restart`, never measured, and the
measurement is in the seat's own log: a stream started, the list was rebuilt one
minute later, and nothing followed.

So the rebuild waits while somebody is streaming, and happens the moment they
stop. Both paths, the minute timer and somebody installing a launcher from the
web interface, because throwing a player out of their game is a worse outcome
than a menu entry appearing a few minutes late.

The daemon also puts the seat back itself when it sees a session end, rather than
trusting Sunshine's undo to have run. That is the same signal the card uses, and
it covers every abnormal end and not only this one.

**A guard is only as good as the thing it asks.** The first version of this asked
the marker file that describes the stream, and got thrown out of a stream anyway.
Sunshine runs its prep commands once per application launch, not once per
connection: a client that drops and reconnects keeps the application running, so
nothing rewrites that file. The old read deleted it whenever the connection was
missing for a single poll, so a twelve second wifi hiccup on an iPhone left a
live session with no marker and a daemon certain the seat was idle. A minute
later the list was rebuilt under it. Both halves are in the logs: the seat's
`CLIENT DISCONNECTED` at 17:48:21 and `CLIENT CONNECTED` at 17:48:33 with no
`Do Cmd` between them, and the daemon's rebuild at 17:49:12.

So the two questions are kept apart. The sockets decide whether somebody is
streaming, because they come back by themselves; the file only describes what
they are playing, and is kept rather than replaced when a reading brings none. A
missing connection has to stay missing for 45 seconds before the stream counts
as over, which is what a reconnect fits into, and the stale file is cleared only
then.

**And the guard has to ask something that is there for the whole stream.** The
second version asked for an established TCP connection on the control ports, and
somebody lost a stream to a game installed in Steam anyway. The seat's own log
has both halves: `CLIENT CONNECTED` at 19:51:02, the whole app list printed at
19:52:31, `Process terminated` in the same second, and no `CLIENT DISCONNECTED`
anywhere between them. The daemon believed the seat idle throughout, and the
stale marker file it never cleared proves it: it only clears one when it sees a
stream end, and it never saw one, because it never saw a stream. A client that
has finished its handshake can leave no established connection behind at all.

What is there for exactly as long as a session is the set of sockets Sunshine
opens for it: video, control and audio on UDP 47998, 47999 and 48000. None of
them exists in an idle seat, checked, and they belong to the running process, so
unlike anything written to a file they cannot be left behind by a session that
died badly. The connection is still asked about as well; either one says
streaming.

Three further things follow from being wrong about this twice.

**The check says which of three things it found.** Idle, streaming, or nothing
it understood, and the last one holds the app list back exactly as firmly as the
second. The old check ended in `cat` of a file that need not exist, so a seat
without a marker answered with a non zero status, and that was read as nobody
streaming: a check whose failure mode was the dangerous answer. A reading that
says nothing also no longer ends a stream, so an `incus exec` that timed out
cannot put the resolution back under a game.

**The guard sits immediately in front of the destructive step, not a minute
ahead of it.** Between deciding that nobody is streaming and telling Sunshine to
reload, the seat is scanned: Steam's manifests, Lutris, artwork fetched over the
network. Somebody who connects during those seconds used to lose their session
to a decision taken before they existed. The list is still written in that case,
because writing the file disturbs nothing, and the reload alone is held back.

**Which is why a held back reload is remembered.** The file on disk is then
ahead of what Sunshine has loaded, and the next pass would find it already
correct, report no change and never reload, leaving Moonlight on the old list
until the seat restarted.

## Bringing the seats up to date, and seeing who is on them

Two things the interface owes somebody running a machine several people share.

**One button for every seat an older generation built.** The generation mechanism
above marks a seat as out of date; acting on that used to mean opening each card
in turn and remembering which had been done, which after a change to the daemon
is every one of them. A banner offers it as the single action it is, and the
sweep works through the seats one at a time: four provisioning runs at once turn
four slow operations into four slower ones and make each log impossible to
follow. It runs in the daemon rather than in the browser, because it takes
minutes per seat and the person who pressed the button is often on a phone that
will lock its screen.

The waiting is the part worth writing down, because the first version did not do
any. It called Provision straight away, and a seat that is busy answers "busy",
which was noted on that seat and skipped. Both seats had been started five
seconds earlier, so a request that reported it was provisioning two seats
provisioned neither and said nothing anybody would read as a failure. It now
waits for a seat to be free first, gives up on one that never is after five
minutes and says so on that seat, and carries on past a seat that fails rather
than abandoning the rest.

**Who is streaming, on the seat's own card.** Asked of Sunshine first, which does
not answer it: `/api/session`, `/api/sessions`, `/api/status` and
`/api/clients/active` are all 404 on the version in a seat, `/api/clients/list`
gives the paired devices and not the one connected, and the log at its default
level records the encoder and the bitrate and never the client. So it is written
down where it is known instead. Sunshine's prep commands run when a stream starts
and again when it ends, with the client's size, framerate and HDR in their
environment and the name of the application it asked for, and `polyseat-session`
puts that in a file for as long as the stream lasts.

The address comes from the connection: Sunshine's control channel is a TCP
connection that lives exactly as long as the stream, so the peer on port 47989 or
48010 is the machine somebody is sitting at. Not a name, because Moonlight only
gives its name while pairing and Sunshine keeps that against a certificate rather
than against an address. The paired names are listed a few rows below on the same
card, which is as close as this gets without Sunshine's help. Reading that peer
cost one mistake worth recording: `ss` leaves out the state column when it has
been asked for a single state, so the address is the fourth field there and the
fifth without the filter, and counting from the left returned nothing and looked
exactly like nobody being connected.

## When Incus stops answering

The manager talks to Incus over one long lived connection, and every look inside
a seat is an `incus exec` over it. That connection can stop delivering the
results of its operations while staying open, and when it does, `WaitContext`
never returns: two calls were found parked in it for twelve minutes, one from
provisioning and one from the sweep that runs after every operation, while the
same command typed into a shell answered instantly. The seat sat in
"provisioning" with nothing in its log and nothing to press.

That shape of failure is worse than a crash, so the calls that are only ever a
read now carry a deadline: the unit states, the encoder, the output size, the
session, the uid, and the wait for systemd inside a new container. Thirty seconds
for the reads, twenty per attempt for the systemd wait, whose own ninety second
deadline was decoration while a single attempt could hang for ever. The reconcile
that runs after an operation finishes gets two minutes, having had no caller to
cancel it at all: it used `context.Background()`, so one stuck exec leaked a
goroutine for the life of the daemon.

It happened twice in an hour, the second time inside `Start` for a container
Incus had already brought up, so the operations are bounded too: three minutes
for the ones that are neither a download nor somebody's package manager, and a
caller that set its own deadline keeps it, because provisioning installs packages
for minutes at a time and passes a context of its own.

Waiting is not enough on its own, though, and a deadline is a poor answer to it:
it cannot tell a stalled wait from an image that is genuinely still downloading.
Both halves of building a seat were lost this way on the same afternoon, a
container created and sitting there stopped with its image fully downloaded, and
a container started and running, while the daemon waited on each for minutes. So
every operation is now asked about directly as well, once every five seconds,
with `Refresh` on its own URL. That is a plain GET and involves no events at all,
which is exactly why it answers when the stream does not, and it turns a lost
notification into a normal completion instead of a failure.

And a wait that times out replaces the connection before returning, so the
next call works and nobody has to restart the daemon. Only that failure: a
container that genuinely refuses to start must not cost the connection every
time. The old connection is left to be collected rather than disconnected,
because the lifecycle listener is riding on it and cutting that would take the
daemon down with it. The dialling is a field on the client rather than a call, so
the repair can be tested at all: the real one needs the Incus socket, which a
test running as an ordinary user cannot open, and a repair that quietly fails to
happen looks exactly like one that worked.

## Where a seat sits on the network

Each seat is a host of its own on the LAN with its own address, so it can use
the standard Sunshine ports and no port juggling is needed. How it gets there
depends on one thing about the machine, and the daemon reads that rather than
being told: whether the uplink named in the configuration is a bridge, which is
`/sys/class/net/<if>/bridge` existing. A plain interface gives the seat a
**macvlan** on it, a bridge gives it a **port** on that bridge. No new setting,
and `lanDevice` builds the device for provisioning and for the checkbox below
from the same rule, or which arrangement a seat ended up in would depend on
which code path touched it last.

**Why the bridge exists at all.** A macvlan and its parent are kept apart by the
kernel, deliberately and unconditionally: the host and its seats are on the same
wire and cannot hear each other, and no route, firewall rule or port forward
changes it. That is fine until somebody wants to play a local multiplayer game
between the host and a seat, because those games find each other by
broadcasting. `host/lan-bridge.sh` makes the uplink a bridge and moves the
address onto it. Measured after the change on this machine: ping both ways, UDP
broadcast in all three directions (host to seat, seat to host, seat to seat),
and a gateway round trip of 0.640 ms against 0.647 ms before, so bridging costs
nothing measurable.

**An interface with macvlan children cannot be enslaved to a bridge.** The
kernel answers `EBUSY`, and moving the children into another network namespace
does not help; reproduced on a dummy interface three ways. Worse, none of it is
visible from the host, since `/sys/class/net/<if>/upper_*` only shows the current
namespace, so the question "does anything have a macvlan on this uplink" has to
be asked of Incus. Getting that wrong once cost this machine its LAN: the script
built the bridge, moved the address, failed silently at the enslave and reported
success. It now stops the seats first, rolls back everything from the first
change on any failure, and calls it a success only when the interface really is
a port, the bridge really has the address, and the gateway really answers over
it.

**Which side of the line a seat is on is a checkbox on the seat**, on for a new
one. A seat with it off gets a macvlan on the bridge rather than a port on it,
which restores exactly the isolated arrangement for that one seat: it reaches
the gateway and the other seats and cannot reach the host, and the host cannot
reach it. No nftables rule is involved; it is the same macvlan property as
before. The MAC is pinned and carried across the switch, because Incus otherwise
generates a new one, the new one gets a new DHCP lease, and the seat moves to a
different address the first time somebody ticks the box. What that costs on the
security side is in [`security.md`](security.md), and it is a real cost: the
bridge is the removal of a line that used to hold.

## Capacity

Reference machine: RTX 4080 (16 GB), 24 cores, **31 GB RAM**, btrfs.

RAM is the bottleneck, not the GPU. An AAA seat wants 8-16 GB - five
simultaneously playing seats do not fit; realistically 2-3 plus a few light
ones. NVENC and CPU have plenty of headroom; VRAM gets tight with three modern
titles. The software is built for N, the hardware sets the cap.

## Alternative approaches to input isolation

Sunshine issue [#3768](https://github.com/LizardByte/Sunshine/issues/3768)
collects the same problem, and two other solutions are discussed there. Both
deserve a look, because one of them is conceptually cleaner than ours.

**Wolf's fake-udev** (games-on-whales) is the same approach we arrived at
independently: send a netlink message on the udev multicast group from inside
the container's network namespace as root, and write the matching entries under
`/run/udev/data/`. Their documentation adds one detail we get for free, namely
a `/run/udev/control` file to signal udev's presence, which exists in our seats
because systemd-udevd runs there. Nothing to change, but worth knowing that the
approach is documented prior art rather than an invention of ours.

**vuinputd** (joleuger) is architecturally the better idea. It is a CUSE proxy
for `/dev/uinput`: every container gets its own mediated uinput device, the real
device creation happens on the host, and the daemon forwards the udev events
into the container itself. Attribution is then **structural**. It knows which
container created a device because it mediated the call, so there is no name
tag, no regular expression and no polling of `/sys`.

That was strictly nicer than what we did at the time this was written, when the
broker inferred ownership from a name Sunshine happens to write. It no longer
describes the difference: attribution here is structural too. For uinput the
creating descriptor is asked directly through `UI_GET_SYSNAME`, and for uhid a
kprobe records the creating process at the moment the kernel makes the device.
Names decide nothing, and a device called something nobody listed is attributed
correctly anyway.

**We are still not using it, for one concrete reason: vuinputd proxies
`/dev/uinput` only, not `/dev/uhid`.** And we measured in M3 that Sunshine
creates gamepads through inputtino as HID devices via uhid. So exactly the
device class that matters most for gaming would fall outside its isolation. The
author also lists Steam input support and force feedback as open gaps and calls
the project alpha.

Our approach covers keyboard, mouse, touch, pen and gamepad today, verified on
real hardware, and puts nothing in the event path between Sunshine and the
kernel. What is left to envy is that vuinputd never has to attribute a device at
all, because it mediated its creation; ours attributes after the fact, and pays
for it with the half second before the broker's next pass for a device that is
neither named in the udev rule nor made through uhid. Gamepads and their raw HID
nodes are sealed at creation and are not in that gap, which
[`security.md`](security.md) measures. If it gains uhid support, it is worth
reconsidering for that reason and not for the old one.

## Rejected alternatives

- **Adopting Wolf (games-on-whales).** Solves the same problem container-first
  and speaks Moonlight directly. Deliberately not taken: we want our own stack,
  and the host desktop must keep running alongside. Still relevant as a source
  of ideas (`inputtino`, fake-udev).
- **One Unix user per seat without containers.** Remains the fallback plan
  should the input chain fail in M0.
- **One VM per seat.** One GPU, not divisible.
- **One user, several compositor instances.** Solves neither the Steam lock nor
  input.
