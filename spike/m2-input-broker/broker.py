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
import stat as statmod
import subprocess
import sys
import time

import device_owner
import uhid_observer

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


def scan(pattern=None):
    """Every virtual event device, or only those whose name matches a pattern.

    Unfiltered by default, and that is the point. This used to carry a list of
    name patterns for what Sunshine creates, which quietly decided what could
    be attributed at all: a device the list did not name was never even
    considered, so it stayed on the host and never reached its seat. That is
    how antimicrox, which a seat runs to turn a gamepad into a mouse, ended up
    driving the host's cursor instead of the seat's.

    Names decide nothing now. What may enter a seat is decided by attribute(),
    structurally, and a device created on the host is refused there however it
    is called. The old worry, that "Controller" would also match the host's
    ASRock LED controller, is answered twice over: that device is not virtual,
    and it was not created by a container.

    The filter on `/devices/virtual/` stays and is the real safeguard: only
    what was made through uinput or uhid may enter a seat at all. A pattern can
    still be passed to narrow things while debugging.
    """
    rx = re.compile(pattern, re.IGNORECASE) if pattern else None
    found = {}
    for entry in os.listdir(SYS_INPUT):
        if not entry.startswith("event"):
            continue
        sysdev = f"{SYS_INPUT}/{entry}"
        if "/devices/virtual/" not in os.path.realpath(sysdev):
            continue
        name = read(f"{sysdev}/device/name")
        if not name or (rx and not rx.search(name)):
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
            "sysname": device_owner.sysname_of_node(entry),
            "props": classify(os.path.realpath(f"{sysdev}/device")),
        }
    return found


# Devices already reported as refused, so the log says it once rather than
# twice a second. Keyed by the sysfs path rather than the node name: the kernel
# reuses event numbers, and a stale entry would then silence the refusal for a
# different device that happened to get the same number.
_reported = set()

# uhid descriptors seen in the previous cycle. A gamepad that appears belongs to
# whichever container opened a descriptor since then, because uhid ties one
# device to one descriptor just as uinput does.
_uhid_seen = {}


def attribute(candidates, seat, tag):
    """Decide which of the candidates belong to this seat.

    Two sources of truth, in order of trustworthiness:

    **Structural, for uinput devices.** The creating descriptor is still open,
    because a uinput device dies with it, so `device_owner` can ask that
    descriptor which device it made and read the owner's cgroup. Nothing here
    depends on what the creator wrote into the device name. A device whose
    creator sits on the host rather than in a container is refused outright,
    which closes the case of any host process in the `input` group producing a
    device with a convincing name.

    **The seat tag, for uhid devices.** Gamepads are created through
    `/dev/uhid`, which has no ioctls at all, so there is no way to ask a
    descriptor what it made. Until a proxy sees the creation itself, these keep
    relying on the name that Sunshine writes when XDG_SEAT is set.
    """
    global _uhid_seen
    owners = device_owner.owners()

    holders = device_owner.uhid_holders()
    fresh = {k: v for k, v in holders.items() if k not in _uhid_seen}
    # Distinct containers that opened a uhid descriptor since the last cycle.
    candidates_from_fresh = {c for c in fresh.values() if c}
    _uhid_seen = holders

    mine = {}
    for node, dev in candidates.items():
        owner = owners.get(dev["sysname"], "unknown")
        if owner == "unknown":
            # No uinput descriptor claims it, so it came through uhid.
            claimed = tag and f"({tag})" in dev["name"]

            # Best answer first: the observer saw the kernel create it and
            # recorded the calling process. That is a fact, not a correlation.
            observed = uhid_observer.owner_of_node(node)
            if observed is None:
                dev["attribution"] = "host"
                if dev["syspath"] not in _reported:
                    _reported.add(dev["syspath"])
                    print(f"  ! {node:<10} refused: created on the host, not in a "
                          f"container ({dev['name']})")
                continue
            if observed != "unknown":
                dev["attribution"] = "uhid-observed"
                if observed == seat:
                    mine[node] = dev
                elif claimed and dev["syspath"] not in _reported:
                    _reported.add(dev["syspath"])
                    print(f"  ! {node:<10} refused: name claims ({seat}) but the "
                          f"kernel says '{observed}' created it")
                continue

            # The observer did not see it, usually because it was created
            # before the observer started. Fall back to the correlation.
            if len(candidates_from_fresh) == 1:
                # Exactly one container just opened a descriptor: correlate.
                correlated = next(iter(candidates_from_fresh))
                dev["attribution"] = "uhid-correlated"
                if correlated == seat:
                    if claimed or not tag:
                        mine[node] = dev
                    elif dev["syspath"] not in _reported:
                        _reported.add(dev["syspath"])
                        print(f"  ! {node:<10} refused: descriptor says '{seat}' "
                              f"but the name claims otherwise ({dev['name']})")
                elif claimed and dev["syspath"] not in _reported:
                    _reported.add(dev["syspath"])
                    print(f"  ! {node:<10} refused: name claims ({seat}) but the "
                          f"uhid descriptor belongs to '{correlated}'")
            else:
                # Nothing new, or several at once: fall back to the name and say so.
                dev["attribution"] = "tag"
                if claimed:
                    mine[node] = dev
        elif owner is None:
            # Created by a process on the host. Never belongs to a seat.
            dev["attribution"] = "host"
            if dev["syspath"] not in _reported:
                _reported.add(dev["syspath"])
                print(f"  ! {node:<10} refused: created on the host, not in a "
                      f"container ({dev['name']})")
        else:
            dev["attribution"] = "owner"
            if owner == seat:
                mine[node] = dev
            elif f"({seat})" in dev["name"] and dev["syspath"] not in _reported:
                # Claims our tag but was created elsewhere. Exactly what the
                # structural check exists for.
                _reported.add(dev["syspath"])
                print(f"  ! {node:<10} refused: name claims ({seat}) but the "
                      f"creator is '{owner}'")
    return mine


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

    def push(self, container, local_path, remote_path):
        """Place a file inside the container, executable."""
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

    def push(self, container, local_path, remote_path):
        self._incus("file", "push", local_path, f"{container}{remote_path}",
                    "--mode", "0755", check=False)


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


