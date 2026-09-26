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
- **Seats run permanently.** On-demand start is a later feature, not a design
  constraint. Idle was 400 MB when this was written and a seat was sway with a
  terminal in it; a seat today starts Steam with the session and sits at
  1386 MB of memory and 493 MB of video memory, measured in seat vince on
  2026-09-22. What that buys, and why Big Picture is not part of it, is under
  "Steam is started with the session".
- **SDR**, no HDR. On Linux/wlroots/NVIDIA, HDR is the most expensive wish on
  the list and buys nothing for the start. This one has since been tested to
  destruction rather than left as an assumption: HDR out of a headless seat was
  built and measured end to end in [`spike/m8-hdr/`](../spike/m8-hdr/), and the
  decision on 2026-09-19 was still not to ship it, because the price is a
  forked compositor pinned to one wlroots release. The constraint stands, and
  now it stands on numbers.
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
   keep in step. See [amd.md](amd.md), which is honest about
   what one AMD machine has reported and what that still leaves open.
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

Per seat: headless Sway (`WLR_BACKENDS=headless,libinput`,
`WLR_LIBINPUT_NO_DEVICES=1` because `/dev/input` is empty until a client
connects, `WLR_RENDERER=gles2`, `LIBSEAT_BACKEND=noop`) as the session shell,
because Sunshine can capture there via
`wlr-screencopy`/`export-dmabuf` - KMS capture is dead on the proprietary NVIDIA
driver, and on AMD it wants `cap_sys_admin` and, more to the point, DRM master,
which is held per device: on a machine with one card exactly one seat could
capture that way. Same setting on both vendors, for two different reasons. gamescope is nested inside that, permanently rather than
per game: Steam runs in it so that the in-game overlay works at all, and games
started from Steam inherit it. The framerate cap is not gamescope's either, it
rides on MangoHud, which is the one route that reaches a native game, a game
under Proton, a flatpak and an emulator alike. Both are spelled out under "What
a seat looks like from the inside".

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

There is no CLI at all, not even a thin one. The daemon takes five flags and
none of them operate anything: `-config`, `-listen` and `-version`, plus
`-report`, which describes the installation on stdout for a bug report, and
`-uplink`, which prints the interface the seats hang off and why it was picked.
The last two read and print and then exit; neither creates, starts or changes
anything. A second way in
would mean a second author for the generated files, which is exactly what the
next section forbids. When the interface will not start, the thing to read is
`journalctl -u polyseatd`.

**That reaches the machine itself now, not only the seats on it.** Getting a
host ready and taking Polyseat off it again were the two things at either end of
its life that still needed a terminal, and both are buttons. Neither is a second
way into anything the daemon owns: they run the same two scripts somebody at a
terminal runs, `polyseat-prepare` and `polyseat-uninstall`, and the browser
cannot say what to run: one takes an account name, the other takes two flags.
What is left on the command line is what nothing already on the machine can do
for itself: installing the package and starting the unit. Where that line runs
and why is [`installation.md`](installation.md).

**The page is told that something changed, not what.** The daemon pushes a
token over server sent events whenever anything changes, several a second while
a seat provisions, and the page used to fetch both the state and the library on
every one. The library walks the pool and every seat's manifests under the
pool's lock, so an open page kept the daemon reading manifests several times a
second for a view that had not changed. It now fetches the library when
something it did may have changed it, when the stream connects, when the set of
seats or which of them take part has changed, and otherwise at most every
fifteen seconds, with a refresh scheduled for the end of that interval so a
change inside it is not left waiting. A game installed inside a seat reaches the
pool with no token of its own, so those fifteen seconds are also how long it can
take to show. Reading the library for the page also no longer asks every seat
whether its files may be replaced right now; only a pass acts on that answer.

Every value the page puts into an API path is encoded, through one tagged
template, `apiPath`, so that a call site cannot forget. A folder title such as
`folder:Tom & Jerry #2` used to be cut off at the `#`, and the daemon answered
404 for a title it had just listed.

## Principle: the daemon owns the configuration

Incus profiles, Sunshine configs, udev rules and systemd units are **generated
artifacts, never inputs**. Edit them by hand and you lose the change on the next
write - in exchange, the state is always explainable and reproducible. Without
this rule, GUI-centred management inevitably drifts out of sync.

## How the daemon is built

The decisions worth writing down, each of them made by something that went
wrong first.

**Events, never polling.** The daemon learns what containers are doing from the
Incus lifecycle stream. It polls only *inside* a container it knows is running,
every ten seconds, to read what the session is doing, and never while a seat is
stopping. The M2 broker prototype polled `incus exec` twice a second regardless
of state; an exec landed inside a shutdown and the Incus daemon hung in
"Stopping instance" with the container already dead.

That read is one exec, not four. The state of the session's two units, the size
of the output and the stream check used to be four round trips through the
Incus daemon every ten seconds per seat, each one a process in the seat and a
lifecycle event, underneath whatever somebody was playing. They are one script
now, with a marker line between the parts, and a part that did not run reads as
"unknown", which is what four failing execs said too.

**"Never while a seat is stopping" is a lock, not a look.** The sweep used to
check the seat's busy flag and then go on to its execs without holding
anything, so a Stop that began a moment later found nothing in its way and the
next exec landed in the container's shutdown, which is the shape of the hang
above. Each seat now has lanes, one per kind of reading that may take a while:
`sweep` for the ten second read, `apps` for rebuilding Moonlight's list, and
`asking` for the question of what the seat is behind on. A reading holds its
lane for as long as it runs and enters it only while no operation holds the
seat. An operation sets busy first, cancels whatever reading is in progress,
and waits for every lane to be let go before it touches the container, in a
goroutine of its own so the request that started it returns at once. A reading
that was cancelled half way returns before it writes down or acts on what it
never finished reading.

