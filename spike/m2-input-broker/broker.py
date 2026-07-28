#!/usr/bin/env python3
"""broker.py - prototype of the Polyseat input broker.

Sunshine creates its virtual input devices inside the seat, but `uinput` is not
namespaced: the kernel registers them globally, the host's udev creates the
nodes, and in the very seat that created them they are invisible. The broker
closes that gap. Three steps per device, each of them measured separately in M2:

  1. **Attach the node** to the container, world readable and writable.
     `mode=0666` is mandatory: without it the node arrives as `root:root 0660`
     and sway fails with "Failed to open device: Permission denied". How the
     node gets there is the backend's business, see ContainerBackend.
  2. **Write a udev database entry.** libudev reads properties not from `/sys`
     but from `/run/udev/data/`. Without `ID_INPUT=1` libinput ignores the
     device. The entry is *synthesised*, not copied from the host: the host's
     hide rule strips exactly that property.
  3. **Send a synthetic uevent**, otherwise sway only notices the device on its
     next restart. See `fakeudev.py`.

Classification (keyboard/mouse/pad) is derived from the capability bitmaps in
`/sys`, not from the host's udev properties, because those are not set at all
for Sunshine's devices and are deliberately stripped for Polyseat's own.

**Device to seat assignment:** through the seat tag in the device name. Sunshine
reads `XDG_SEAT` and appends the seat name as soon as the seat is not "seat0",
turning "Keyboard passthrough" into "Keyboard passthrough (seat1)". A patch or
LD_PRELOAD shim, as originally planned, is not needed; the feature already
exists.

The broker requires the tag by default. `--tag ""` turns the check off and falls
back to plain name matching, which is only defensible for a single seat; with
several it would be guesswork.

    ./broker.py --seat seat1
"""

import argparse
import json
import os
import re
import subprocess
import sys
import time

SYS_INPUT = "/sys/class/input"

# Capability bits that suffice for classification.
EV_KEY, EV_REL, EV_ABS = 0x01, 0x02, 0x03
ABS_X, ABS_Y = 0x00, 0x01
BTN_LEFT, BTN_GAMEPAD = 0x110, 0x130
BTN_TOOL_PEN, BTN_TOUCH, BTN_TOOL_FINGER = 0x140, 0x14A, 0x145
BTN_STYLUS = 0x14B
KEY_Q = 16          # sits in the block of letter keys


def read(path):
    try:
        with open(path) as fh:
            return fh.read().strip()
    except OSError:
        return ""


def bitmap(path):
    """Capability bitmaps are a sequence of hex words, highest first."""
    words = read(path).split()
    value = 0
    for i, word in enumerate(reversed(words)):
        value |= int(word or "0", 16) << (i * 64)
    return value


def classify(sysdev):
    """Reimplements what udev's input_id does.

    The order is not arbitrary: Sunshine's "Mouse passthrough (absolute)"
    reports EV_ABS rather than EV_REL. Checking only for EV_REL makes it look
    like a keyboard, and sway did indeed classify it as one, leaving the pointer
    dead.
    """
    ev = bitmap(f"{sysdev}/capabilities/ev")
    key = bitmap(f"{sysdev}/capabilities/key")
    abs_ = bitmap(f"{sysdev}/capabilities/abs")
    props = ["ID_INPUT=1"]

    has = lambda bits, bit: bool(bits & (1 << bit))
    is_pointer = False

    if has(ev, EV_REL) and has(key, BTN_LEFT):
        props.append("ID_INPUT_MOUSE=1")
        is_pointer = True

    if has(ev, EV_ABS) and has(abs_, ABS_X) and has(abs_, ABS_Y):
        if has(key, BTN_STYLUS) or has(key, BTN_TOOL_PEN):
            props.append("ID_INPUT_TABLET=1")
            is_pointer = True
        elif has(key, BTN_GAMEPAD):
            props.append("ID_INPUT_JOYSTICK=1")
        elif has(key, BTN_LEFT):
            props.append("ID_INPUT_MOUSE=1")     # absolute pointer
            is_pointer = True
        elif has(key, BTN_TOOL_FINGER):
            props.append("ID_INPUT_TOUCHPAD=1")
            is_pointer = True
        elif has(key, BTN_TOUCH):
            props.append("ID_INPUT_TOUCHSCREEN=1")
            is_pointer = True
    elif has(ev, EV_ABS) and has(key, BTN_GAMEPAD):
        props.append("ID_INPUT_JOYSTICK=1")

    if has(ev, EV_KEY) and has(key, KEY_Q):
        props += ["ID_INPUT_KEY=1", "ID_INPUT_KEYBOARD=1"]
    elif has(ev, EV_KEY) and not is_pointer and len(props) == 1:
        props.append("ID_INPUT_KEY=1")

    # Drop duplicates, keep the order.
    return list(dict.fromkeys(props))


