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

**Pointer mode follows what is in front, and that is the whole safety story.**
A helper that turned a thumbstick into a mouse while a game was running would
make every game unplayable. So the compositor is asked instead of guessed at: a
fullscreen application in front means the controller belongs to it and the mode
goes off; back on the desktop it goes on. That is what the Windows tools do from
the foreground window, and sway can answer it exactly rather than by heuristic.

Two of Select, Start and Guide, held together for a second, still toggle it by
hand. A chord, because single buttons are all taken by the game. Held rather
than tapped, because Select with Start is something a game can ask for outright;
a second is long enough that it cannot happen while somebody is playing, and
short enough to still be a gesture rather than a wait.

Any two of the three rather than two named ones, because which of them arrive is
the client's decision. Moonlight builds the Guide button out of Select and Start,
tvOS having kept the real one for itself, so what reaches the seat is Start with
Guide, or Select with Guide. Measured through an Apple TV: holding Select and
Start on an Xbox controller arrives as BTN_START with BTN_MODE, and BTN_SELECT
never appears at all. CHORD says the rest.

The pad buzzes when it takes, which is the only feedback there is: nothing
appears on screen, and the pointer only shows itself once the stick is moved, so
without it there is no way to tell a hold that was long enough from one that was
not.

A toggle holds until the next time something goes fullscreen or stops being
fullscreen, at which point the automatic answer takes over again. That way a
windowed game can be dealt with, and forgetting to switch back cannot leave
somebody with a dead stick.

While the mode is off, this reads events and emits nothing.

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
import glob
import json
import os
import queue
import socket
import struct
import subprocess
import sys
import threading
import time

import evdev
from evdev import ecodes, ff

# How far the stick has to move before it counts. Sticks rest slightly off
# centre and a pointer that drifts on its own is worse than one that needs a
# firm push.
DEADZONE = 0.18

# How much of the screen the pointer crosses in a second at full deflection, and
# the curve applied before it. The square makes small movements fine and large
# ones fast, which is what makes a stick usable for pointing at all.
#
# A fraction of the screen rather than a number of pixels, because a seat's
# output becomes whatever size the connected client asked for. The first version
# was 1100 pixels per second, which on a phone streaming 1280x720 threw the
# pointer across the screen and on a 4K television crawled. Now it takes the same
# time to cross the screen on all of them.
#
# Two numbers, and they do different things, which is worth saying because the
# feedback that shaped them was "too fast" and then "still too sensitive".
#
# SPEED is the top: how much of the screen a second of full deflection covers.
# CURVE is how much of the range is given to slow movement. Raising it makes
# small pushes finer without changing the top speed at all, and that is the one
# that answers "too sensitive": a 1440p stream on a phone shows targets a few
# millimetres across, and hitting one needs resolution near the centre rather
# than a lower ceiling.
SPEED = 0.45
CURVE = 2.5

# What to assume when the compositor cannot be asked. 1080p is the resolution a
# seat's session comes up at before anybody connects.
FALLBACK_HEIGHT = 1080

# Where the daemon writes the seat's own speed, set from the web interface.
#
# Reread while running rather than read once, because it is adjusted by somebody
# holding the controller and looking at the result. A setting that needs the
# session restarted to take effect is a setting that looks broken.
CONFIG = os.path.expanduser("~/.config/polyseat/pointer.conf")

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

# The chord that turns the mode on and off: any two of Select, Start and Guide,
# held together for a second.
#
# **Any two of the three, because the client decides which of them arrive.**
# Moonlight builds the Guide button out of Select and Start, since tvOS keeps
# the real one for itself, and what reaches the seat is then Start and Guide, or
# Select and Guide, depending on which went down first. Measured in a seat:
# holding Select and Start on an Xbox controller through an Apple TV arrives as
# BTN_START with BTN_MODE and BTN_SELECT never appears at all. Naming two
# particular buttons therefore names something the player cannot press.
#
# These three are the buttons no game plays with, so two of them is still a
# chord nobody trips by accident.
#
# Held rather than tapped for the same reason it is a chord at all: a tap was
# the first version, and Select with Start is something a game can ask for
# outright, so a player could flip the pointer on under their own hands.
CHORD = (ecodes.BTN_SELECT, ecodes.BTN_START, ecodes.BTN_MODE)
CHORD_SIZE = 2
HOLD = 1.0