**Nothing in the main loop waits for a seat.** The loop that delivers Incus's
events also runs the timers, and until the audit of 2026-09-26 it did the timed
work in place: the sweep visited the seats one after another, rebuilt an app
list when one was due, ran the six hourly `pacman -Sy` in every seat and the
Proton check, reconciled a seat after each event, and ran a whole library pass
every minute. Each of those is as long as a seat decides, and a player can
decide that from inside a seat, so one slow seat held up every other seat's
sweep and every lifecycle event for minutes. Each of them now runs on a
goroutine of its own. A seat's sweep takes its lane before the goroutine
starts, so a seat still being read costs one `TryLock` per tick rather than a
goroutine parked behind it; the app list rebuild runs in the `apps` lane beside
the sweep; the freshness pass and the Proton pass run at most one at a time,
and each seat's turn goes through the lane or through the operation machinery
as above; a lifecycle event is reconciled on a goroutine, and two events for one
seat queue on its sweep lane and each reads what is true when its turn comes;
the timer's library pass runs at most one at a time. The buttons in the
interface still run the library pass directly, because somebody is waiting for
the answer.

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

The directory that walk looks for is resolved first, because `/proc` never names
a symlink: maps, working directories and descriptors all carry the real path.
A library reached through one, which is the ordinary case for
`~/.steam/steam/steamapps`, was never found in use before the audit, and the
pool replaced a game's files while the host's Steam was playing it.

**Nothing a member put in its library is followed.** The daemon is root and
every library it works in belongs to somebody else: a seat's to the seat's
mapped uid, where the player can put a symlink anywhere, and the host's and
`~/Games/shared` to the desktop user. So below the point where a member's
directory starts, the pool reaches nothing by path. It opens the member's
directory once and everything under it one component at a time with
`O_NOFOLLOW`, relative to the directory it is in, and makes every change
through a held descriptor, which leaves no moment between looking at an entry
and using it for a link to be swapped in. Links inside a game are copied as
links and never resolved. The host's paths run through somebody's home, so they
are opened from `/`, following a link only in a directory nobody but root can
write; that keeps `/home` as a link to `/var/home` working and refuses
`~/Games` as a link to another disk. A member whose directory cannot be opened
that way is reported and left out of the pass rather than ending it, since a
seat can now make that happen on purpose. The attacks this closes are in
[`security.md`](security.md), and `internal/library/nofollow.go` has the
reasons for `openat` over `openat2` and for not switching the thread to the
member's uid.

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

**The host has the same place**, at `~/Games/shared` below whoever owns the
library the pool gives Steam titles to. It is read when it exists and not
otherwise, so making the directory is what switches the host on and removing it
is what switches it off; the daemon never creates it, for the same reason it
never creates anything else in somebody's own library. The shape matches a seat
on purpose: a seat's Lutris has `game_path` at `/home/player/games` with
`shared/` beneath it, so the host's wants `~/Games`, which is where Lutris
installs by default anyway. Both have to be real directories, not links to
somewhere else, for the reason in "Library pool" above. A library owned by root
or a system account, below uid 1000, takes no part in the folders at all: the
search for a host library includes `/root`, and what arrives here is meant for a
person's own Lutris.

What travels is the directory, and only the directory. A Lutris installation is
a folder plus a row in that machine's `pga.db` plus a YAML under
`~/.config/lutris/games/`, and the last two are per machine and per user; they
are not replicated and could not sensibly be, since the runner a host install
names lives under `~/.local/share/lutris/runners` and a seat has no such path.
So a game installed on the host arrives in every seat as files that nothing in
that seat's Lutris knows about. Registering it there is the folder's own job.

**A folder may carry `polyseat-setup.sh`**, and once somebody has allowed it the
daemon runs it wherever that folder has been delivered: inside the container as
the player for a seat, and as the library's owner for the host, never as root
or a system account. It runs again after every update the member receives, once
that version has been allowed too, so these scripts have to be safe to run
twice. A failure is written to that
member's log and stops nothing else; the files arrived either way.

**Allowed, because a script from the pool is a script a seat may have
written.** Until the audit of 2026-09-26 the daemon ran it by itself, and that
let a player run code as the host's desktop user, who usually has sudo, by
putting a folder in their own `shared/`. Where a folder came from cannot settle
it, since the pool records no origin and takes the newest copy from whoever
has one, so every script waits for a person. The Library section lists the
scripts waiting, with their text, their sha256 and where they would run, and
the approval names the hash that was shown and is tied to the folder's version
in the pool, its size and its newest time. A changed folder that reaches
another member is a new version and asks again, a removed folder takes its
approval with it, and each member's copy is hashed again immediately before it
runs. Why it is tied to the whole folder and not only the script is that the
script runs the rest of the folder.

A delivery is written down per member, against the pool's version, in
`folder-setup.json` beside the seat records, and kept until the run happens.
It used to exist only in the report of the pass that delivered it, so a seat
that was off when a folder arrived never got its setup, and neither did
anything after a daemon restart. Now a seat that was off runs it on the first
pass after it is running again.

The runs happen on a worker goroutine, one at a time, with a context of their
own. They used to happen inside the library pass, which is called from the
main loop and from interface requests, so a ten minute setup held up every
lifecycle event, and one started from a button was bound to a request that
ended when the answer was sent. Ten minutes is still the limit. On the host the
script gets a process group of its own, killed when time is up, and only the
last 8 KiB of its output are kept; in a seat it runs under `timeout` inside the
container, from the folder, as it does on the host.

This is not the manifest this design does without. The daemon reads nothing out
of the script, and has no opinion about what is in it or which launcher it
speaks to. It is the same bargain as the rest of this side: the directory is the
unit, and what is inside it is its own business. What it buys is the step the
Steam half gets for free, where an `appmanifest` travels inside the library and
Steam reads the library itself.

The script runs as the member's owner rather than as the daemon, and that is
half of the trust argument: whatever it does, it does with what the person it
runs as already has, and never with root. This paragraph used to say it was the
whole argument, on the reasoning that a folder's script hands the member exactly
what the person it came from had. That was wrong, because the person it runs as
is not the person it came from: a seat's script ran on the host as the
administrator. The approval above is the other half.

The two signals Steam gives are replaced by facts read off the tree. Finished
becomes "nothing in it has changed for a couple of minutes", which is honest but
weaker: a download that stalls for longer than that can be picked up half
complete, and there is no way to tell from outside. Version becomes the newest
modification time inside the tree, which is why cloning preserves file times.
Without that a copy would always look newer than its own original and the two
would carry each other back and forth forever.

