#!/usr/bin/env python3
"""uhidgen.py - creates a virtual HID gamepad through /dev/uhid.

The counterpart to padgen.py for the other creation path. Sunshine uses uhid
for DualSense style pads, and that is the path where attribution is hardest,
so it needs to be reproducible without asking somebody to plug in a controller.

uhid has no ioctls at all. A device is created by writing a `uhid_event`
struct, and the kernel answers by sending events back that have to be read, so
this keeps a reader running for as long as the device lives.

    ./uhidgen.py --name "Sunshine PS5 (virtual) pad (seat1)"
"""

import argparse
import os
import signal
import struct
import sys
import threading
import time

# enum uhid_event_type, in order.
UHID_DESTROY = 1
UHID_CREATE2 = 11

# A minimal but valid gamepad report descriptor: eight buttons, nothing else.
REPORT_DESCRIPTOR = bytes([
    0x05, 0x01,        # Usage Page (Generic Desktop)
    0x09, 0x05,        # Usage (Game Pad)
    0xA1, 0x01,        # Collection (Application)
    0x05, 0x09,        #   Usage Page (Button)
    0x19, 0x01,        #   Usage Minimum (Button 1)
    0x29, 0x08,        #   Usage Maximum (Button 8)
    0x15, 0x00,        #   Logical Minimum (0)
    0x25, 0x01,        #   Logical Maximum (1)
    0x75, 0x01,        #   Report Size (1)
    0x95, 0x08,        #   Report Count (8)
    0x81, 0x02,        #   Input (Data, Variable, Absolute)
    0xC0,              # End Collection
])

RD_MAX = 4096          # HID_MAX_DESCRIPTOR_SIZE


def create2(name, phys, uniq, bus, vendor, product):
    """struct uhid_event { u32 type; struct uhid_create2_req u; } packed."""
    # struct uhid_create2_req:
    #   name[128], phys[64], uniq[64], rd_size, bus, vendor, product,
    #   version, country, rd_data[4096]
    body = struct.pack(
        f"<128s64s64sHHIIII{RD_MAX}s",
        name.encode()[:127],
        phys.encode()[:63],
        uniq.encode()[:63],
        len(REPORT_DESCRIPTOR),
        bus,
        vendor,
        product,
        0,                     # version
        0,                     # country
        REPORT_DESCRIPTOR,
    )
    return struct.pack("<I", UHID_CREATE2) + body


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--name", default="uhidgen Virtual Pad")
    ap.add_argument("--uniq", default="", help="the uniq field, application chosen")
    # Deliberately not Sony's 054C:0CE6. With that identity the kernel's
    # playstation driver claims the device and then fails, because this minimal
    # descriptor does not implement DualSense feature reports: a hidraw node
    # appears but no input device. A neutral identity lets hid-generic bind.
    ap.add_argument("--vendor", type=lambda s: int(s, 0), default=0x1234)
    ap.add_argument("--product", type=lambda s: int(s, 0), default=0x5678)
    args = ap.parse_args()

    try:
        fd = os.open("/dev/uhid", os.O_RDWR)
    except PermissionError:
        sys.exit("no access to /dev/uhid: run as root or pass the device through")
    except FileNotFoundError:
        sys.exit("/dev/uhid is missing: pass it into the container as unix-char")

    os.write(fd, create2(args.name, "", args.uniq, 0x0003, args.vendor, args.product))
    print(f"created: {args.name!r}  {args.vendor:04x}:{args.product:04x}", flush=True)

    running = True

    def drain():
        # The kernel sends UHID_START, UHID_OPEN and friends. Nothing is done
        # with them here, but they have to be read or the queue fills up.
        while running:
            try:
                os.read(fd, 4380)
            except OSError:
                return

    threading.Thread(target=drain, daemon=True).start()

    def stop(_sig, _frm):
        nonlocal running
        running = False

    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGTERM, stop)
    print("running, Ctrl-C removes the device", flush=True)
    while running:
        time.sleep(0.5)

    try:
        os.write(fd, struct.pack("<I", UHID_DESTROY))
    finally:
        os.close(fd)
    print("removed")


if __name__ == "__main__":
    main()
