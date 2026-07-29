#!/usr/bin/env python3
"""polyseat-pad-pointer - drive a seat's desktop with a gamepad.

A seat streams to whatever Moonlight runs on, and that is often something with
no keyboard and no mouse at all: an Apple TV, a phone, a television. The app
list and Steam Big Picture are navigable with a controller, so starting a game
is covered. Everything else in the seat was not. Signing in to a store in a
launcher needs a pointer and letters, and the client cannot supply either:
Moonlight on tvOS sends modifiers rather than text, and has an open request for
exactly this.

So the pointer and the keyboard both live in the seat and travel in the video
stream. This turns the gamepad into the pointer; squeekboard draws the letters.

**Pointer mode is off until asked for, and that is the whole safety story.**
A helper that turned a thumbstick into a mouse the moment a controller appeared
would make every game unplayable. Select and Start together toggle it, a chord
because single buttons are taken: Guide opens Steam's overlay, and everything
else is a game input. While the mode is off, this reads events and emits
nothing.

The gamepad is never grabbed. Games keep seeing it exactly as before, which is
also why the mode has to be a deliberate act rather than a guess about what the
player is doing.

The virtual devices are called "polyseat:pointer" and "polyseat:keys" so that
the udev rule on the host recognises them at creation. That is an optimisation
rather than the protection: they are created from inside the container, so the
broker attributes them structurally and takes them off the host regardless of
what they are called.
"""

import errno
import os
import subprocess
import sys
import time

import evdev
from evdev import ecodes

# How far the stick has to move before it counts. Sticks rest slightly off
# centre and a pointer that drifts on its own is worse than one that needs a
# firm push.
DEADZONE = 0.18

# Pixels per second at full deflection, and the curve applied before it. The
# square makes small movements fine and large ones fast, which is what makes a
# stick usable for pointing at all.
SPEED = 1100.0
CURVE = 2.0

# Scroll steps per second at full deflection.
SCROLL_SPEED = 12.0

INTERVAL = 1.0 / 90.0

KEYBOARD = "/usr/local/bin/polyseat-keyboard"

# Buttons that click, and buttons that press a key. Deliberately small: this is
# for getting through a login form and a launcher, not for replacing a keyboard.
CLICKS = {
    ecodes.BTN_SOUTH: ecodes.BTN_LEFT,
    ecodes.BTN_EAST: ecodes.BTN_RIGHT,
    ecodes.BTN_THUMBR: ecodes.BTN_MIDDLE,
}

KEYS = {
    ecodes.BTN_WEST: ecodes.KEY_ENTER,
    ecodes.BTN_NORTH: ecodes.KEY_ESC,
    ecodes.BTN_TL: ecodes.KEY_BACKSPACE,
    ecodes.BTN_TR: ecodes.KEY_TAB,
    ecodes.BTN_DPAD_UP: ecodes.KEY_UP,
    ecodes.BTN_DPAD_DOWN: ecodes.KEY_DOWN,
    ecodes.BTN_DPAD_LEFT: ecodes.KEY_LEFT,
    ecodes.BTN_DPAD_RIGHT: ecodes.KEY_RIGHT,
}

# The chord that turns the mode on and off.
TOGGLE = (ecodes.BTN_SELECT, ecodes.BTN_START)


def log(message):
    print(f"polyseat-pad-pointer: {message}", flush=True)


def is_pad(device):
    """A device with a south button and two axes is a gamepad.

    By capability rather than by name. Sunshine names its virtual pads after
    whatever model it is emulating, and that string is chosen by the client.
    """
    caps = device.capabilities()
    keys = caps.get(ecodes.EV_KEY, [])
    axes = [code for code, _ in caps.get(ecodes.EV_ABS, [])]

    return ecodes.BTN_SOUTH in keys and ecodes.ABS_X in axes


def pads():
    found = []

    for path in evdev.list_devices():
        try:
            device = evdev.InputDevice(path)
        except OSError:
            continue

        if is_pad(device):
            found.append(device)
        else:
            device.close()

    return found


def axis_info(device):
    """Range of every absolute axis, so raw values can be scaled to -1..1."""
    info = {}

    for code, absinfo in device.capabilities().get(ecodes.EV_ABS, []):
        info[code] = absinfo

    return info


def scale(value, absinfo):
    """Raw axis value to -1..1, with the deadzone removed rather than clipped.

    Removed, so that the first movement past the deadzone is a small movement.
    Clipping it instead makes the pointer jump the moment the stick is touched.
    """
    span = (absinfo.max - absinfo.min) / 2.0
    if span <= 0:
        return 0.0

    centre = absinfo.min + span
    position = (value - centre) / span

    if abs(position) < DEADZONE:
        return 0.0

    position = (abs(position) - DEADZONE) / (1.0 - DEADZONE)

    return position if value >= centre else -position