That has happened twice, and the second time was subtler. Carrying a seat's
`drive_c/users` into the new tree, below, is a rename, and a rename sets the
time of the directory it lands in to the present; `drive_c` is counted, so
every updated copy of a folder with a wine prefix measured newer than its
source, and the pool and the seats handed the whole game back and forth once
the settle time had passed each round. The carry now puts back the times of
every directory it touches, on both sides.

**The saves stay in the seat.** Lutris points at this directory by default, so
installing a game the ordinary way puts it here, and for a Windows game that
brings the wine prefix with it: a Lutris installer sets the prefix to
`$GAMEDIR`, which is the folder itself, so the prefix root and the game root
are one directory and the game files sit in `drive_c/Program Files`. The prefix
cannot be left out, because the prefix is the game.

So the line is drawn one level further in, at `drive_c/users`, which is where
wine keeps Documents, AppData and Saved Games and therefore what the person
playing made. That directory is the seat's, in the same sense `compatdata/` is
on the Steam side: it is not counted when the folder's version is measured, so
an evening at a game does not make the folder a new version and copy it over
the other seat; and a clone leaves it alone, carrying the destination's own
across the swap rather than replacing it. A seat that has never seen the game
is given the one that came with it, so that a game keeping data files rather
than saves under `drive_c/users` works there at all.

A folder can hold more than one prefix, and their saves are carried one after
the other. When an update fails part of the way through, everything that was
carried is carried back before the old copy is restored; until the audit the
cleanup removed the new tree with the saves already moved into it. If carrying
back fails as well, the staging directory is kept and the error names it.

What this does not cover is a game that saves into its own installation
directory, as titles from before the prefix convention do. Nothing can, without
knowing that particular game, which is the manifest this design does not have.

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
ten seconds that a game was uninstalled. The sweep notices when it is due and
hands it to the seat's `apps` lane on a goroutine of its own, rather than
running it in place, and a rebuild still running when the next falls due is
left to finish.

**Everything the scan reads belongs to the player**, Steam's manifests, Lutris,
the desktop entries and `~/Applications`, so it is bounded where it runs. Each
part runs under `timeout` inside the seat as the player, because Incus does not
end a command whose caller has stopped waiting and a FIFO in the right place
used to hold a scan, and with it the daemon's view of every seat, for good. The
daemon keeps a deadline of its own a little beyond the seat's, caps what a scan
may print at 16 MiB, and bounds the whole rebuild at ten minutes. The Python
scans run with `python3 -I`, so that nothing in the player's home is imported
before the scan's first line, and the desktop entry scan has `grep` skip
anything that is not a plain file rather than trusting `find` to have looked.
Only app ids that are one to ten digits make it into the list, because the id
becomes part of a command line Sunshine splits on whitespace.

**And everything the daemon writes into the player's home is written by the
player.** The app list, the game entries, the uploads and every file
provisioning puts there go through a short script run as the player, which
writes to a temporary name and moves it over the destination with `mv -T`, so
a link standing at the name is replaced rather than written through. The
Incus file API, which is what all of it used before, writes as root and follows
links, and why that mattered is in [`security.md`](security.md). A seat that is
switched off has no player to write as, so an upload into one checks every
directory on the way for links first, which is sound only because nothing in
the seat runs; the state is asked before every file.

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

**The desktop's own launcher gets the same games**, from the same scan but not
with the same pictures. That is a second menu, for whoever is already streaming
the desktop, and it was showing Steam, Firefox and a file manager while the
installed games were nowhere: a desktop entry for a game exists only when
somebody asks Steam or Lutris for a shortcut. Where somebody has, theirs is left
alone and Polyseat writes nothing, because that launcher lists every entry it
finds and two files would be two rows. Matched on what the entry starts rather
than on what it is called: the two are written by different hands and only the
game underneath is the same. Remove their shortcut and the generated one is back
within the minute. Each entry starts its game through `polyseat-capped`, a
short script that sets the cap and replaces itself with the game, rather
than through an `env` line, because the launcher's way of starting an entry
cannot carry the dollar sign in `LD_PRELOAD`.

**The pictures differ because the menus draw differently.** A client draws every
entry as a portrait card, which is what the covers are for; that launcher draws
a grid of square icons at a fixed size, and a cover put through it comes out as
a tall sliver between the square icons of Firefox and Steam. It was inconsistent
with itself as well: a game somebody had made a Steam shortcut for wore a proper
icon in that grid, since that entry is Steam's and not ours, so half the games
wore icons and half wore covers.

So a game entry wears the icon Steam's own shortcut would have used, and falls
back to its card only when there is no icon to be had. Steam keeps the address
of that icon in `appinfo.vdf` and nowhere else: a hash under which the icon is
published as a Windows `.ico` of up to 256 pixels, or as a zip of PNGs for the
minority of titles that publish `linuxclienticon` instead. That is Valve's
binary key-value format with an index in front of it, which `polyseat-icons`
reads far enough to find the two fields it wants; a record says how long it is
before it says anything else, so the six hundred apps nobody asked about are
stepped over rather than parsed. Where Steam has already written a shortcut's
icon into the seat's icon theme, that file is used and nothing is fetched at
all, and where the icon cannot be had the small one Steam cached for its own
library list is better than a cover. Lutris names its icons after the slug it
knows a game by, and those are already in the theme.

`appinfo.vdf` is a megabyte and a half, and the icon's file name is the hash
read out of it, so until the audit the helper read it on nearly every pass of
the minute timer. The values it wants are now kept beside the icons with the
size and time of the `appinfo.vdf` they came from, and Steam's file is read
again only when it changed or a title is asked about that the copy does not
have. Both picture helpers take the list of games on standard input rather than
as their one argument, which the kernel caps at 128 KiB: a large library stopped
both from running at all, silently, and every game lost its card and its icon.

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

**The launcher is a grid, because the person using it is usually holding a
controller.** It was fuzzel, a list of eighteen narrow rows, which works with a
mouse and is a poor target for a thumbstick on a phone screen. nwg-drawer draws
the same desktop entries as large icons over the whole screen, and it can be
driven without aiming: the D-pad moves keyboard focus from icon to icon, Y or
Start is Enter, B is Escape. The stylesheet makes the focused icon unmistakable,
since GTK's own one pixel focus ring does not survive a video encoder. It runs
as a fresh process each time it is opened rather than resident, so
`polyseat-launcher` keeps treating "running" as "on screen", and it sits on the
overlay layer with the bar's height left clear, so it covers a fullscreen
window but not the buttons that close it or bring up the keyboard.