# The buzz that confirms it. Short and firm, and only for the switch by hand:
# the automatic one follows what is on screen, which is feedback enough, and a
# pad that buzzed every time a game went fullscreen would be a nuisance.
#
# Magnitudes are 0 to 0xffff per motor. The weak one carries most of it because
# that is the motor that gives a buzz rather than a thud.
RUMBLE_MS = 150
RUMBLE_STRONG = 0x4000
RUMBLE_WEAK = 0xA000


def log(message):
    print(f"polyseat-pad-pointer: {message}", flush=True)


def configured_speed():
    """The seat's own speed, or None to keep the built-in one.

    Anything unreadable or out of range is ignored rather than argued with. A
    pointer at the wrong speed is a nuisance; a helper that exits over a
    malformed line leaves somebody with no pointer at all and nothing on screen
    saying why.
    """
    try:
        with open(CONFIG) as handle:
            lines = handle.read().splitlines()
    except OSError:
        return None

    for line in lines:
        key, _, value = line.partition("=")
        if key.strip() != "speed":
            continue

        try:
            speed = float(value.strip())
        except ValueError:
            return None

        return speed if 0.10 <= speed <= 1.50 else None

    return None


def config_stamp():
    """Something that changes when the file does."""
    try:
        info = os.stat(CONFIG)
    except OSError:
        return None

    return (info.st_mtime_ns, info.st_size)


class Sway:
    """Just enough of sway's IPC to know whether a game is in front.

    Two connections, because a subscribed one only ever delivers events and
    cannot be asked a question in between. One stays open and reports that
    something happened; the other is opened per question and asks it.

    The question is asked of the tree rather than taken from the event, because
    an event says what changed and not what is in front afterwards. Closing a
    fullscreen window and revealing another one is one event about the window
    that went away.

    Every failure here is answered with "nothing is fullscreen". A seat whose
    compositor cannot be reached should behave like a desktop, where the worst
    case is a pointer somebody did not ask for and can turn off with the chord.
    """

    MAGIC = b"i3-ipc"
    SUBSCRIBE = 2
    GET_OUTPUTS = 3
    GET_TREE = 4

    def __init__(self):
        self.events = None
        self.path = self._socket_path()

        if not self.path:
            log("no sway socket, pointer mode stays manual")
            return

        try:
            self.events = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            self.events.connect(self.path)
            # Outputs as well as windows, because the size of the screen the
            # pointer moves across changes at runtime: polyseat-resize gives the
            # output the mode the connecting client asked for.
            self._send(self.events, self.SUBSCRIBE, b'["window","output"]')
            self._recv(self.events)
        except OSError as exc:
            log(f"could not subscribe to sway, pointer mode stays manual: {exc}")
            self.events = None

    @staticmethod
    def _socket_path():
        path = os.environ.get("SWAYSOCK", "")
        if path and os.path.exists(path):
            return path

        runtime = os.environ.get("XDG_RUNTIME_DIR") or f"/run/user/{os.getuid()}"
        found = sorted(glob.glob(f"{runtime}/sway-ipc.*"),
                       key=os.path.getmtime, reverse=True)

        return found[0] if found else ""

    @classmethod
    def _send(cls, sock, kind, payload=b""):
        sock.sendall(cls.MAGIC + struct.pack("=II", len(payload), kind) + payload)

    @classmethod
    def _recv(cls, sock):
        head = cls._exactly(sock, len(cls.MAGIC) + 8)
        length, _ = struct.unpack("=II", head[len(cls.MAGIC):])

        return json.loads(cls._exactly(sock, length) or b"{}")

    @staticmethod
    def _exactly(sock, count):
        buf = b""

        while len(buf) < count:
            chunk = sock.recv(count - len(buf))
            if not chunk:
                raise OSError("sway closed the connection")

            buf += chunk

        return buf

    def drain(self):
        """Take one event off the wire. True when sway is still there."""
        try:
            self._recv(self.events)

            return True
        except (OSError, ValueError) as exc:
            log(f"lost the sway connection, pointer mode stays manual: {exc}")
            self.events = None

            return False

    def _query(self, kind):
        try:
            sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            sock.connect(self.path)

            try:
                self._send(sock, kind)

                return self._recv(sock)
            finally:
                sock.close()
        except (OSError, ValueError):
            return None

    def screen_height(self):
        """How tall the output is, in pixels, or None."""
        if not self.path:
            return None

        return self._height_of(self._query(self.GET_OUTPUTS))

    @staticmethod
    def _height_of(outputs):
        """The height of the output the pointer moves across, or None.

        The first one with a size. An output sway knows about but is not driving
        reports a geometry of 0 by 0, measured by adding a second headless
        output to a seat and disabling it, so a truthy height is the whole test
        and there is no need to look at the active flag as well. Taking a zero
        from one of those would leave the pointer with a speed of nothing.
        """
        if not isinstance(outputs, list):
            return None

        for output in outputs:
            if not isinstance(output, dict):
                continue

            height = (output.get("rect") or {}).get("height")
            if height:
                return height

        return None

    def fullscreen_in_front(self):
        """Is the focused window fullscreen."""
        if not self.path:
            return False

        tree = self._query(self.GET_TREE)
        if not isinstance(tree, dict):
            return False

        return self._focused_is_fullscreen(tree)

    # Only real windows. Workspaces report fullscreen_mode 1 themselves, which
    # is a quirk inherited from i3 and reads as "this workspace may hold a
    # fullscreen window" rather than as an answer about anything on it. Sway
    # focuses the workspace when its last window closes, so without this the
    # helper concluded that an empty desktop was a game and left the pointer
    # off exactly when somebody needed it.
    WINDOWS = ("con", "floating_con")

    @classmethod
    def _focused_is_fullscreen(cls, node):
        # fullscreen_mode is 1 for a window filling its output and 2 for one
        # covering everything. Both mean a game rather than a desktop.
        if (node.get("focused")
                and node.get("type") in cls.WINDOWS
                and node.get("fullscreen_mode", 0)):
            return True

        for child in node.get("nodes", []) + node.get("floating_nodes", []):
            if cls._focused_is_fullscreen(child):
                return True

        return False


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


