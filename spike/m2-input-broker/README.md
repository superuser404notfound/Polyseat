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

## Open

- **The prototype polls** `/sys` twice a second. The daemon should hang off the
  host's udev monitor, and it has to know the container lifecycle: blind polling
  during a container stop once drove Incus into a hung "Stopping instance"
  (see M3).
- **Steam Input unverified.** Steam bundles its own SDL and also uses udev
  itself; the broker so far only passes through `event*` nodes, no `hidraw*`.