**The grid follows the size of the screen, and the screen is the client's.** A
seat's output takes the resolution of whoever connects, so one fixed icon size
is either right on a phone or right on a television. The launcher asks sway how
tall the output is and doubles the icons and the text above 1800 pixels.
GDK_SCALE, which looks like the whole answer, is not: it is set, it is in the
process environment, and GTK on Wayland ignores it, measured in a seat at
3840x2160 where the drawer came up drawn exactly as at 1080p. So the size
travels as the icon flag and the text as `GDK_DPI_SCALE`, which is also why the
stylesheet sets no font size in pixels: a fixed one would have stayed small
while everything around it grew. The bar's margin is not doubled, because
waybar is 30 pixels tall whatever the client asked for.

Two things had to change underneath before the grid was usable, and neither
showed until it was tried. The D-pad had never produced arrow keys at all: the
helper listened for `BTN_DPAD_*`, and every pad inputtino builds reports the
D-pad as `ABS_HAT0X` and `ABS_HAT0Y`. And nwg-drawer starts an entry through
`env -S`, which refuses the `$LIB` the game entries carried for the dynamic
linker, so every game in the grid exited with 125 while Steam beside them
started. Both are described where they were fixed. A third only appeared in a seat: the
launcher had no `SWAYSOCK`, because the session imports `WAYLAND_DISPLAY`,
`XDG_SESSION_TYPE` and `DISPLAY` into the user manager and not that one, so
every caller that matters had to find the socket the way `polyseat-resize` does.

**Software goes in from either end.** `flatpak --user` needs no privileges at
all, which is what makes it the right mechanism here rather than a convenient
one: the player installs into their own home with no password, and the daemon's
install button in the web interface runs the same command as the same user, so
there is one list of installed software and not two. Flathub is added per user
for the same reason. The cost is `security.nesting=true` on every seat, which
flatpak's sandbox needs since the setuid bwrap it used to get by with is gone
upstream, and why that is the smaller cost is set out in
[`security.md`](security.md).

Three routes, because they answer to different people. `gnome-software` is in
the seat for whoever is sitting in it, browsing Flathub with pictures and a
search field; it costs almost nothing to add because a seat already has the
toolkit underneath it, where bazaar would have been 52 MB and discover 212 MB.
The web interface is for setting a seat up for somebody before handing it over.
The command line is for neither and stays anyway.

**AppImages are the other kind, and they need a different mechanism because they
have no index.** There is no Flathub for them: an AppImage is one file on
somebody's release page, and the only thing that knows it exists is the
directory it was put in. So the seat side is a directory listing rather than a
package manager. `~/Applications` is what is listed, `~/Downloads` is swept into
it once a minute, and each file's name and icon are read out of the file itself
with `unsquashfs`, cached against its size and modification time so that a scan
costs a listing rather than an unpack. A type 2 AppImage is an ELF runtime with
a squashfs appended, and the squashfs begins where the ELF's section header
table ends, so the scan computes that offset from the header, checks the
squashfs magic there and reads the filesystem from the outside. Type 1 images,
ISO 9660 and long out of use, get their file name and no icon. From there they
join the same path as everything else and become entries in Moonlight and in the
seat's launcher.

Three facts about this were measured in a seat rather than assumed. The
container already has `/dev/fuse` and a setuid `fusermount3`, so nothing about
the container had to change; what was missing was **fuse2**, because the
AppImage runtime dlopens `libfuse.so.2` by name and a seat with only fuse3
stopped every classic AppImage at `dlopen(): error loading libfuse.so.2`.
Reading the metadata needs no FUSE at all, which is why it works even where
running the thing would not. And `curl -#` draws its progress
bar on standard error with no terminal attached, so unlike flatpak, whose
progress needs a pseudo terminal, a download reports itself for free.

The magic bytes are checked before anything else happens to a file, in both the
daemon and the scan. **The scan used to run each file to read its metadata**,
with `--appimage-extract`, which the runtime at the front of the file answers
without reaching the payload. But the runtime is part of the file as well, so
anything that arrived in `~/Downloads` with the right bytes in its header was
executed within a minute, as the player, without anybody having opened it, and
a page that makes a browser save a file is enough to put one there. That ended
with the audit of 2026-09-26. `unsquashfs` comes from `squashfs-tools`, which
every seat installs since generation 59 because nothing else a seat installs
brings it; without it the scan reads nothing, keeps no answer for that file so
that the icon appears once the tool is there, and does not fall back to running
anything.

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
daemon looks for a newer release every six hours and shortly after it starts,
on a goroutine of its own and at most one pass at a time, since a pass is a
download of a third of a gigabyte for each seat that is behind and it used to
hold up the main loop for as long as GitHub took. Each seat's turn goes through
the same machinery as any other operation, so a seat that is being provisioned
or having its software updated is skipped until the next pass rather than
having its compatibility tools replaced underneath it; a side effect is that a
turn clears the seat's last error, as any operation does. What the release
says, its URL, its checksum and its tag, goes into the script the seat runs
single quoted, so that nothing in a release's name is read by a shell.
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

**GE-Proton is the other one, and every seat carries it too.** The two are
not competing builds of the same idea: Proton CachyOS publishes work on latency,
which is the currency a seat is short of before a game has drawn anything, and
GE publishes a long list of named games and launchers - window modes, logins,
controller mappings. So one is under everything by default and the other is
there to be picked for a game that misbehaves, which Steam does per title. The
seat's `config.vdf` is never touched for GE; it is a directory and a manifest
and nothing else.

It goes in by the same machinery, which is why adding it was mostly deletion:
one description of a tool - a name on disk, a label for the log, the compression
upstream chose and how to turn a tag into a menu name - and one installer that
takes either. Both keep a fixed identity for the reason described above, both
write a stamp naming the release they are, both are unpacked beside the target
and moved into place, and both are updated on the same six hourly pass, each
waiting for its own directory to be idle rather than for the other's.

