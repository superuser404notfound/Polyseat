#!/usr/bin/env python3
"""device_owner.py - works out which container really created an input device.

The broker used to answer that question by reading the seat tag out of the
device name, which Sunshine writes because `XDG_SEAT` is set. That works, but it
trusts a string that the creating process chose. Any process that can open
`/dev/uinput`, on the host or in another seat, could write a name carrying
somebody else's tag and have the device delivered there.

This module answers the question structurally instead, using two facts:

* **A uinput device lives exactly as long as the file descriptor that created
  it.** Close the descriptor and the kernel destroys the device. So while the
  device exists, its creator is still holding an open descriptor, and that
  descriptor can be found.
* **`UI_GET_SYSNAME` asks a descriptor which device it created.** So once the
  descriptor is found, the mapping is not a guess.

The remaining step is to duplicate the descriptor out of the foreign process,
which `pidfd_getfd()` does, and to read the owner's cgroup for the container
name.

**This covers uinput only.** Gamepads are created through `/dev/uhid`, and uhid
offers no equivalent of `UI_GET_SYSNAME`, in fact no ioctls at all. There is no
way to ask a uhid descriptor what it made, so attribution for gamepads needs a
proxy that sees the creation itself. Until that exists, gamepads keep using the
name tag.

Needs root: reading other processes' descriptors and `pidfd_getfd` are
privileged.

    sudo ./device_owner.py            # print the mapping for every device
    sudo ./device_owner.py --udev event27
                                      # one line for udev: container, host or
                                      # unknown
"""

import ctypes
import json
import os
import re
import stat
import sys

# Both are fixed misc devices.
UINPUT_MAJOR, UINPUT_MINOR = 10, 223
UHID_MAJOR, UHID_MINOR = 10, 239

SYS_PIDFD_OPEN = 434
SYS_PIDFD_GETFD = 438

_libc = ctypes.CDLL("libc.so.6", use_errno=True)
_libc.syscall.restype = ctypes.c_long


def _syscall(number, *args):
    res = _libc.syscall(ctypes.c_long(number), *[ctypes.c_long(a) for a in args])
    if res < 0:
        raise OSError(ctypes.get_errno(), os.strerror(ctypes.get_errno()))
    return res


def _ui_get_sysname_op(length):
    """_IOC(_IOC_READ, 'U', 44, length), as a signed 32 bit value."""
    op = (2 << 30) | (length << 16) | (ord("U") << 8) | 44
    return op - (1 << 32) if op >= (1 << 31) else op


def sysname_of_fd(fd):
    """Ask a uinput descriptor which device it created, e.g. 'input200'."""
    import fcntl
    buf = bytearray(64)
    fcntl.ioctl(fd, _ui_get_sysname_op(len(buf)), buf, True)
    return buf.split(b"\0")[0].decode()


def descriptors(major, minor):
    """Every (pid, fd) in the system pointing at that character device.

    Matched by device number rather than by path: inside a container the node
    is a different path, and tools that match on "/dev/uinput" miss it. This is
    why `lsof /dev/uinput` shows nothing while seats are streaming.
    """
    for entry in os.listdir("/proc"):
        if not entry.isdigit():
            continue
        pid = int(entry)
        fd_dir = f"/proc/{pid}/fd"
        try:
            names = os.listdir(fd_dir)
        except OSError:
            continue                      # process gone, or not ours to read
        for name in names:
            try:
                st = os.stat(f"{fd_dir}/{name}")
            except OSError:
                continue
            if not stat.S_ISCHR(st.st_mode):
                continue
            if (os.major(st.st_rdev), os.minor(st.st_rdev)) == (major, minor):
                yield pid, int(name)


def uinput_descriptors():
    return descriptors(UINPUT_MAJOR, UINPUT_MINOR)


def uhid_descriptors():
    return descriptors(UHID_MAJOR, UHID_MINOR)


CGROUP_RE = re.compile(r"(?:lxc|incus)\.payload\.([A-Za-z0-9_.-]+)")


def container_of(pid):
    """Container name from the process's cgroup, or None if it runs on the host.

    Incus still uses LXC underneath, so the prefix is `lxc.payload.` here;
    other versions write `incus.payload.`. Both are accepted.
    """
    try:
        with open(f"/proc/{pid}/cgroup") as fh:
            match = CGROUP_RE.search(fh.read())
    except OSError:
        return None
    return match.group(1) if match else None


def owners():
    """Map sysname ('input200') to the container that created it.

    Devices created by a process on the host map to None, which the broker
    treats as a refusal rather than as a free-for-all.
    """
    found = {}
    for pid, fd in uinput_descriptors():
        try:
            pidfd = _syscall(SYS_PIDFD_OPEN, pid, 0)
        except OSError:
            continue
        try:
            try:
                dup = _syscall(SYS_PIDFD_GETFD, pidfd, fd, 0)
            except OSError:
                continue
            try:
                sysname = sysname_of_fd(dup)
            except OSError:
                continue                  # descriptor exists, no device on it yet
            finally:
                os.close(dup)
        finally:
            os.close(pidfd)
        found[sysname] = container_of(pid)
    return found


