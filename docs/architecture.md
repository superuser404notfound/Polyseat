# Architecture

This document records **what** is being built and, more importantly, **why it is
built this way** - so that later decisions do not run against insights that have
already been paid for.

## Constraints

Almost everything else follows from these:

- **Everyone plays on a Moonlight client**, nobody sits at the host's console.
  There are therefore no physical controllers on the host that would need
  assigning - only the virtual pads Sunshine creates inside each seat.
- **The host desktop (KDE/Wayland) keeps running normally** and must not be
  disturbed by any seat.
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
   libnvidia-container by hand.
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
driver. Optionally gamescope nested per game for scaling and FPS caps.

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
bridge, never the LAN address**, because the seats reach the LAN through macvlan
and a macvlan interface cannot talk to its own host; using the address Moonlight
uses produces a timeout that looks like Sunshine being down. And the seat's
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

**Taking part is per seat and off by default**, because this is the only place
that mounts host storage into a seat. Ticking the box applies straight away:
the disk device is hotplugged and the games are cloned in within seconds, no
provisioning run needed. The entry in Steam's `libraryfolders.vdf` is merged
into whatever is already there rather than replacing it, since a seat that has
run Steam already owns that file and it lists every library folder Steam knows.
A Steam that is running keeps its old list until the session restarts.

**Layout.** Under `library_dir`, `pool/steamapps/` is the canonical copy and
`seats/<name>/` is the one directory mounted into that seat, appearing as a
second Steam library folder at `/home/player/games`. Steam's own directory stays
where it is, so `compatdata/`, `shadercache/` and `downloading/` are per seat
without anything having to be symlinked away.

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
That is asked of the seat directly: a small `/proc` walk looks for the mount in
any process's mappings, open descriptors or working directory, because `lsof` is
not in a seat and a game that is running has its files open. A seat that is busy
keeps its copy and the waiting update is reported, so the interface shows
something pending instead of nothing happening. Overwriting a game under a
running client corrupts an install rather than improving one.

**The host's own library is watched, not imported once.** An imported library is
remembered and re-read on every pass, read only in both senses: the daemon takes
from it and never writes into it, and a game uninstalled there stays in the pool.
Without that, a game the host updates afterwards never reaches the seats and
every one of them downloads that update for itself.

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
has opened there. Not every title has one published: what is missing is
remembered for a week rather than asked for every minute.

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
heuristic. Select and Start together still override it by hand, a chord because
single buttons are taken, and the override holds until something goes fullscreen
or stops being: that covers a windowed game, and it means a forgotten override
cannot leave somebody holding a stick that does nothing. The gamepad is never
grabbed, so games see it exactly as before. Left stick points and right stick
scrolls, which is the way round the Windows tools do it and worth matching.

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

Three things carry it into place, because there are three ways an application
gets started in a seat. Sunshine's app list carries the two variables in its
`env` block, which Sunshine applies to what it launches and not to itself: the
same library loaded into Sunshine would be limiting the encoder, and loaded into
sway it would be limiting the desktop. The launcher passes everything it starts
through `mangohud` for the same reason. And flatpaks get a user wide override,
because a sandbox sees neither the seat's environment nor its home directory,
along with the MangoHud layer extension for whichever runtime version they use.

One thing had to be fixed before any of it worked. squeekboard registers a
virtual keyboard with the compositor when it starts but only gives it a layout
the first time it is shown, and until then sway hands out a zero length keymap.
MangoHud maps that without checking the length and dereferences the failure, so
every Vulkan application in the seat died with SIGSEGV before drawing a frame,
and opening the on-screen keyboard during a game killed the game. The session
therefore shows the keyboard once at startup and puts it away again, before
anybody has connected and while there is nobody to see it.

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

That is strictly nicer than what we do. Our broker infers ownership from a name
that Sunshine happens to write, which means it depends on a third party's
formatting staying stable.

**We are still not using it, for one concrete reason: vuinputd proxies
`/dev/uinput` only, not `/dev/uhid`.** And we measured in M3 that Sunshine
creates gamepads through inputtino as HID devices via uhid. So exactly the
device class that matters most for gaming would fall outside its isolation. The
author also lists Steam input support and force feedback as open gaps and calls
the project alpha.

Our approach covers keyboard, mouse, touch, pen and gamepad today, verified on
real hardware, and puts nothing in the event path between Sunshine and the
kernel. If vuinputd gains uhid support, replacing the broker's name correlation
with it would be a clear improvement and should be reconsidered then.

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