class Pointer:
    """The virtual mouse and keyboard this writes into."""

    def __init__(self, seat):
        tag = f" ({seat})" if seat else ""

        self.mouse = evdev.UInput(
            {
                ecodes.EV_REL: [ecodes.REL_X, ecodes.REL_Y,
                                ecodes.REL_WHEEL, ecodes.REL_HWHEEL],
                ecodes.EV_KEY: [ecodes.BTN_LEFT, ecodes.BTN_RIGHT,
                                ecodes.BTN_MIDDLE],
            },
            name=f"polyseat:pointer{tag}",
        )

        self.keys = evdev.UInput(
            {ecodes.EV_KEY: sorted(set(KEYS.values()))},
            name=f"polyseat:keys{tag}",
        )

        # Fractions of a pixel accumulate between frames. Without this a slow
        # stick produces no movement at all, because every frame rounds to zero.
        self.debt_x = 0.0
        self.debt_y = 0.0

    def move(self, dx, dy):
        self.debt_x += dx
        self.debt_y += dy

        # int() truncates towards zero, so what is left keeps the sign it had
        # and a slow stick in either direction eventually adds up to a pixel.
        step_x = int(self.debt_x)
        step_y = int(self.debt_y)
        self.debt_x -= step_x
        self.debt_y -= step_y

        if step_x:
            self.mouse.write(ecodes.EV_REL, ecodes.REL_X, step_x)
        if step_y:
            self.mouse.write(ecodes.EV_REL, ecodes.REL_Y, step_y)
        if step_x or step_y:
            self.mouse.syn()

    def scroll(self, steps):
        if steps:
            self.mouse.write(ecodes.EV_REL, ecodes.REL_WHEEL, steps)
            self.mouse.syn()

    def click(self, button, pressed):
        self.mouse.write(ecodes.EV_KEY, button, 1 if pressed else 0)
        self.mouse.syn()

    def key(self, code, pressed):
        self.keys.write(ecodes.EV_KEY, code, 1 if pressed else 0)
        self.keys.syn()

    def release_all(self):
        """Leave nothing held when the mode goes off.

        A button that was down at the moment the mode was switched off would
        otherwise stay down forever, and a stuck mouse button makes a desktop
        look broken in a way that is very hard to connect back to here.
        """
        for button in CLICKS.values():
            self.mouse.write(ecodes.EV_KEY, button, 0)

        self.mouse.syn()

        for code in set(KEYS.values()):
            self.keys.write(ecodes.EV_KEY, code, 0)

        self.keys.syn()


def toggle_keyboard():
    try:
        subprocess.Popen([KEYBOARD], stdout=subprocess.DEVNULL,
                         stderr=subprocess.DEVNULL)
    except OSError as exc:
        log(f"could not reach the on-screen keyboard: {exc}")


def main():
    seat = os.environ.get("XDG_SEAT", "")

    devices = pads()

    while not devices:
        # Normal rather than exceptional: the seat's session starts before
        # anybody connects, and a gamepad only exists once Moonlight is
        # streaming and the broker has attached it.
        time.sleep(2.0)
        devices = pads()

    log(f"following {len(devices)} gamepad(s): "
        f"{', '.join(d.name for d in devices)}")

    pointer = Pointer(seat)
    log("ready. Select and Start together turn pointer mode on and off")

    ranges = {d.fd: axis_info(d) for d in devices}
    by_fd = {d.fd: d for d in devices}

    active = False
    held = set()
    axes = {ecodes.ABS_RX: 0.0, ecodes.ABS_RY: 0.0, ecodes.ABS_Y: 0.0}
    scroll_debt = 0.0
    last = time.monotonic()

    from select import select

    while True:
        readable, _, _ = select(list(by_fd), [], [], INTERVAL)

        for fd in readable:
            device = by_fd.get(fd)
            if device is None:
                continue

            try:
                events = list(device.read())
            except OSError as exc:
                if exc.errno in (errno.ENODEV, errno.EBADF):
                    log(f"{device.name} went away")
                    by_fd.pop(fd, None)
                    ranges.pop(fd, None)
                    continue
                raise

            for event in events:
                if event.type == ecodes.EV_KEY:
                    if event.value:
                        held.add(event.code)
                    else:
                        held.discard(event.code)

                    if all(button in held for button in TOGGLE):
                        if event.value:
                            active = not active
                            log(f"pointer mode {'on' if active else 'off'}")
                            if not active:
                                pointer.release_all()
                        continue

                    if not active:
                        continue

                    if event.code in CLICKS:
                        pointer.click(CLICKS[event.code], bool(event.value))
                    elif event.code in KEYS:
                        pointer.key(KEYS[event.code], bool(event.value))
                    elif event.code == ecodes.BTN_THUMBL and event.value:
                        toggle_keyboard()

                elif event.type == ecodes.EV_ABS and event.code in axes:
                    absinfo = ranges.get(fd, {}).get(event.code)
                    if absinfo is not None:
                        axes[event.code] = scale(event.value, absinfo)

        now = time.monotonic()
        elapsed, last = now - last, now

        if not active:
            continue

        x = axes[ecodes.ABS_RX]
        y = axes[ecodes.ABS_RY]

        if x or y:
            pointer.move(
                (abs(x) ** CURVE) * (1 if x > 0 else -1) * SPEED * elapsed,
                (abs(y) ** CURVE) * (1 if y > 0 else -1) * SPEED * elapsed,
            )

        wheel = axes[ecodes.ABS_Y]
        if wheel:
            scroll_debt -= (abs(wheel) ** CURVE) * (1 if wheel > 0 else -1) \
                * SCROLL_SPEED * elapsed
            steps = int(scroll_debt)
            if steps:
                scroll_debt -= steps
                pointer.scroll(steps)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit(0)