class Chord:
    """Two of the system buttons, held rather than tapped.

    Kept out of the loop and given its own state because it is the whole of the
    protection: a chord that fires on the press is a chord a game can trip, and
    Select with Start is an input a game may well ask for at once.

    Counting rather than naming, because which of the three arrive is the
    client's decision and not the player's. See CHORD.

    Nothing here reads a clock. The time comes in from the caller, which is
    what makes a second of holding something that can be tested rather than
    waited for.
    """

    def __init__(self, buttons, size, hold):
        self.buttons = frozenset(buttons)
        self.size = size
        self.hold = hold

        # When the chord became complete, or None while it is not complete and
        # also once it has fired.
        self.since = None

        # Set from firing until the chord is broken, so that a hand resting on
        # both buttons toggles the mode once rather than once a second.
        self.fired = False

        # Which pad completed it, so the buzz goes back to the hands that did.
        self.fd = None

    def update(self, held, fd, now):
        """A button of the chord went down or came up."""
        if len(self.buttons & set(held)) < self.size:
            self.forget()
        elif self.since is None and not self.fired:
            self.since, self.fd = now, fd

    def due(self, now):
        """True exactly once, when the chord has been held long enough."""
        if self.since is None or now - self.since < self.hold:
            return False

        self.since, self.fired = None, True

        return True

    def forget(self):
        """Back to nothing held.

        Also for a pad that vanished mid-chord: buttons that were down when it
        went never send their release, and half a chord surviving its own
        controller would let the next pad complete it with one button.
        """
        self.since, self.fired = None, False