**It is there by default because the alternative is finding out mid-evening.**
A game that wants GE says so by misbehaving, and that is a poor moment to start
a download. The first version of this was opt in and had the shape wrong. The
cost is 1.6 GB per seat unpacked, measured rather than guessed, so a seat short
of disk can still say no: the setting is stored as the refusal rather than as
the wish, the way isolation is, so that the zero value describes a seat nobody
has configured and seats that predate the whole thing pick it up by themselves.
Saying no removes it and says in the log that a game set to use it falls back to
Valve's Proton, which is the one consequence somebody would otherwise have to
work out from a game suddenly performing differently. Both directions wait for
Steam to be closed, for the same reason everything else here does.

A seat that predates it does not have to be provisioned for it either: the six
hourly pass installs it the same way it updates the other one.

`/dev/ntsync` is passed into the seat for it. That is the kernel interface Wine
uses for the synchronisation primitives Windows programs expect, Proton CachyOS
is built around having it, and without it Proton falls back to esync and fsync.
Optional like the other host devices: a kernel too old to have it should cost a
seat its fastest synchronisation, not its ability to start.

**A seat takes the machine's language, its keyboard layout and its clock**,
because it has no business having any of the three of its own. A seat is a
screen attached to this machine. A plain Arch image has none of them: systemd
falls back to C.UTF-8, xkb to a US layout, and there is no `/etc/localtime` at
all, so a seat came up in English with y and z swapped and a clock two hours
out from the host beside it - which cost an evening once, reading a seat's log
against the host's time.

The language reaches further than the menus. wine asks the Unix locale what
`GetUserDefaultUILanguage` should answer, so a Windows game that ships fifteen
translations picks by that alone, and a seat on C.UTF-8 plays every one of them
in English. The layout is the difference between signing in to a store and
hunting for y and z on a phone's on-screen keyboard.

All three are read off the host's own files rather than asked of `localectl` or
taken from the daemon's environment, because polyseatd is a system service: its
`LANG` is whatever systemd started it with and it has no bus of its own.
`/etc/locale.conf` gives the locale, `/etc/vconsole.conf` the xkb settings, and
the `/etc/localtime` symlink the zone, which is the one place every
distribution agrees on - `/etc/timezone` is a Debian habit and is not written
on Arch. `C`, `POSIX` and `C.*` are read as the absence of a choice and copied
nowhere. `KEYMAP` from that second file is deliberately ignored, being the
console keymap rather than the graphical one, and the xkb values land in an
`input *` block in the seat's sway configuration. Each value is checked against
a pattern before it goes anywhere, because these are the one place in
provisioning where something read off the host's disk reaches a shell or a
configuration file that is parsed line by line.

**DLSS needs two things the driver injection does not bring.** `libnvidia-ngx.so`
is the native half of NGX and is not among the libraries
`nvidia-container-toolkit` mirrors into a container; the wine DLLs Proton looks
for, `nvngx.dll` and `_nvngx.dll`, ship with the host's driver and have to be
put where that particular Proton expects them, which is asked of the seat
rather than assumed. Without them nothing fails and nothing is logged: the game
starts, runs, and simply does not offer DLSS in its settings, so the person in
the seat has no way at all to find out why. A host whose driver has neither is
said so in the log, once, rather than left to be discovered from inside a game.

**Sunshine is allowed to lower its own priority**, which is one line of
container configuration and was worth a stutter nobody could explain. Sunshine
asks for nice -10 on the threads that take the frame off the compositor and
-15 on the ones that hand it to NVENC, at the start of every stream, and in a
seat every one of those requests was refused: `RLIMIT_NICE` defaults to 0, and
that limit is expressed upside down, the floor being 20 - rlim_cur, so 0 means
never negative at all. `limits.kernel.nice` is set to 40, the other end, and
what to do with it is left to Sunshine because Sunshine is what knows which of
its threads belongs where.

What made that matter rather than merely untidy is that the priorities around
it are not neutral. ananicy-cpp on the host matches processes by name and does
not stop at the container boundary - a seat's processes are ordinary host PIDs
to it - so on a CachyOS host a seat came out ordered like this:

    sway         -12   LowLatency_RT
    wineserver   -12   LowLatency_RT
    the game      -5   Game
    sunshine       0   no rule exists

The encoder sat underneath every single thing it has to keep pace with,
including the compositor it captures from. While the machine has headroom
nothing shows; when a scene turns expensive the capture threads lose the CPU to
a game seven steps above them, frames leave late, and the client sees a few
seconds of stutter that no frametime graph inside the seat will ever explain,
because the game really was fine. A rule on the host fixes it too and is worth
having, but a seat must not depend on one: the machine that grows the next seat
may have no ananicy at all.

**Sunshine itself is pinned to one release rather than following the
repository.** A seat is built from that tag's own Arch package, and moving the
pin is a deliberate act with a reason written down each time. Two of those
reasons are worth repeating here. The current pin, 2026.914.233613, closes
GHSA-fp6g-27w5-489j, where the packaged binary carries `cap_sys_admin` and
`cap_sys_nice` as file capabilities and the tray initialised GUI libraries
while honouring the module loader variables it found in the environment; that
environment in a seat belongs to the player, so the party it let over the line
is exactly the party a seat exists to keep on the other side of it. And a
pre-release must never be the pin, because it can be withdrawn under one: a
withdrawn tag is a 404 at provisioning time and no new seat can be built at
all, while the seats that already carry it notice nothing.

## Steam is started with the session

For most of this project's life a seat came up as a desktop and waited. Steam
was something the player started, from Moonlight or from the grid, and what
they spent the first minute of the evening looking at was Steam starting. Two
separate complaints from a television turned out to have one shape: the seat
was doing at the worst possible moment work it could have done while nobody
was watching.

**So the session starts Steam, and it starts it inside gamescope.** sway's
first exec lines run `polyseat-steam`, which waits for the output to have a
mode and then starts

    gamescope --backend wayland -W <width> -H <height> -r <refresh> -f -e \
        --xwayland-count 2 -- polyseat-capped steam -silent

