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

Root is btrfs. Instead of OverlayFS: `/srv/steam-pool` as a subvolume, and **one
writable snapshot per seat**. Copy-on-write means five seats cost storage once,
and Steam sees a fully writable library. `compatdata/` and `shadercache/` are
symlinked to seat-private storage - otherwise they land in the snapshot and eat
the deduplication. Maintain the pool centrally, re-snapshot the seats
periodically.

The licensing reality remains: the same game played simultaneously needs two
copies in two accounts. No software solves that.

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