def uhid_holders():
    """Which container holds each open uhid descriptor: {(pid, fd): container}.

    uhid offers no counterpart to UI_GET_SYSNAME, in fact no ioctls at all, so a
    descriptor cannot be asked what it created. What can be used instead is that
    uhid ties one device to one descriptor exactly like uinput does: UHID_CREATE2
    acts on the descriptor and closing it destroys the device. So a gamepad that
    appears belongs to whichever container just opened a new descriptor.

    That is correlation by ordering rather than a structural fact, and it is
    written down as such. A proxy that sees the creation itself would be exact;
    this is the part that stays a heuristic until then.
    """
    return {(pid, fd): container_of(pid) for pid, fd in uhid_descriptors()}


def sysname_of_node(node):
    """'event26' -> 'input200', by walking sysfs."""
    real = os.path.realpath(f"/sys/class/input/{node}")
    return os.path.basename(os.path.dirname(real))


# The record the uhid observer keeps: HID device id to container name.
UHID_OWNERS = "/run/polyseat/uhid-owners.json"

# A HID device directory is called bus:vendor:product.instance, all hexadecimal,
# and that string is the key the observer files it under.
HID_ID_RE = re.compile(r"/([0-9A-Fa-f]{4}:[0-9A-Fa-f]{4}:[0-9A-Fa-f]{4}\.[0-9A-Fa-f]{4})/")


def uhid_owner(devpath):
    """Which container created this device, according to the uhid observer.

    This is the half of the structural answer that can be given inside udev.
    Asking a foreign process what it made needs pidfd_open and pidfd_getfd, and
    systemd-udevd runs its workers behind a syscall filter that blocks both, so
    the uinput question cannot be answered here at all. Reading a file can be.

    The observer watches the kernel create uhid devices and writes down which
    process did it, keyed by the HID device id, which is also a component of
    every path underneath it. So an input node and a raw HID node belonging to
    the same gamepad both resolve to the same answer.
    """
    match = HID_ID_RE.search(devpath if devpath.endswith("/") else devpath + "/")
    if not match:
        return None

    try:
        with open(UHID_OWNERS) as fh:
            owners = json.load(fh)
    except (OSError, ValueError):
        return None

    if not isinstance(owners, dict):
        return None

    return owners.get(match.group(1))


def udev(devpath):
    """Answer, for one device, whether a container created it.

    This exists so that the udev rule which keeps a seat's input devices away
    from the host desktop can ask the same structural question the broker asks,
    instead of matching on the device name.

    The name based version of that rule was a list of patterns covering what
    Sunshine creates. It held for exactly as long as Sunshine was the only thing
    in a seat creating input devices, and it never held for a gamepad's raw HID
    node at all: hidraw devices have no name attribute to match on, so no
    pattern could reach them. A seat's controller was readable by the host's
    Steam for as long as it took the broker to notice, which is half a second
    and quite long enough for a program that watches for new devices.

    Three answers, and the difference between the last two matters:

      container   made by a process inside a container
      host        made by a process on the host, which is legitimate and must
                  not be touched: this machine runs Steam too, and Steam makes
                  virtual gamepads the desktop is supposed to see
      unknown     nothing here can say. Real hardware, or a uinput device,
                  which cannot be traced from inside udev.

    Only "container" hides anything, so a failure leaves the old behaviour
    rather than either exposing a seat's devices or hiding the host's.
    """
    # uhid first, because it is the one that works here and it covers both
    # halves of a gamepad.
    owner = uhid_owner(devpath)

    if owner:
        print("POLYSEAT_OWNER=container")
        return

    node = os.path.basename(devpath.rstrip("/"))

    try:
        sysname = sysname_of_node(node)
    except OSError:
        print("POLYSEAT_OWNER=unknown")
        return

    mapping = owners()

    if sysname not in mapping:
        print("POLYSEAT_OWNER=unknown")
    elif mapping[sysname] is None:
        print("POLYSEAT_OWNER=host")
    else:
        print("POLYSEAT_OWNER=container")


def main():
    if os.geteuid() != 0:
        sys.exit("needs root: reading foreign descriptors is privileged")

    # udev calls this per device and reads one KEY=value line off stdout.
    if len(sys.argv) == 3 and sys.argv[1] == "--udev":
        udev(sys.argv[2])
        return

    mapping = owners()
    print(f"{len(mapping)} uinput device(s) with a traceable creator\n")

    for node in sorted(os.listdir("/sys/class/input")):
        if not node.startswith("event"):
            continue
        real = os.path.realpath(f"/sys/class/input/{node}")
        if "/devices/virtual/" not in real:
            continue
        try:
            with open(f"/sys/class/input/{node}/device/name") as fh:
                name = fh.read().strip()
        except OSError:
            continue
        sysname = sysname_of_node(node)
        if sysname in mapping:
            owner = mapping[sysname] or "HOST (refused)"
            source = "uinput"
        else:
            owner = "unknown"
            source = "uhid or gone"
        print(f"  {node:<10} {sysname:<10} {source:<13} {owner:<16} {name}")


if __name__ == "__main__":
    main()