and stops there. No window is drawn, nothing is on the screen, and the seat
sits at 1386 MB of memory and 493 MB of video memory, measured in seat vince
on 2026-09-22.

**gamescope is not a preference, and this is the reason.** Steam hands the
in-game overlay to a game through a compositor of its own. That compositor
needs `GLX_EXT_texture_from_pixmap`, NVIDIA's GLX client does not offer that
extension against an X server it did not write, and Xwayland is exactly such a
server. Steam says so itself and then falls back:

    Error: ThreadInit: GLX_EXT_texture_from_pixmap extension unavailable
    Error: Run: failed to initialize GL thread
    SP BPM_uid0: Failed to create output window. Falling back to system composer

The fallback pushes a screen sized texture through main memory once per frame.
Measured on 2026-09-21 by reading the overlay's own page through Steam's CEF
debugging port: the page renders at 60 fps and the player sees one or two,
while the game behind it holds 16.7 ms per frame with no outlier. Nothing about
the cap, the present mode or the resolution was ever the cause, and all three
were tried. Inside gamescope that path does not exist, and this is not a
workaround either: gamescope is what a Steam Deck runs, so Steam and its
overlay inside it is the one arrangement Valve actually tests.

Three details of that command line each cost something to learn. `-e` is what
turns the Steam integration on, and without it the overlay does not appear at
all. `--xwayland-count 2` with `STEAM_MULTIPLE_XWAYLANDS=1` is what Valve's own
session does and what lets a keyboard and a mouse work in game mode. And
`--backend wayland` is not the default: with `DISPLAY` set, gamescope picks
X11, its window in the session is an Xwayland window with a class and no
`app_id`, and every rule this session has for gamescope matches on `app_id`. So
the window is never assigned to its workspace, never made fullscreen and never
seen by the check that waits for it, and the only trace is one line in
gamescope's log. That log is kept, at
`~/.local/share/polyseat/gamescope.log`, overwritten on every start: it is the
one log in this chain that belongs to us, and a gamescope that does not survive
its first seconds is invisible without it.

**Not `-gamepadui`**, which looks like the flag for this and is a trap. It puts
Steam into the Deck's session mode, where "switch to desktop" sends
`CSteamOSManager_SwitchToDesktop_Request` to a SteamOS service that does not
exist here. The request is never answered and Big Picture waits on that screen
for ever. The overlay works because of gamescope, not because of that flag.

**Big Picture is built when somebody asks for it, not at session start**, and
both halves of that are deliberate. Starting Steam early is what takes the cold
start out of the evening: a Steam that has never run needs fifteen to
twenty-five seconds before it can answer anything at all. Leaving the window
closed is what keeps an idle seat cheap. Measured in seat vince on 2026-09-22,
the same Steam throughout and ninety seconds of settling on each side:

    still         1386 MB of memory, 493 MB of video memory
    Big Picture   1411 MB of memory, 711 MB of video memory

The 25 MB of memory is nothing. The 218 MB of video memory is not: two seats
share one card here, and a game wants every megabyte an empty menu is holding.
Building the window on a Steam that is already warm was measured three times in
Sunshine's own command order at 522, 616 and 607 milliseconds, which is the
price of asking for it instead.

**Going back the other way is not possible**, and that is worth writing down
because it looks as though it should be. Once Big Picture has been drawn those
218 MB belong to the renderer rather than to the window. Measured, in the order
somebody would try them: `steam://close/bigpicture` leaves Steam showing its
desktop window and the seat holding more than before; closing the window the
way a window manager does empties the screen while the renderer keeps its
surface, 221 MB of it; and `SteamClient.UI.ExitBigPictureMode` through the
debugging port does free it and leaves a Steam that will not open Big Picture
again. So the only way back to a cheap seat is a restart, which is what picking
Desktop already does, and a stream that merely ended does not decide that for
the player.

**The seat has two workspaces**, and the Moonlight entries switch between them:
1 is the desktop with the terminal and the grid, 2 is gamescope and therefore
Steam. `polyseat-workspace` does the switching as a prep command, and it is the
first one in each entry rather than the last, because everything before it is
time the player spends looking at the other workspace - three to five seconds
of somebody else's desktop, reported from a television.

**Picking Desktop restarts the pair**, which is a stranger answer than it
looks. Under gamescope Steam believes it sits in a session, so Big Picture
offers "switch to desktop", and Steam's own client carries the string "Method
SwitchToDesktop() not implemented." On anything that is not SteamOS that
request is never answered and the interface waits on that screen for ever; it
cost a seat restart to leave. Closing Big Picture is the way out - but closing
it and leaving it closed takes gamescope's only window with it, so the
workspace is empty and the next player switches to a black screen and then
waits for Big Picture to be built. So Desktop runs `polyseat-steam refresh`,
which shuts Steam down and builds the pair again from nothing, while the player
is on the other workspace and cannot see any of it. It is detached rather than
a prep command, because half a minute of prep command is half a minute of
stream that has not started.

**Except while somebody is playing.** That refresh takes a running game with
it, so a player who left a game to look at the desktop for a moment ended it,
with nothing anywhere saying why. Steam starts every game through its own
reaper and the command line carries the app id, so the process table answers
the question exactly rather than by guessing, and a refresh that finds a game
leaves Steam exactly as it is.

**One of these at a time.** The entries overlap in ordinary use: picking
Desktop starts a refresh that takes half a minute, and picking Steam a second
later used to find no gamescope, start a second one, and hand it a Steam the
first was already building. A lock serialises them, held on a file descriptor
the script owns rather than by handing the whole script to `flock`. That is not
style. A lock lives on the open file, so every process inheriting the
descriptor holds it too, and this script's whole purpose is to start a gamescope
that outlives it: the first version handed itself to `flock`, gamescope
inherited the lock, and the next run waited seven minutes with nothing in its
log. The two commands that outlive the script close the descriptor on their way
out, and the wait is bounded at two minutes so that a lock which never comes
free cannot turn picking Steam into a button that does nothing.