def scan(pattern, tag=None):
    """All virtual event devices whose name matches the pattern.

    The filter on `/devices/virtual/` is the real safeguard: only what was
    created through uinput or uhid may enter a seat at all. A pure name filter
    would be dangerous, since "Controller" would also match the host's ASRock
    LED controller, which has no business in any seat.
    """
    rx = re.compile(pattern, re.IGNORECASE)
    needle = f"({tag})" if tag else None
    found = {}
    for entry in os.listdir(SYS_INPUT):
        if not entry.startswith("event"):
            continue
        sysdev = f"{SYS_INPUT}/{entry}"
        if "/devices/virtual/" not in os.path.realpath(sysdev):
            continue
        name = read(f"{sysdev}/device/name")
        if not name or not rx.search(name):
            continue
        # The seat tag decides who owns the device. Without it a second seat
        # would collect the first one's devices as well.
        if needle and needle not in name:
            continue
        dev = read(f"{sysdev}/dev")
        if not dev:
            continue
        major, minor = dev.split(":")
        found[entry] = {
            "node": entry,
            "name": name,
            "major": int(major),
            "minor": int(minor),
            "syspath": os.path.realpath(sysdev).removeprefix("/sys"),
            "props": classify(os.path.realpath(f"{sysdev}/device")),
        }
    return found


def state_path(seat):
    """The broker has to remember what it attached across a restart of its own.
    Otherwise it cannot send a `remove` event while cleaning up, because the
    device is long gone from the host and DEVPATH and device numbers can no
    longer be read. Without that event sway keeps dead input devices in its
    list."""
    base = os.environ.get("XDG_RUNTIME_DIR") or "/tmp"
    return f"{base}/polyseat-broker-{seat}.json"


def load_state(seat):
    try:
        with open(state_path(seat)) as fh:
            return json.load(fh)
    except (OSError, ValueError):
        return {}


def save_state(seat, devices):
    try:
        with open(state_path(seat), "w") as fh:
            json.dump(devices, fh)
    except OSError:
        pass


# --------------------------------------------------------- container backend
#
# Everything runtime specific lives behind this seam. The broker itself never
# mentions Incus; it only needs four operations from a backend. Swapping in a
# different container runtime, or replacing the whole mechanism with something
# like vuinputd, is then one class rather than a rewrite.
#
# Only IncusBackend exists and only it has been exercised, so the seam is a
# claim about the shape of the problem, not a tested abstraction over several
# runtimes. What a second backend would have to solve is written down at the
# bottom of this section.


class ContainerBackend:
    """Contract every backend has to fulfil.

    attach_node   make the host device node available inside the container,
                  world readable and writable, without restarting it
    detach_node   remove it again
    attached_nodes  which nodes this backend currently has attached, so
                  orphans from an earlier run can be found
    run           run a command inside the container as root, optionally
                  feeding it stdin
    """

    def attach_node(self, container, node, major, minor):
        raise NotImplementedError

    def detach_node(self, container, node):
        raise NotImplementedError

    def attached_nodes(self, container):
        raise NotImplementedError

    def run(self, container, argv, stdin=None):
        raise NotImplementedError


class IncusBackend(ContainerBackend):
    """Incus, which supports device hotplug into running containers.

    That was the deciding property when the runtime was chosen: Podman and
    Docker cannot add devices to a running container, so a backend for those
    would have to create the node itself with mknod inside the container, which
    needs CAP_MKNOD and a matching device cgroup rule. See the note below.
    """

    PREFIX = "in-"      # marks the devices this broker owns

    def _incus(self, *args, check=True):
        return subprocess.run(["incus", *args], capture_output=True, text=True,
                              check=check)

    def attach_node(self, container, node, major, minor):
        # mode=0666 is not optional: without it the node arrives as
        # root:root 0660 and the compositor cannot open it.
        self._incus("config", "device", "add", container, f"{self.PREFIX}{node}",
                    "unix-char", f"source=/dev/input/{node}",
                    f"path=/dev/input/{node}", "mode=0666", "required=false",
                    check=False)

    def detach_node(self, container, node):
        self._incus("config", "device", "remove", container,
                    f"{self.PREFIX}{node}", check=False)

    def attached_nodes(self, container):
        listing = self._incus("config", "device", "list", container, check=False)
        return [name[len(self.PREFIX):] for name in listing.stdout.split()
                if name.startswith(self.PREFIX)]

    def run(self, container, argv, stdin=None):
        return subprocess.run(["incus", "exec", container, "--", *argv],
                              input=stdin, text=True, capture_output=True,
                              check=False)