class Rumble:
    """A short buzz on the pad that asked for the switch.

    Sunshine's virtual pads carry force feedback back to the client, and
    Moonlight hands it to the real controller, so this reaches the hands that
    held the chord rather than anything in the seat.

    **It happens on a thread of its own, and that is not tidiness.** Uploading
    an effect to a pad that is itself a uinput device is a request to the
    process behind it, and the kernel gives that process thirty seconds to
    answer before giving up. Sunshine answers at once, because games rely on
    the same path, but a Sunshine that did not would otherwise stop the pointer
    dead for half a minute. So the loop hands the buzz over and carries on.

    The effect is uploaded once per pad and replayed afterwards. A device holds
    a small, fixed number of effects, and uploading a new one per buzz fills
    that up and ends with a pad that cannot rumble at all.

    Every failure is answered with silence. A pad without force feedback, a
    driver that refuses the upload, a device that went away between the switch
    and the buzz: none of that is worth losing the pointer over, and the switch
    itself has already happened by the time this is called.
    """

    def __init__(self):
        # Only the worker touches this, which is why the main loop asks for a
        # pad to be forgotten through the queue rather than reaching in.
        self.effects = {}
        self.work = queue.Queue()

        threading.Thread(target=self._serve, daemon=True).start()

    def buzz(self, device):
        if device is not None:
            self.work.put(("buzz", device))

    def forget(self, fd):
        """Drop what was uploaded to a pad that is gone.

        File descriptors are reused, so an entry left behind here would be
        found again by the next pad to open on the same number and replayed as
        an effect that pad never received.
        """
        self.work.put(("forget", fd))

    def _serve(self):
        while True:
            what, subject = self.work.get()

            if what == "forget":
                self.effects.pop(subject, None)
                continue

            try:
                self._play(subject)
            except Exception as exc:               # noqa: BLE001
                # A thread that dies takes every later buzz with it silently,
                # which is worse than any single failed one.
                log(f"the rumble went wrong and was dropped: {exc}")

    def _play(self, device):
        effect = self._effect(device)
        if effect is None:
            return

        try:
            device.write(ecodes.EV_FF, effect, 1)
        except OSError as exc:
            log(f"{device.name} would not rumble: {exc}")
            self.effects[device.fd] = None

    def _effect(self, device):
        """The pad's uploaded effect, uploading it the first time. None if it
        cannot have one."""
        if device.fd in self.effects:
            return self.effects[device.fd]

        # Recorded before the attempt, so that a pad which cannot take one is
        # asked once rather than on every switch for the rest of the session.
        self.effects[device.fd] = None

        if (ecodes.FF_RUMBLE not in device.capabilities().get(ecodes.EV_FF, [])
                or not device.ff_effects_count):
            log(f"{device.name} has no rumble, so switching by hand is silent there")

            return None

        effect = ff.Effect(
            ecodes.FF_RUMBLE, -1, 0,
            ff.Trigger(0, 0),
            ff.Replay(RUMBLE_MS, 0),
            ff.EffectType(ff_rumble_effect=ff.Rumble(
                strong_magnitude=RUMBLE_STRONG, weak_magnitude=RUMBLE_WEAK)),
        )

        try:
            self.effects[device.fd] = device.upload_effect(effect)
        except OSError as exc:
            log(f"could not give {device.name} something to rumble with: {exc}")

        return self.effects[device.fd]


def toggle_keyboard():
    try:
        subprocess.Popen([KEYBOARD], stdout=subprocess.DEVNULL,
                         stderr=subprocess.DEVNULL)
    except OSError as exc:
        log(f"could not reach the on-screen keyboard: {exc}")


