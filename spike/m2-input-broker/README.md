# M2 - the input broker

**Goal:** keyboard, mouse and pad from the Moonlight client reach the seat
session.

**Status: solved and confirmed on real hardware** (2026-07-28). sway inside the
seat lists Sunshine's devices as pointers and keyboards, both at startup and on
hotplug at runtime. From an iPhone the mouse pointer can be moved and text typed
into the session's terminal.

## The problem

`uinput` is not namespaced. Sunshine creates its virtual devices inside the
seat, the kernel registers them globally, the host's udev creates the nodes, and
in the very seat that created them they are invisible. sway had zero input
devices.

## The solution: three steps, each measured separately

For every device:

1. **Attach the node** with `incus config device add ... unix-char`, **and
   `mode=0666` is mandatory**. Without it the node arrives as `root:root 0660`
   and sway fails with `Failed to open device: Permission denied`.
2. **Write a udev database entry** into `/run/udev/data/cMAJ:MIN` inside the
   container. libudev reads properties from there, not from `/sys`. Without
   `ID_INPUT=1` libinput ignores the device completely.
3. **Send a synthetic uevent** (`fakeudev.py`), otherwise sway only notices the
   device on its next restart.

Steps 1 and 2 are enough for devices that already exist when sway starts. Step 3
makes hotplug work, and hotplug is the normal case, because Sunshine only
creates its devices once a client connects.

## Why no full fake-udev is needed

