#!/usr/bin/env python3
"""padgen.py — erzeugt ein virtuelles Gamepad über rohes uinput.

Bewusst ohne python-evdev: die Bibliothek öffnet nach dem Anlegen den
Geräteknoten unter /dev/input/, um `.device` zu füllen. Genau dieser Knoten
fehlt im Container — das ist der Zustand, den M0 untersucht. Rohes uinput
macht diese Annahme nicht.

Das Pad meldet sich als Xbox-360-Controller (045e:028e) mit dem üblichen
Achsen- und Tastenlayout, damit SDL sein eingebautes Mapping anwendet. Der
Gerätename ist frei setzbar — das ist H4: der Seat-Tag, an dem der Broker
später erkennt, zu welchem Seat ein Pad gehört.

    ./padgen.py --seat m0
"""

import argparse
import fcntl
import os
import signal
import struct
import sys
import time

# ---------------------------------------------------------------- ioctl-Codes

_IOC_WRITE = 1
_IOC_READ = 2
UINPUT_IOCTL_BASE = ord("U")


def _ioc(direction, nr, size):
    op = (direction << 30) | (size << 16) | (UINPUT_IOCTL_BASE << 8) | nr
    # fcntl.ioctl erwartet einen vorzeichenbehafteten 32-Bit-Wert.
    return op - (1 << 32) if op >= (1 << 31) else op


UI_DEV_CREATE = _ioc(0, 1, 0)
UI_DEV_DESTROY = _ioc(0, 2, 0)
UI_DEV_SETUP = _ioc(_IOC_WRITE, 3, 92)   # sizeof(struct uinput_setup)
UI_ABS_SETUP = _ioc(_IOC_WRITE, 4, 28)   # sizeof(struct uinput_abs_setup)
UI_SET_EVBIT = _ioc(_IOC_WRITE, 100, 4)
UI_SET_KEYBIT = _ioc(_IOC_WRITE, 101, 4)
UI_SET_ABSBIT = _ioc(_IOC_WRITE, 103, 4)
UI_GET_SYSNAME = _ioc(_IOC_READ, 44, 32)

# ------------------------------------------------------------- Event-Konstanten

EV_SYN, EV_KEY, EV_ABS = 0x00, 0x01, 0x03
SYN_REPORT = 0x00
BUS_USB = 0x03

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
    0x00: (-32768, 32767, 16, 128),  # ABS_X   linker Stick
    0x01: (-32768, 32767, 16, 128),  # ABS_Y
    0x03: (-32768, 32767, 16, 128),  # ABS_RX  rechter Stick
    0x04: (-32768, 32767, 16, 128),  # ABS_RY
    0x02: (0, 255, 0, 0),            # ABS_Z   linker Trigger
    0x05: (0, 255, 0, 0),            # ABS_RZ  rechter Trigger
    0x10: (-1, 1, 0, 0),             # ABS_HAT0X  D-Pad
    0x11: (-1, 1, 0, 0),             # ABS_HAT0Y
}


class Pad:
    def __init__(self, name, vendor, product):
        self.fd = os.open("/dev/uinput", os.O_WRONLY | os.O_NONBLOCK)

        # UI_SET_*BIT nehmen den Code als Wert, nicht als Zeiger auf einen Puffer.
        fcntl.ioctl(self.fd, UI_SET_EVBIT, EV_KEY)
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

        # struct uinput_abs_setup { __u16 code; <2 Byte Padding>; input_absinfo }
        # struct input_absinfo { value, minimum, maximum, fuzz, flat, resolution }
        for code, (lo, hi, fuzz, flat) in AXES.items():
            value = 0
            fcntl.ioctl(
                self.fd, UI_ABS_SETUP,
                struct.pack("<H2x6i", code, value, lo, hi, fuzz, flat, 0),
            )

        fcntl.ioctl(self.fd, UI_DEV_CREATE)

    def sysname(self):
        """Liefert z.B. 'input42' — die Kennung, über die der Broker später
        Host-Knoten und erzeugendes Pad korreliert."""
        buf = bytearray(32)
        try:
            fcntl.ioctl(self.fd, UI_GET_SYSNAME, buf, True)
            return buf.split(b"\x00")[0].decode()
        except OSError as exc:
            return f"<UI_GET_SYSNAME fehlgeschlagen: {exc}>"

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
    ap.add_argument("--seat", default="m0", help="Seat-Tag im Gerätenamen")
    ap.add_argument("--name", help="voller Gerätename (überschreibt --seat)")
    ap.add_argument("--vendor", type=lambda s: int(s, 0), default=0x045E)
    ap.add_argument("--product", type=lambda s: int(s, 0), default=0x028E)
    ap.add_argument("--quiet", action="store_true", help="keine Testereignisse senden")
    args = ap.parse_args()

    name = args.name or f"polyseat:{args.seat} Virtual Gamepad"

    try:
        pad = Pad(name, args.vendor, args.product)
    except PermissionError:
        sys.exit("Kein Zugriff auf /dev/uinput — als root ausführen bzw. Device durchreichen.")
    except FileNotFoundError:
        sys.exit("/dev/uinput fehlt — im Container als unix-char durchreichen.")

    print(f"Pad angelegt: {name!r}")
    print(f"  vendor:product = {args.vendor:04x}:{args.product:04x}")
    print(f"  sysname        = {pad.sysname()}")
    print("Läuft. Strg-C beendet und räumt auf.", flush=True)

    running = True

    def stop(_sig, _frm):
        nonlocal running
        running = False

    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGTERM, stop)

    tick = 0
    while running:
        if not args.quiet:
            # Sichtbare Ereignisse, damit evtest und SDL nicht nur ein stummes
            # Gerät sehen: A-Taste antippen, linken Stick wackeln.
            pad.emit(EV_KEY, BTN_SOUTH, 1)
            pad.emit(EV_ABS, ABS_X, 12000)
            pad.sync()
            time.sleep(0.15)
            pad.emit(EV_KEY, BTN_SOUTH, 0)
            pad.emit(EV_ABS, ABS_X, 0)
            pad.sync()
            tick += 1
            print(f"  tick {tick}", flush=True)
        time.sleep(2)

    pad.close()
    print("Pad entfernt.")


if __name__ == "__main__":
    main()