**What decides is gamescope, not Steam**, and having that the wrong way round
cost a morning. A session restart leaves gamescope gone and Steam still
running, detached, talking to whatever X server it can find; a guard that asks
"is Steam running" says yes and does nothing, and what is left is precisely the
arrangement this exists to avoid. Worse, a gamescope whose sway is gone does not
die at once - it follows a few seconds later - so `pgrep` during those seconds
finds the previous session's gamescope and the script decides there is nothing
to do. Age is what tells them apart: a gamescope older than the session's sway
belongs to a session that is over, and it is taken away rather than waited for.

**And readiness is the window, not the request.**
`steam steam://open/bigpicture` writes into a pipe in the home directory, and a
Steam that is not listening yet does not queue the request, it drops it. Asking
once after a wait chosen to be about right is what left a seat with a Steam
running and no Big Picture, which the player then watched being built. So the
request is repeated every five seconds until sway can see gamescope's window,
for up to ninety, and the window is proof because Big Picture is gamescope's
only one.

**The environment has to be found rather than inherited.** This script runs
from three places - sway's exec, a Sunshine prep command and `incus exec` from
the daemon - and the last of them arrives with no session environment at all.
`SWAYSOCK` and `WAYLAND_DISPLAY` are therefore looked up from
`$XDG_RUNTIME_DIR`, newest socket first, the same way `polyseat-resize` and
`polyseat-launcher` do it. Newest first because a socket left behind by a
previous sway is still lying there after a restart and picking it is
indistinguishable from picking the live one until something tries to draw.

**What this left behind is worth recording**, since the script it describes
has been deleted and this is now the only account of it. Before gamescope, Big
Picture was fought into place by `polyseat-bigpicture`: sway made the window fullscreen and
the script then insisted, because fullscreen is not the same as a picture that
fills the screen. Big Picture is a fixed 1280x800 interface that Steam scales
to whatever its window happens to be, it asks how big that window is exactly
once, and on a cold start it asks before sway's rule has fired. The witness was
Steam's own log line, `ThreadSetForceDeviceScaleFactors 1.000000 * 1.423025`,
rather than a screenshot - version 0.22.0 photographed the screen instead and
was silently wrong twice in one seat. Inside gamescope none of that happens,
because gamescope hands Steam a screen of exactly the right size. What is left
is `polyseat-bigpicture-watch`, for a different bug with the same appearance:
sway allows one fullscreen container per workspace, so a game going fullscreen
dethrones Big Picture and nothing gives it back when the game exits. The
watcher reads sway's `fullscreen_mode` and `close` events rather than any
window title, since Steam translates the title and a German seat calls that
window "Big-Picture-Modus".

**On the ordinary path it matches nothing any more**, and neither do the
`class="^steam$"` rules in the session's sway configuration: inside gamescope
sway sees one window with the app_id `gamescope` and never Steam's. They are
kept, because `polyseat-steam` still has two ways to end up with a Steam
outside gamescope, a gamescope that fails on its second try and a Steam that
would not close, and a Big Picture that owns the screen there is worth one
process asleep on sway's socket. Said here so that nobody removes them as dead
or takes them for the ordinary path.

That retry had a way of producing exactly that case until the audit. It
started a second gamescope and asked for Big Picture straight away, before the
Steam inside it was a process, and `steam steam://open/bigpicture` with no
Steam to hand it to starts one, outside gamescope. The script now sends nothing
while no Steam is running, and every place that shuts Steam down checks first
and waits the shutdown out. sway also starts Sunshine only once the session's
display has been imported into the user manager, in one command, where the
two used to be separate lines that sway starts at once.

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

**The D-pad is an axis, not four buttons.** The helper's arrow keys were bound to
`BTN_DPAD_UP` and its siblings from the start, and nothing ever arrived on them:
inputtino writes the D-pad of its Xbox, PlayStation and Nintendo pads alike as
`ABS_HAT0X` and `ABS_HAT0Y`, from -1 to 1, which is also what the kernel's xpad
driver does. So the help text promised arrow keys for as long as it existed and
the D-pad did nothing, unnoticed because the pointer got through everything.
Each axis now holds a pair of arrow keys and both are set on every event, since
an axis reports where it is rather than what changed. The button bindings stay
for any pad whose driver does report four buttons.

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
across a phone streaming 720p and crawled on a 4K television. It crosses the
screen height in the same time on all of them instead, measured by feeding a
synthetic gamepad in at full deflection and summing the relative motion coming
out of the pointer device.

**There are two numbers and they answer different complaints**, which took a
round trip to learn. The ceiling was lowered twice, to 0.60 screens a second
and then to 0.45, on reports that said "too fast" and then "still too
sensitive" - and the second of those was not about the ceiling at all. A 1440p
stream on a phone shows targets a few millimetres across, and hitting one needs
resolution near the middle of the stick rather than a lower top speed. So the
curve does that work now: `CURVE = 2.5` gives most of the stick's travel to
slow movement, and the ceiling went back up to `SPEED = 0.90`, a screen in just
over a second, where 0.45 was two and a quarter seconds to cross it.

**And it is a per seat setting**, because it is a matter of whose hand is on the
stick: the number that suits somebody on a television is too much for somebody on
a phone, and two people can be playing at once. The daemon writes it into
`~/.config/polyseat/pointer.conf` and the helper rereads that file when it
changes, so moving the slider is felt within a couple of seconds without
restarting the session or provisioning again. The slider covers 0.10 to 1.50
screens a second, and 0.90 is where it sits until somebody moves it. That mattered enough to build:
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

A pad can also disappear in the middle of a rescan, which is when Sunshine takes
it away at the end of a stream, and until the audit that ended the helper for
the rest of the session: only the open was guarded, not the two capability
queries after it. Both are now, and the session starts the helper through a
loop that starts it again when it dies after running for a while; one that dies
within half a minute is left dead, since that is a helper that cannot run here
at all. It also stopped turning over ninety times a second for the whole
session. It does that only while a stick is off centre or a chord is counting,
and otherwise sleeps until the next rescan.

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
them again, by starting through `polyseat-capped`, for the games somebody starts
from the desktop.
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