The assumption from M0 and M1 was that a shim replicating libudev would be
required. It is not, because enumeration already works: **libudev walks `/sys`
for that, and the container sees `/sys` in full** (`udevadm trigger --dry-run`
inside the container lists the host's devices). Only the *notification* is
missing, because the uevent netlink is bound to the network namespace.

`fakeudev.py` therefore simply puts a message on the udev multicast group
**inside** the container. udevd is bypassed and the libudev clients (libinput,
SDL) listen to it directly. That is roughly 40 lines instead of a reimplemented
udev.

Two pitfalls along the way:

- The message needs the correct **MurmurHash2 of the subsystem** in its header,
  or clients filter it away.
- The sender must run **as root inside the container**. libudev discards
  messages whose sender is not uid 0, and the credentials are translated into
  the container's user namespace. A host process that only enters the network
  namespace via `setns` fails that check.

## Procedure

```
./10-uevent-test.sh        # proves: no uevent reaches the container
./11-udevdb-test.sh        # proves: the database entry alone is not enough
./12-sway-enumeration.sh   # proves: enumeration works, permissions were missing
./20-broker.sh             # start the broker; must run during streaming
```

## Findings

- **libinput ignores gamepads entirely.** A pad never shows up in
  `swaymsg -t get_inputs`, and that is correct: games read it directly through
  evdev. Anyone testing the input chain against sway has to use a keyboard or a
  mouse. Half an evening went into using a gamepad as the probe.
- **The broker has to classify devices itself.** Sunshine's devices get no
  `ID_INPUT` properties from the host's udev at all, and Polyseat's own are
  deliberately stripped by the hide rule. So the broker reads the capability
  bitmaps from `/sys` and reimplements what `input_id` does.
- **Order matters in the classification.** "Mouse passthrough (absolute)"
  reports `EV_ABS` rather than `EV_REL`. Checking only for `EV_REL` makes it look
  like a keyboard, sway then classified it as one, and the absolute pointer
  stayed dead.
- **The host's hide rule has to cover Sunshine's names.** Until M2 it only
  covered `polyseat:*`. That the "passthrough" devices did not show up on the
  KDE desktop anyway was down to missing `ID_INPUT` properties for reasons that
  remain unclear, and that is not something to rely on.

## Device to seat assignment: solved, without a patch

Originally noted as the big open point: Sunshine's devices are named identically
in every seat, so there would be nothing to tell them apart. The plan was a
Sunshine patch or an LD_PRELOAD shim around `UI_DEV_SETUP`.

**Neither is necessary, because Sunshine can already do it.** It reads
`XDG_SEAT` and appends the seat name to its virtual device names as soon as the
seat is not `seat0`. With `Environment=XDG_SEAT=seat1` in the Sunshine unit the
result is (measured on 2026-07-28):

```
Keyboard passthrough (seat1)               ID_INPUT_KEYBOARD
Mouse passthrough (seat1)                  ID_INPUT_MOUSE
Mouse passthrough (seat1) (absolute)       ID_INPUT_MOUSE
Touch passthrough (seat1)                  ID_INPUT_TOUCHSCREEN
Pen passthrough (seat1)                    ID_INPUT_TABLET
Sunshine X-Box One (virtual) pad (seat1)   ID_INPUT_JOYSTICK
```

The broker therefore requires the tag by default: it only touches devices whose
name contains `(<seat>)`. That makes the assignment exact instead of guessed,
and the blocker for multiple seats disappeared before it ever became one.

The lesson: **check whether the feature already exists before patching.** Half a
day of shim tinkering would have been for nothing.

## Attribution: structural for uinput, by name for uhid

The first version answered "which seat does this device belong to" by reading
the seat tag out of the device name. That trusts a string the creating process
chose. Any process able to open `/dev/uinput`, on the host or in another seat,
could write a name carrying somebody else's tag and have the device delivered
there. On this host that is not hypothetical: the desktop user is in the `input`
group.

`device_owner.py` answers it structurally instead, using two facts:

* **A uinput device lives exactly as long as the descriptor that created it.**
  Close the descriptor and the kernel destroys the device. So while a device
  exists, its creator still holds an open descriptor that can be found.
* **`UI_GET_SYSNAME` asks a descriptor which device it created.** Once the
  descriptor is found, the mapping is not a guess.

`pidfd_getfd()` duplicates the descriptor out of the foreign process, and the
owner's cgroup gives the container name. Nothing in that chain depends on what
the creator wrote into the name.

Demonstrated: a host process creating a device named exactly
`Keyboard passthrough (seat1)` is refused, while seat1 keeps its real devices.

```
! event21    refused: created on the host, not in a container
```

**Two practical notes.** Devices have to be found by device number rather than
by path, because inside a container `/dev/uinput` is a different path and
`lsof /dev/uinput` shows nothing while seats are streaming. And the broker now
needs root, since reading foreign descriptors is privileged.

### Gamepads: correlated, not proven

uhid has no counterpart to `UI_GET_SYSNAME`, in fact no ioctls at all, so a
descriptor cannot be asked what it created. What can be used instead is that
**uhid ties one device to one descriptor exactly like uinput does**:
`UHID_CREATE2` acts on the descriptor and closing it destroys the device. So a
gamepad that appears belongs to whichever container opened a uhid descriptor
since the previous cycle.

The broker uses that, and cross-checks it against the name tag. If the
descriptor says one seat and the name claims another, the device is refused and
the disagreement logged. If several containers opened descriptors at once, or
none did, it falls back to the name and labels the attribution
`name tag only, unverified` rather than pretending.

**This is correlation by ordering, not a structural fact.** A determined
attacker could race it. It is a large improvement over trusting a string and it
is not the same thing as the uinput case, which is why the log distinguishes
`creator verified` from `uhid descriptor correlated`.

Being exact here needs a proxy that sees the creation itself, which is the open
piece across this whole problem space: neither Wolf nor vuinputd has it either.
A forged gamepad is also the least dangerous forgery, a fake pad in somebody's
game rather than a keyboard in their session.

**Measured on 2026-07-28 with a controller on each seat.** Only one of the two
pads went through uhid at all:

```
seat1  Sunshine X-Box One (virtual) pad (seat1)            creator verified
seat2  Sunshine PS5 (virtual) pad (seat2)                  uhid descriptor correlated
seat2  Sunshine PS5 (virtual) pad (seat2) Motion Sensors   uhid descriptor correlated
seat2  Sunshine PS5 (virtual) pad (seat2) Touchpad         uhid descriptor correlated
```

Two things worth keeping. Xbox emulation uses uinput and therefore gets the
strong attribution for free, so the heuristic only applies to DualSense-style
pads. And one DualSense produces **three** input devices from a single HID
device, so the uhid side is a one-to-many mapping rather than one node per
creation.

## The container backend is a seam

Everything runtime specific sits behind `ContainerBackend`, four operations:

```
attach_node      make the host node available inside the container, 0666,
                 without restarting it
detach_node      remove it again
attached_nodes   what this broker currently has attached, so orphans from an
                 earlier run can be found
run              run a command inside the container as root, optionally with
                 stdin
```

The broker itself no longer mentions Incus anywhere. Steps two and three, the
udev database entry and the synthetic uevent, are backend independent: they only
need `run`.

The point is not elegance. If vuinputd grows uhid support, or if a runtime other
than Incus becomes interesting, the change is one class rather than a rewrite.

**Only `IncusBackend` exists and only it has been exercised**, so this is a claim
about the shape of the problem, not a tested abstraction over several runtimes.
A backend for a runtime without device hotplug, meaning Docker or Podman, would
have to create the node itself with `mknod` inside the container, which needs
`CAP_MKNOD` and a matching device cgroup rule. That is written down in the code
next to the interface.

## Open

- **The prototype polls** `/sys` twice a second. The daemon should hang off the
  host's udev monitor, and it has to know the container lifecycle: blind polling
  during a container stop once drove Incus into a hung "Stopping instance"
  (see M3).
- **Steam Input unverified.** Steam bundles its own SDL and also uses udev
  itself; the broker so far only passes through `event*` nodes, no `hidraw*`.
