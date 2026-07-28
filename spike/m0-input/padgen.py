#!/usr/bin/env python3
"""padgen.py - creates a virtual gamepad through raw uinput.

Deliberately without python-evdev: after creating the device, that library
opens the device node under /dev/input/ to populate `.device`. That node is
precisely what is missing inside a container - which is the very state M0
investigates. Raw uinput does not make that assumption.

The pad identifies itself as an Xbox 360 controller (045e:028e) with the usual
axis and button layout so that SDL applies its built-in mapping. The device
name is freely settable - that is H4: the seat tag the broker later uses to
tell which seat a pad belongs to.

    ./padgen.py --seat m0
"""

import argparse
import fcntl
import os
import signal
import struct
import sys
import time

# ---------------------------------------------------------------- ioctl codes

_IOC_WRITE = 1
_IOC_READ = 2
UINPUT_IOCTL_BASE = ord("U")


def _ioc(direction, nr, size):
    op = (direction << 30) | (size << 16) | (UINPUT_IOCTL_BASE << 8) | nr
    # fcntl.ioctl expects a signed 32-bit value.
    return op - (1 << 32) if op >= (1 << 31) else op


UI_DEV_CREATE = _ioc(0, 1, 0)
UI_DEV_DESTROY = _ioc(0, 2, 0)
UI_DEV_SETUP = _ioc(_IOC_WRITE, 3, 92)   # sizeof(struct uinput_setup)
UI_ABS_SETUP = _ioc(_IOC_WRITE, 4, 28)   # sizeof(struct uinput_abs_setup)
UI_SET_EVBIT = _ioc(_IOC_WRITE, 100, 4)
UI_SET_KEYBIT = _ioc(_IOC_WRITE, 101, 4)
UI_SET_RELBIT = _ioc(_IOC_WRITE, 102, 4)
UI_SET_ABSBIT = _ioc(_IOC_WRITE, 103, 4)
UI_GET_SYSNAME = _ioc(_IOC_READ, 44, 32)

# ------------------------------------------------------------ event constants

EV_SYN, EV_KEY, EV_REL, EV_ABS = 0x00, 0x01, 0x02, 0x03
SYN_REPORT = 0x00
BUS_USB = 0x03

# Keyboard and mouse variants. libinput ignores gamepads entirely - a game
# reads those directly through evdev. For the compositor only keyboard and
# mouse matter, and those are exactly what Sunshine creates as "Keyboard
# passthrough" and "Mouse passthrough". Anyone testing the input chain against
# sway therefore needs these two types, not the pad.
KEYBOARD_KEYS = list(range(1, 128))          # KEY_ESC .. KEY_COMPOSE
MOUSE_BUTTONS = [0x110, 0x111, 0x112]        # BTN_LEFT, BTN_RIGHT, BTN_MIDDLE
REL_AXES = [0x00, 0x01, 0x08]                # REL_X, REL_Y, REL_WHEEL

BUTTONS = [
    0x130,  # BTN_SOUTH  (A)
    0x131,  # BTN_EAST   (B)
    0x133,  # BTN_NORTH  (Y)
    0x134,  # BTN_WEST   (X)
    0x136,  # BTN_TL
    0x137,  # BTN_TR
    0x13A,  # BTN_SELECT
    0x13B,  # BTN_START
    0x13C,  # BTN_MODE
    0x13D,  # BTN_THUMBL
    0x13E,  # BTN_THUMBR
]

BTN_SOUTH = 0x130
ABS_X = 0x00

# code -> (min, max, fuzz, flat)
AXES = {
    0x00: (-32768, 32767, 16, 128),  # ABS_X   left stick
    0x01: (-32768, 32767, 16, 128),  # ABS_Y
    0x03: (-32768, 32767, 16, 128),  # ABS_RX  right stick
    0x04: (-32768, 32767, 16, 128),  # ABS_RY
    0x02: (0, 255, 0, 0),            # ABS_Z   left trigger
    0x05: (0, 255, 0, 0),            # ABS_RZ  right trigger
    0x10: (-1, 1, 0, 0),             # ABS_HAT0X  D-pad
    0x11: (-1, 1, 0, 0),             # ABS_HAT0Y
}