**It closes the application first.** A client that leaves without quitting
leaves the application running for it to resume, and a resume runs no prep
commands, so putting the seat back underneath it meant somebody who stepped out
of Steam came back at 1920x1080 on a 4K television, for as long as they played.
So once the 45 seconds are up the daemon asks Sunshine to close the application
(`POST /api/apps/close`), which runs Sunshine's own undo, and the next
connection is a launch that sizes the seat for whoever makes it. Every
application in a seat is detached, so the close ends no process: Steam and a
running game carry on, and picking the same entry in Moonlight lands back in
them. The seat is asked once more immediately before, because closing the
application under a client that has just come back would end their stream.

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

**What a seat is behind on** is a `pacman -Sy` inside it, so it is asked every
six hours and two minutes after the daemon starts, off the main loop and at most
one pass at a time. Each seat's turn and the "Check for updates" button go
through the seat's `asking` lane, which an operation cancels and waits out, so
a Stop no longer brings a container down under a running `pacman -Sy`. The
button waits for a pass already asking rather than refusing, since that pass is
answering the same question within two minutes.

**And the end of a build writes back only what it learned.** A build used to
read the seat's record, provision for minutes, and write that same copy back,
which silently undid anything saved in between, a label, a pointer speed, an
address, and marked the seat current even when the change was one only
provisioning applies. It now reads the record again and sets only the
generation and the uid; when a setting provisioning applies changed while it
ran, the seat stays marked as needing provisioning and the log says why.

**Who is streaming, on the seat's own card.** Asked of Sunshine first, which does
not answer it: `/api/session`, `/api/sessions`, `/api/status` and
`/api/clients/active` are all 404 on the version in a seat, `/api/clients/list`
gives the paired devices and not the one connected, and the log at its default
level records the encoder and the bitrate and never the client. So it is written
down where it is known instead. Sunshine's prep commands run when a stream starts
and again when it ends, with the client's size, framerate and HDR in their
environment and the name of the application it asked for, and `polyseat-session`
puts that in a file for as long as the stream lasts. The names are whatever an
application or a client is called, so control characters in them become spaces
and the size and framerate are written only when they are digits; a line break
in a name used to make the file invalid JSON, and the card lost what was being
played and by whom.

**The name comes from Sunshine now**, and this document said for a long time
that it could not. Since 2026.906.222525 Sunshine puts the paired name of the
client it has just verified into the environment of these commands, as
`SUNSHINE_CLIENT_NAME`, beside the size and the framerate. That is the version
a seat is pinned to, so the card says who rather than only where.

The address is still read, and not merely as a fallback for a seat on an older
build. It answers a different question - which machine, not which pairing - and
a name is chosen by whoever set the client up, so two of them can be the same.
It comes from the connection: Sunshine's control channel is a TCP connection
that lives exactly as long as the stream, so the peer on port 47989 or 48010 is
the machine somebody is sitting at. Reading that peer cost one mistake worth
recording: `ss` leaves out the state column when it has
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

**It also refuses a `br0` that is not its own.** `br0` is the most ordinary
bridge name there is, and the script never asked whether one existed: on a
machine with a `br0` made by hand or from libvirt's documentation it added a
second profile for the same interface, and the two raced at every boot. It now
stops before any seat is stopped when an interface called `br0` exists, when a
profile other than its own names it, or when a profile of its own is left from
an earlier run, which is what `--undo` is for. The one step that was left to
`set -e`, switching the old uplink profile's autoconnect off, rolls back like
every other step after the bridge exists. And the run's output no longer waits
for a child the script left behind: the interface showed a bridge run as in
progress, and refused the next one, for as long as such a child lived.

**The management bridge is made once.** Every autostarting seat starts in its
own goroutine and arranges its management interface, so on a host with no
usable bridge they all looked, all found nothing and all asked Incus to make
`polyseatbr0`, and every one but the first failed and was left without a path
back to the daemon. Looking and creating are one step under a lock now, and a
create that fails is looked at again, since a second daemon or somebody's own
`incus` command is outside the lock. `polyseat-uninstall --seats` removes
`polyseatbr0` after the seats, but only when Incus counts nobody still using
it, profiles included; the LAN bridge from `lan-bridge.sh` is kept and named
in its summary, because taking the host's own network away from a script that
may be running over it is how a machine ends up off it.

**A static address is parsed, not pattern matched.** The address and the
gateway are written into the seat's `systemd-networkd` file, and the only check
was that the address contained a slash, which an address followed by a line
break and any directive at all passed. Both go through `netip` now and have to
be the same family, and a gateway without an address is refused, since it
would be stored and shown and never written anywhere.

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
ones. An idle seat is no longer free either, since it holds a warm Steam:
1386 MB of memory and 493 MB of video memory, measured in seat vince on
2026-09-22. NVENC and CPU have plenty of headroom; VRAM gets tight with three modern
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
- **A compositor other than sway**, looked at on 2026-09-19 because HDR would
  be easier somewhere else. It would not: KWin's virtual backend refuses HDR
  outright, Mutter's `meta-output-virtual.c` never sets a supported EOTF,
  aquamarine's headless backend reports no capabilities at all, Smithay's
  colour management is a draft, and cosmic-comp has no headless backend and
  wants DRM master, which a seat may not hold. Read in the sources rather than
  in announcements. The switching cost would be paid whatever the answer: six
  files here speak sway's IPC in 21 places, and three protocols a replacement
  does not simply bring - `wlr-layer-shell` for waybar, squeekboard and the
  grid, `wlr-screencopy` for Sunshine's `capture = wlr`, and XWayland for
  Steam. The lesson written down at the time still holds: the compositor is not
  the bottleneck, the capture protocol is.
- **Windows seats**, worked through on 2026-09-20 in four shapes and dropped.
  Hyper-V GPU-P has no Vulkan in the guest, costs ten to twenty percent, and
  needs a guest driver matched to the host's after every host update; Windows
  Sandbox is ephemeral and effectively single instance; ASTER is native and
  commercial but brings neither provisioning nor isolation; and Windows VMs as
  seats on a Linux host, which is the only serious candidate of the four, gives
  up shared RAM and disk, breaks the library pool, and needs the private saves
  solved again for `%APPDATA%` and Steam's `userdata`, with a licence per seat
  and no GPU hotplug. What it would have bought is games that do not start
  under Proton, and not the online shooters people ask about, since anti-cheat
  still sees a VM. If it ever comes back it is a second class of seat beside
  the containers, not a parameter on one.
