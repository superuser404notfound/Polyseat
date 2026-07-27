#!/usr/bin/env python3
"""fakeudev.py — sendet ein synthetisches udev-Ereignis in den Container.

Hintergrund: Ein per `incus config device add` eingehängtes Gerät erzeugt im
Container kein uevent (in `10-uevent-test.sh` gemessen). Die *Enumeration*
funktioniert trotzdem, weil libudev dafür `/sys` abläuft — ein Gerät, das
beim Start schon da ist, sieht sway also. Was fehlt, ist die Benachrichtigung
über Geräte, die *später* dazukommen. Und genau das ist der Normalfall:
Sunshine legt seine virtuellen Geräte an, wenn sich ein Client verbindet,
also lange nachdem sway läuft.

Der Kernel-Uevent-Netlink ist an den Netzwerk-Namespace gebunden, deshalb
erreichen Host-Ereignisse den Container nie. Man kann aber im Namespace des
Containers selbst eine Nachricht auf die udev-Multicast-Gruppe legen —
libudev-Clients (libinput, SDL) hören genau darauf. udevd wird dabei
umgangen; die Clients bekommen das Ereignis direkt.

Muss IM Container als root laufen: libudev verwirft Nachrichten, deren
Absender nicht uid 0 ist, und die Absenderkennung wird beim Übersetzen in
den User-Namespace des Containers geprüft. Ein Host-Prozess, der nur per
setns in den Netzwerk-Namespace wechselt, fiele durch diese Prüfung.

    ./fakeudev.py add /sys/devices/virtual/input/input42/event29 \
        --subsystem input --devname input/event29 --major 13 --minor 93 \
        --prop ID_INPUT=1 --prop ID_INPUT_KEY=1 --prop ID_INPUT_KEYBOARD=1
"""

import argparse
import socket
import struct
import sys

NETLINK_KOBJECT_UEVENT = 15
UDEV_MONITOR_UDEV = 2          # Multicast-Gruppe, auf der libudev-Clients hören
UDEV_MONITOR_MAGIC = 0xFEEDCAFE


def murmur_hash2(key: bytes, seed: int = 0) -> int:
    """systemds string_hash32 — MurmurHash2 mit Seed 0.

    libudev trägt den Hash des Subsystems im Nachrichtenkopf, damit Clients
    uninteressante Ereignisse verwerfen können, ohne sie zu zerlegen. Stimmt
    er nicht, filtert der Client die Nachricht weg.
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

    # struct udev_monitor_netlink_header: prefix, dann acht uint32.
    # magic und die Hashes liegen in Netzwerk-Byteordnung, die Längenfelder
    # in der des Rechners.
    header_size = 40
    header = (
        b"libudev\0"
        + struct.pack(">I", UDEV_MONITOR_MAGIC)
        + struct.pack("=III", header_size, header_size, len(payload))
        + struct.pack(">I", murmur_hash2(props["SUBSYSTEM"].encode()))
        + struct.pack(">I", 0)   # devtype-Hash: kein Filter
        + struct.pack(">II", 0, 0)   # Tag-Bloomfilter: keiner
    )
    return header + payload


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("action", choices=["add", "remove", "change"])
    ap.add_argument("devpath", help="Pfad unterhalb von /sys, mit führendem /devices/…")
    ap.add_argument("--subsystem", default="input")
    ap.add_argument("--devname", help="z.B. input/event29")
    ap.add_argument("--major", type=int)
    ap.add_argument("--minor", type=int)
    ap.add_argument("--seqnum", type=int, default=9000)
    ap.add_argument("--prop", action="append", default=[],
                    metavar="KEY=VALUE", help="mehrfach verwendbar")
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
        sys.exit("Kein Zugriff auf die Multicast-Gruppe — als root im Container ausführen.")
    finally:
        sock.close()

    print(f"{args.action}: {args.devpath} ({len(msg)} Bytes)")


if __name__ == "__main__":
    main()