class Pad:
    def __init__(self, name, vendor, product, kind="gamepad"):
        self.fd = os.open("/dev/uinput", os.O_WRONLY | os.O_NONBLOCK)
        self.kind = kind

        # UI_SET_*BIT take the code by value, not as a pointer to a buffer.
        fcntl.ioctl(self.fd, UI_SET_EVBIT, EV_KEY)
        if kind == "keyboard":
            for code in KEYBOARD_KEYS:
                fcntl.ioctl(self.fd, UI_SET_KEYBIT, code)
        elif kind == "mouse":
            for code in MOUSE_BUTTONS:
                fcntl.ioctl(self.fd, UI_SET_KEYBIT, code)
            fcntl.ioctl(self.fd, UI_SET_EVBIT, EV_REL)
            for code in REL_AXES:
                fcntl.ioctl(self.fd, UI_SET_RELBIT, code)
        else:
            for code in BUTTONS:
                fcntl.ioctl(self.fd, UI_SET_KEYBIT, code)
            fcntl.ioctl(self.fd, UI_SET_EVBIT, EV_ABS)
            for code in AXES:
                fcntl.ioctl(self.fd, UI_SET_ABSBIT, code)

        # struct uinput_setup { struct input_id id; char name[80]; __u32 ff; }
        setup = struct.pack(
            "<HHHH80sI", BUS_USB, vendor, product, 0x0110,
            name.encode()[:79], 0,
        )
        fcntl.ioctl(self.fd, UI_DEV_SETUP, setup)

        # struct uinput_abs_setup { __u16 code; <2 bytes padding>; input_absinfo }
        # struct input_absinfo { value, minimum, maximum, fuzz, flat, resolution }
        if kind == "gamepad":
            for code, (lo, hi, fuzz, flat) in AXES.items():
                value = 0
                fcntl.ioctl(
                    self.fd, UI_ABS_SETUP,
                    struct.pack("<H2x6i", code, value, lo, hi, fuzz, flat, 0),
                )

        fcntl.ioctl(self.fd, UI_DEV_CREATE)

    def sysname(self):
        """Returns e.g. 'input42' - the identifier the broker later uses to
        correlate the host node with the pad that created it."""
        buf = bytearray(32)
        try:
            fcntl.ioctl(self.fd, UI_GET_SYSNAME, buf, True)
            return buf.split(b"\x00")[0].decode()
        except OSError as exc:
            return f"<UI_GET_SYSNAME failed: {exc}>"

    def emit(self, etype, code, value):
        # struct input_event { struct timeval; __u16 type; __u16 code; __s32 value }
        now = time.time()
        os.write(self.fd, struct.pack(
            "<qqHHi", int(now), int((now % 1) * 1_000_000), etype, code, value,
        ))

    def sync(self):
        self.emit(EV_SYN, SYN_REPORT, 0)

    def close(self):
        try:
            fcntl.ioctl(self.fd, UI_DEV_DESTROY)
        finally:
            os.close(self.fd)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--seat", default="m0", help="seat tag inside the device name")
    ap.add_argument("--name", help="full device name (overrides --seat)")
    ap.add_argument("--vendor", type=lambda s: int(s, 0), default=0x045E)
    ap.add_argument("--product", type=lambda s: int(s, 0), default=0x028E)
    ap.add_argument("--quiet", action="store_true", help="send no test events")
    ap.add_argument("--type", dest="kind", default="gamepad",
                    choices=["gamepad", "keyboard", "mouse"],
                    help="device type - libinput ignores gamepads, so only "
                         "keyboard and mouse are relevant for sway")
    args = ap.parse_args()

    suffix = {"gamepad": "Virtual Gamepad",
              "keyboard": "Virtual Keyboard",
              "mouse": "Virtual Mouse"}[args.kind]
    name = args.name or f"polyseat:{args.seat} {suffix}"

    try:
        pad = Pad(name, args.vendor, args.product, args.kind)
    except PermissionError:
        sys.exit("No access to /dev/uinput - run as root or pass the device through.")
    except FileNotFoundError:
        sys.exit("/dev/uinput is missing - pass it into the container as unix-char.")

    print(f"Pad created: {name!r}")
    print(f"  vendor:product = {args.vendor:04x}:{args.product:04x}")
    print(f"  sysname        = {pad.sysname()}")
    print("Running. Ctrl-C stops and cleans up.", flush=True)

    running = True

    def stop(_sig, _frm):
        nonlocal running
        running = False

    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGTERM, stop)

    tick = 0
    while running:
        if not args.quiet:
            # Visible events, so that evtest and SDL do not just see a silent
            # device.
            if args.kind == "gamepad":
                pad.emit(EV_KEY, BTN_SOUTH, 1)
                pad.emit(EV_ABS, ABS_X, 12000)
                pad.sync()
                time.sleep(0.15)
                pad.emit(EV_KEY, BTN_SOUTH, 0)
                pad.emit(EV_ABS, ABS_X, 0)
            elif args.kind == "mouse":
                pad.emit(EV_REL, 0x00, 5)   # REL_X
                pad.sync()
                time.sleep(0.15)
                pad.emit(EV_REL, 0x00, -5)
            else:
                pad.emit(EV_KEY, 0x39, 1)   # KEY_SPACE
                pad.sync()
                time.sleep(0.15)
                pad.emit(EV_KEY, 0x39, 0)
            pad.sync()
            tick += 1
            print(f"  tick {tick}", flush=True)
        time.sleep(2)

    pad.close()
    print("Pad removed.")


if __name__ == "__main__":
    main()