def main():
    seat = os.environ.get("XDG_SEAT", "")

    pointer = Pointer(seat)
    rumble = Rumble()
    sway = Sway()

    log("ready. The pointer follows what is in front, and two of Select, Start "
        "and Guide held for a second override it until that changes")

    ranges = {}
    by_fd = {}
    paths = set()

    def rescan():
        """Pick up gamepads that were not there a moment ago.

        This runs for the life of the session and a gamepad does not. It
        appears when somebody starts streaming and the broker attaches it, and
        it goes away again when they stop, so over an evening the same seat
        sees several. Scanning once at startup, which is what this did first,
        meant the helper worked until the first person stopped playing and was
        dead to everyone afterwards.
        """
        for device in pads():
            if device.path in paths:
                device.close()
                continue

            paths.add(device.path)
            by_fd[device.fd] = device
            ranges[device.fd] = axis_info(device)
            log(f"following {device.name}")

    rescan()
    last_scan = time.monotonic()

    # What is in front decides, and the state is remembered so that a manual
    # override is only overruled when that actually changes rather than by the
    # next unrelated window event.
    fullscreen = sway.fullscreen_in_front()
    active = not fullscreen

    height = sway.screen_height() or FALLBACK_HEIGHT
    setting = configured_speed()
    stamp = config_stamp()
    speed = (setting or SPEED) * height

    log(f"pointer mode {'off' if fullscreen else 'on'} to begin with, "
        f"{speed:.0f} pixels per second across a {height} pixel screen"
        + ("" if setting is None else f", from this seat's setting of {setting:.2f}"))

    held = set()
    chord = Chord(CHORD, CHORD_SIZE, HOLD)

    # Left stick points, right stick scrolls. That is the way round the
    # Windows tools do it and the way it is worth matching: the hand that
    # aims in a game is the hand that expects to aim here.
    axes = {ecodes.ABS_X: 0.0, ecodes.ABS_Y: 0.0, ecodes.ABS_RY: 0.0}
    scroll_debt = 0.0
    last = time.monotonic()

    from select import select

    while True:
        watching = [sway.events.fileno()] if sway.events else []
        readable, _, _ = select(list(by_fd) + watching, [], [], INTERVAL)

        if sway.events and sway.events.fileno() in readable:
            # Several events can arrive for one change, so the tree is asked
            # once afterwards rather than once per event.
            while sway.events and sway.drain():
                more, _, _ = select([sway.events.fileno()], [], [], 0)
                if not more:
                    break

            # The output may have taken the mode a connecting client asked for,
            # which changes how far a second of full deflection should carry.
            now_height = sway.screen_height() or FALLBACK_HEIGHT

            if now_height != height:
                height = now_height
                speed = (setting or SPEED) * height
                log(f"screen is {height} pixels tall, {speed:.0f} pixels per second")

            now_fullscreen = sway.fullscreen_in_front()

            if now_fullscreen != fullscreen:
                fullscreen = now_fullscreen
                active = not fullscreen
                log("a fullscreen application is in front, pointer mode off"
                    if fullscreen else "back on the desktop, pointer mode on")

                if not active:
                    pointer.release_all()

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
                    paths.discard(device.path)
                    rumble.forget(fd)

                    # Whoever was holding it is gone, so nothing should stay
                    # pressed and the next person should not inherit a mode
                    # that was set by hand. Back to whatever is in front.
                    #
                    # Buttons held at the moment a pad vanishes never send
                    # their release, so what was down has to be forgotten too.
                    # Otherwise half a chord survives its own controller and
                    # the next pad completes it with one button.
                    if not by_fd:
                        pointer.release_all()
                        active = not fullscreen
                        held.clear()
                        chord.forget()

                    continue
                raise

            for event in events:
                if event.type == ecodes.EV_KEY:
                    if event.value:
                        held.add(event.code)
                    else:
                        held.discard(event.code)

                    if event.code in CHORD:
                        chord.update(held, fd, time.monotonic())
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

        # The chord is held rather than tapped, so it fires here on the clock
        # instead of on the press. The loop turns over every INTERVAL whether
        # anything arrived or not, which is what makes that possible without a
        # timer of its own.
        if chord.due(now):
            active = not active
            log(f"pointer mode {'on' if active else 'off'} by hand, "
                "until something goes fullscreen or stops being")

            if not active:
                pointer.release_all()

            rumble.buzz(by_fd.get(chord.fd))

        if now - last_scan >= 2.0:
            last_scan = now
            rescan()

            # The web interface may have changed this seat's speed. Compared
            # rather than reread every time round, so that a helper sitting idle
            # is not opening a file ninety times a second.
            if config_stamp() != stamp:
                stamp = config_stamp()
                setting = configured_speed()
                speed = (setting or SPEED) * height
                log(f"pointer speed is now {speed:.0f} pixels per second"
                    + ("" if setting is None else f", from a setting of {setting:.2f}"))

        if not active:
            continue

        x = axes[ecodes.ABS_X]
        y = axes[ecodes.ABS_Y]

        if x or y:
            pointer.move(
                (abs(x) ** CURVE) * (1 if x > 0 else -1) * speed * elapsed,
                (abs(y) ** CURVE) * (1 if y > 0 else -1) * speed * elapsed,
            )

        # Scrolling is inverted against the axis: pushing the stick up should
        # move the page up, which means the wheel turns the other way.
        wheel = axes[ecodes.ABS_RY]
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
