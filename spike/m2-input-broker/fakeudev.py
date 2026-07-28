#!/usr/bin/env python3
"""fakeudev.py - sends a synthetic udev event into the container.

Background: a device attached through `incus config device add` produces no
uevent inside the container (measured in `10-uevent-test.sh`). *Enumeration*
still works, because libudev walks /sys for that, so a device that is already
present at startup is visible to sway. What is missing is the notification about
devices that arrive *later*. And that is the normal case: Sunshine creates its
virtual devices when a client connects, long after sway is up.

The kernel uevent netlink is bound to the network namespace, so host events
never reach the container. But one can put a message on the udev multicast group
inside the container's own namespace, and libudev clients (libinput, SDL) listen
for exactly that. udevd is bypassed; the clients get the event directly.

Must run INSIDE the container as root: libudev discards messages whose sender is
not uid 0, and the sender credentials are translated into the container's user
namespace. A host process that only enters the network namespace via setns would
fail that check.

    ./fakeudev.py add /sys/devices/virtual/input/input42/event29 \
        --subsystem input --devname input/event29 --major 13 --minor 93 \
        --prop ID_INPUT=1 --prop ID_INPUT_KEY=1 --prop ID_INPUT_KEYBOARD=1
"""

import argparse
import socket
import struct
import sys

NETLINK_KOBJECT_UEVENT = 15
UDEV_MONITOR_UDEV = 2          # multicast group libudev clients listen on
UDEV_MONITOR_MAGIC = 0xFEEDCAFE


def murmur_hash2(key: bytes, seed: int = 0) -> int:
    """systemd's string_hash32, i.e. MurmurHash2 with seed 0.

    libudev carries the hash of the subsystem in the message header so that
    clients can discard uninteresting events without parsing them. Get it wrong
    and the client filters the message away.
    """
    m = 0x5BD1E995
    r = 24
    length = len(key)
    h = (seed ^ length) & 0xFFFFFFFF
    i = 0
    while length >= 4:
        k = int.from_bytes(key[i:i + 4], "little")
        k = (k * m) & 0xFFFFFFFF
        k ^= k >> r
        k = (k * m) & 0xFFFFFFFF
        h = (h * m) & 0xFFFFFFFF
        h ^= k
        i += 4
        length -= 4
    if length == 3:
        h ^= key[i + 2] << 16
    if length >= 2:
        h ^= key[i + 1] << 8
    if length >= 1:
        h ^= key[i]
        h = (h * m) & 0xFFFFFFFF
    h ^= h >> 13
    h = (h * m) & 0xFFFFFFFF
    h ^= h >> 15
    return h & 0xFFFFFFFF


def build_message(props: dict) -> bytes:
    payload = b"".join(f"{k}={v}".encode() + b"\0" for k, v in props.items())

    # struct udev_monitor_netlink_header: prefix followed by eight uint32.
    # magic and the hashes are in network byte order, the length fields in
    # host byte order.
    header_size = 40
    header = (
        b"libudev\0"
        + struct.pack(">I", UDEV_MONITOR_MAGIC)
        + struct.pack("=III", header_size, header_size, len(payload))
        + struct.pack(">I", murmur_hash2(props["SUBSYSTEM"].encode()))
        + struct.pack(">I", 0)   # devtype hash: no filter
        + struct.pack(">II", 0, 0)   # tag bloom filter: none
    )
    return header + payload


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("action", choices=["add", "remove", "change"])
    ap.add_argument("devpath", help="path below /sys, with a leading /devices/...")
    ap.add_argument("--subsystem", default="input")
    ap.add_argument("--devname", help="e.g. input/event29")
    ap.add_argument("--major", type=int)
    ap.add_argument("--minor", type=int)
    ap.add_argument("--seqnum", type=int, default=9000)
    ap.add_argument("--prop", action="append", default=[],
                    metavar="KEY=VALUE", help="may be given several times")
    args = ap.parse_args()

    props = {
        "ACTION": args.action,
        "DEVPATH": args.devpath,
        "SUBSYSTEM": args.subsystem,
        "SEQNUM": str(args.seqnum),
    }
    if args.devname:
        props["DEVNAME"] = f"/dev/{args.devname}"
    if args.major is not None:
        props["MAJOR"] = str(args.major)
    if args.minor is not None:
        props["MINOR"] = str(args.minor)
    for p in args.prop:
        k, _, v = p.partition("=")
        props[k] = v

    msg = build_message(props)

    sock = socket.socket(socket.AF_NETLINK, socket.SOCK_RAW, NETLINK_KOBJECT_UEVENT)
    try:
        sock.bind((0, 0))
        sock.sendto(msg, (0, UDEV_MONITOR_UDEV))
    except PermissionError:
        sys.exit("No access to the multicast group. Run as root inside the container.")
    finally:
        sock.close()

    print(f"{args.action}: {args.devpath} ({len(msg)} bytes)")


if __name__ == "__main__":
    main()