def hidraw_of(node):
    """The hidraw node of the same device, when it has one.

    A gamepad made through uhid appears twice: as an event device under
    /dev/input, and as a raw HID node under /dev. They are two views of one
    device and they sit under the same directory in sysfs, which is what this
    walks up to find.
    """
    path = os.path.realpath(f"{SYS_INPUT}/{node}")

    while path and path != "/sys":
        listing = os.path.join(path, "hidraw")

        if os.path.isdir(listing):
            for entry in sorted(os.listdir(listing)):
                if entry.startswith("hidraw"):
                    return entry

        path = os.path.dirname(path)

    return None


def seal_path(path):
    """Take one device node away from the host desktop."""
    try:
        st = os.stat(path)
    except OSError:
        return False

    acl = False
    if os.path.exists("/usr/bin/getfacl"):
        try:
            out = subprocess.run(["getfacl", "-p", path], check=False,
                                 capture_output=True, text=True).stdout
            acl = any(line.startswith("user:") and not line.startswith("user::")
                      for line in out.splitlines())
        except OSError:
            acl = False

    if (st.st_uid == 0 and st.st_gid == 0
            and statmod.S_IMODE(st.st_mode) == 0o600 and not acl):
        return False

    try:
        os.chown(path, 0, 0)
        os.chmod(path, 0o600)
    except OSError as exc:
        print(f"  ! {path} could not be taken off the host: {exc}")
        return False

    subprocess.run(["setfacl", "-b", path], check=False,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    return True


def seal(node):
    """Take a device that belongs to a seat away from the host desktop.

    The udev rule does this at creation time, which is the right moment, but it
    can only decide by name there. It cannot ask who created the device,
    because systemd-udevd runs its workers behind a syscall filter that blocks
    pidfd_open and pidfd_getfd, and those are exactly what reading a foreign
    descriptor needs. Measured rather than assumed: the same helper answers
    "container" from a shell and "unknown" under udevd's filter.

    So the structural answer is enforced here instead, by the component that
    already has it. A name list is an allowlist of the tools somebody thought
    of, and it failed the first time a seat ran something new.

    **Both nodes, not only the event one.** A gamepad made through uhid also
    appears as a raw HID node, and that is the one Steam reads a DualSense
    through. The event device was being pinned to root while its hidraw sibling
    kept an access control entry for the desktop user, put there by Sunshine's
    own udev rules, which are written for a Sunshine running on the machine
    rather than in a container. So the seat's controller was reaching the host's
    Steam the whole time, through a door nobody had looked at.

    The permissions alone are not the test either. logind grants the desktop
    user an entry through the uaccess tag, and that survives a mode change, so
    a node can read root:root 0600 and still be open to somebody.
    """
    changed = seal_path(f"/dev/input/{node}")

    raw = hidraw_of(node)
    if raw:
        changed = seal_path(f"/dev/{raw}") or changed

    return changed


def attach(backend, seat, dev):
    node, minor = dev["node"], dev["minor"]
    how = {"owner": "creator verified",
           "uhid-observed": "creator observed at creation",
           "uhid-correlated": "uhid descriptor correlated",
           "tag": "name tag only, unverified"}.get(
        dev.get("attribution", ""), dev.get("attribution", ""))
    print(f"  + {node:<10} {dev['name']}")
    print(f"    {how}: {' '.join(dev['props'])}")

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
    ap.add_argument("--match", default=None,
                    help="regular expression on the device name, for narrowing "
                         "things down while debugging. Empty by default: every "
                         "virtual device is considered and attribution decides "
                         "structurally which seat it belongs to, if any. A name "
                         "list here used to decide what could be seen at all, "
                         "which silently excluded anything nobody had thought "
                         "of yet")
    ap.add_argument("--tag", default=None,
                    help="seat tag the device name must carry (default: the "
                         "seat name). Sunshine appends it when XDG_SEAT is set. "
                         "\"\" disables the check.")
    ap.add_argument("--interval", type=float, default=0.5)
    args = ap.parse_args()

    backend = IncusBackend()

    # fakeudev has to live inside the seat, because libudev checks the sender's
    # credentials after translating them into the container's user namespace.
    # Copying it here rather than in a wrapper script keeps the broker usable
    # as a plain systemd unit.
    local_fakeudev = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                  "fakeudev.py")
    if os.path.exists(local_fakeudev):
        backend.push(args.seat, local_fakeudev, FAKEUDEV)
    else:
        print(f"warning: {local_fakeudev} not found, the seat needs {FAKEUDEV} "
              f"to notice hotplug", file=sys.stderr)

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
            current = attribute(scan(args.match), args.seat, tag)
            for node, dev in current.items():
                # Before attaching, and on every pass afterwards: a udev
                # retrigger puts the permissions back to what the name rules
                # decided, and for a device no pattern matches that is open.
                if seal(node):
                    print(f"  * {node:<10} taken off the host desktop ({dev['name']})")
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