# A second backend, for a runtime without device hotplug, would have to:
#
#   attach_node     enter the container's mount namespace and mknod the node
#                   itself, which needs CAP_MKNOD and a device cgroup rule
#                   allowing that major:minor
#   detach_node     unlink it again
#   attached_nodes  keep its own record, since there is no runtime to ask
#   run             nsenter into the container's namespaces
#
# The remaining two steps, the udev database entry and the synthetic uevent,
# are backend independent: they only need `run`.


# ------------------------------------------------------------------ the steps

# Where fakeudev.py is expected inside the container. It has to run there
# rather than on the host, because libudev checks the sender's credentials
# after translating them into the container's user namespace.
FAKEUDEV = "/root/fakeudev.py"


def attach(backend, seat, dev):
    node, minor = dev["node"], dev["minor"]
    print(f"  + {node:<10} {dev['name']}")
    print(f"    {' '.join(dev['props'])}")

    # 1) Make the node available inside the container.
    backend.attach_node(seat, node, dev["major"], minor)

    # 2) Synthesise the udev database entry. libudev reads properties from
    #    there, not from /sys, and without ID_INPUT libinput ignores the device.
    entry = "I:1\n" + "".join(f"E:{p}\n" for p in dev["props"]) + "G:seat\nQ:seat\nV:1\n"
    backend.run(seat, ["sh", "-c",
                       f"mkdir -p /run/udev/data && cat > /run/udev/data/c{dev['major']}:{minor}"],
                stdin=entry)

    # 3) Send the event, so that clients already running notice the device.
    argv = [FAKEUDEV, "add", dev["syspath"], "--subsystem", "input",
            "--devname", f"input/{node}",
            "--major", str(dev["major"]), "--minor", str(minor)]
    for p in dev["props"]:
        argv += ["--prop", p]
    backend.run(seat, argv)


def detach(backend, seat, node, dev):
    print(f"  - {node:<10} {dev['name']}")
    # Event first, while the node still exists inside the container.
    backend.run(seat, [FAKEUDEV, "remove", dev["syspath"], "--subsystem", "input",
                       "--devname", f"input/{node}",
                       "--major", str(dev["major"]), "--minor", str(dev["minor"])])
    backend.detach_node(seat, node)
    backend.run(seat, ["rm", "-f", f"/run/udev/data/c{dev['major']}:{dev['minor']}"])


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--seat", required=True, help="name of the container")
    ap.add_argument("--match",
                    default=r"passthrough|x-?box|dualsense|dualshock|nintendo|"
                            r"sunshine|gamepad|joystick|controller",
                    help="regular expression on the device name. Only applies "
                         "to virtual devices. Sunshine calls keyboard and mouse "
                         "'... passthrough', while gamepads are named after the "
                         "emulated model (e.g. 'Xbox One')")
    ap.add_argument("--tag", default=None,
                    help="seat tag the device name must carry (default: the "
                         "seat name). Sunshine appends it when XDG_SEAT is set. "
                         "\"\" disables the check.")
    ap.add_argument("--interval", type=float, default=0.5)
    args = ap.parse_args()

    backend = IncusBackend()

    if os.geteuid() != 0 and not os.access("/run/udev/data", os.R_OK):
        print("Note: without root some udev data is unreadable.", file=sys.stderr)

    tag = args.seat if args.tag is None else (args.tag or None)
    if tag:
        print(f"Broker running for seat '{args.seat}', requiring tag '({tag})'.")
    else:
        print(f"Broker running for seat '{args.seat}' WITHOUT a tag check, "
              f"which is only defensible for a single seat.")
    print("Ctrl-C stops it.\n")

    # Clean up orphaned attachments from earlier runs. Sunshine instances that
    # crash or restart leave their devices behind on the host; the matching
    # attachments in the seat then point at nothing and keep showing up in sway
    # as dead input devices.
    stale = 0
    remembered = load_state(args.seat)
    for node in backend.attached_nodes(args.seat):
        sysdev = f"{SYS_INPUT}/{node}"
        name = read(f"{sysdev}/device/name")
        if not name or (tag and f"({tag})" not in name):
            print(f"  ~ {node:<10} orphaned, removing")
            if node in remembered:
                detach(backend, args.seat, node, remembered[node])
            else:
                # Without a memory only the attachment can go; sway then keeps
                # the dead device in its list until its next restart.
                backend.detach_node(args.seat, node)
            stale += 1
    if stale:
        print(f"{stale} orphaned attachment(s) removed.\n")

    known = {}
    try:
        while True:
            current = scan(args.match, tag)
            for node, dev in current.items():
                if node not in known:
                    attach(backend, args.seat, dev)
            for node, dev in list(known.items()):
                if node not in current:
                    detach(backend, args.seat, node, dev)
            if current != known:
                save_state(args.seat, current)
            known = current
            time.sleep(args.interval)
    except KeyboardInterrupt:
        print("\nStopped. Attached devices stay in place.")


if __name__ == "__main__":
    main()
