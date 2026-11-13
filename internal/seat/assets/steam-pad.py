#!/usr/bin/env python3
"""polyseat-steam-pad - Steam sees the controller only while Steam is in front.

Steam runs all session long in gamescope on the second workspace, and it reads
every gamepad in the seat whether or not anybody is looking at it. Reported from
the couch: playing Animal Crossing in Eden, a press on the Xbox button opened Big
Picture on the other workspace, and from then on every button went to Steam as
well as to the game.

Measured in seats vince and joser on 2026-11-13, with a uinput pad and Steam's
own logs:

    Steam never grabs a pad. A second reader got every event, and sway's focus
    stayed where it was. So the game keeps working, and Steam acts on the same
    presses in a window nobody can see.

    A Steam nobody has signed in to opens Big Picture on the Xbox button
    straight from the silent start. A signed in one does so as soon as it has
    any window of its own, Big Picture or the desktop one, which after the
    first evening is always.

    Steam's own switch for that, "Guide button focuses Steam", changed nothing:
    set in config.vdf, in localconfig.vdf, and through Steam's settings call
    over the debugging port, Big Picture opened every time.

    Steam does honour SDL_GAMECONTROLLER_IGNORE_DEVICES_EXCEPT. Started with
    0x046d/0xc21d, it opened a pad with that id and left a 045e:0b13, the id
    Sunshine gives an Xbox client, completely alone.

So the pad Sunshine makes stays exactly what it was, for every application in
the seat, and Steam is pointed at a second one that this helper keeps: the
Steam pad. It carries the controller while Steam is in front and nothing while
anything else is. A game outside Steam, Eden or Faugus or a launcher, still has
the controller to itself, the Xbox button included, and Steam simply never hears
of it.

**Why the Steam pad is an F310.** It needs an id Sunshine never hands out, so
that the exception names this pad and nothing else, and one that Steam and SDL
both know as an XInput pad. Logitech's F310 in its XInput mode is both.

**Games that Steam starts inherit the exception, and that is what they need.**
Steam replaces SDL_GAMECONTROLLER_IGNORE_DEVICES for every game with its own
list, measured with DREDGE, so a game started from Steam would otherwise see
Sunshine's pad and the Steam pad, which carry the same presses while Steam is in
front. The exception survives into the game, so it sees the Steam pad alone.
And where Steam Input wraps the Steam pad, measured by turning Steam's Xbox
support on, DREDGE held Steam's virtual pad (28de:11ff) and not the Steam pad
underneath it, so nothing arrives twice.

**Everything else is told to leave the Steam pad alone**, through
SDL_GAMECONTROLLER_IGNORE_DEVICES in the session's units. It is silent while
they are in front, but a game that only listens to the first controller it finds
could find that one.

The pad is kept for as long as Sunshine's pad exists rather than made and
dropped on every change of focus. A controller that disconnects whenever
somebody switches workspace would pause Steam games, pop notices in Big Picture
and renumber players.

Rumble goes the other way: what a Steam game uploads to the Steam pad is
uploaded to Sunshine's pad in turn, and Sunshine sends it to the client.
"""

import errno
import glob
import json
import os
import queue
import socket
import struct
import sys
import threading
import time
from select import select

import evdev
from evdev import ecodes

# The Steam pad's identity. polyseat-steam names the same id in the exception
# it starts Steam with, and the session units name it in the list everybody else
# ignores; a test holds the three to the same value.
VENDOR = 0x046D
PRODUCT = 0xC21D
EXCEPTION = f"0x{VENDOR:04x}/0x{PRODUCT:04x}"

# Every device this helper or the pointer helper creates carries this, so that
# neither mistakes the other's devices for a controller from a client.
PREFIX = "polyseat:"

# Steam's own virtual pads, which it makes when Steam Input wraps a controller
# for a game. Mirroring one of those into the Steam pad would feed Steam its
# own output.
VALVE = 0x28DE

# How often the gamepads are listed again. A pad appears when somebody starts
# streaming and goes when they stop.
RESCAN = 2.0

# The sticks rest at their centre. Everything else - triggers, the D-pad - rests
# at zero, or at its minimum where zero is not in range.
STICKS = (ecodes.ABS_X, ecodes.ABS_Y, ecodes.ABS_RX, ecodes.ABS_RY)


def log(message):
    print(f"polyseat-steam-pad: {message}", flush=True)


def steam_in_front(tree):
    """Whether the focused window is Steam.

    gamescope is what Steam lives in, and the only window sway ever sees of it.
    A Steam window of its own is the arrangement polyseat-steam falls back to
    when gamescope will not start, and it is Steam all the same.

    Nothing focused, or a focused workspace with no window on it, is not Steam.
    """
    if not isinstance(tree, dict):
        return False

    if tree.get("focused") and tree.get("pid"):
        if tree.get("app_id") == "gamescope":
            return True

        properties = tree.get("window_properties") or {}

        return properties.get("class") == "steam"

    for key in ("nodes", "floating_nodes"):
        for child in tree.get(key) or []:
            if steam_in_front(child):
                return True

    return False


def is_source(name, vendor, keys, axes):
    """Whether a device is a controller from a client.

    By capability, like the pointer helper: Sunshine names its pads after the
    model the client asked for. Not one of ours, and not one of Steam's.
    """
    if name.startswith(PREFIX) or vendor == VALVE:
        return False

    return ecodes.BTN_SOUTH in keys and ecodes.ABS_X in axes


def rest(code, absinfo):
    """Where an axis sits when nobody touches it."""
    if code in STICKS:
        return (absinfo.min + absinfo.max) // 2

    return 0 if absinfo.min <= 0 <= absinfo.max else absinfo.min


class Sway:
    """Just enough of sway's IPC to know whether Steam is in front.

    The same arrangement as in the pointer helper: one connection subscribed
    to events, and a fresh one for each question, because a subscribed
    connection only ever delivers events.

    Every failure answers "Steam is in front". A seat whose compositor cannot be
    asked is better off with Steam reading the controller, which is how every
    seat worked before this existed, than with Steam deaf to it.
    """

    MAGIC = b"i3-ipc"
    SUBSCRIBE = 2
    GET_TREE = 4

    def __init__(self):
        self.events = None
        self.path = self._socket_path()

        if not self.path:
            log("no sway socket, so Steam keeps the controller")
            return

        try:
            self.events = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            self.events.connect(self.path)
            self._send(self.events, self.SUBSCRIBE, b'["window","workspace"]')
            self._recv(self.events)
        except OSError as exc:
            log(f"could not subscribe to sway, so Steam keeps the controller: {exc}")
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
            log(f"lost the sway connection, so Steam keeps the controller: {exc}")
            self.events = None

            return False

    def steam_in_front(self):
        if not self.path or self.events is None:
            return True

        try:
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
                sock.connect(self.path)
                self._send(sock, self.GET_TREE)
                tree = self._recv(sock)
        except (OSError, ValueError):
            return True

        return steam_in_front(tree)


class Rumble:
    """Force feedback from the Steam pads to the pads behind them.

    On a thread of its own, because an upload to a pad that is itself a
    uinput device waits for the process behind it, and a game uploading to the
    Steam pad waits for this in turn. Neither should hold up the controller.
    """

    def __init__(self):
        self.work = queue.Queue()

        threading.Thread(target=self._serve, daemon=True).start()

    def put(self, *job):
        self.work.put(job)

    def _serve(self):
        while True:
            job = self.work.get()

            try:
                job[0](*job[1:])
            except Exception as exc:               # noqa: BLE001
                # A thread that dies takes every later effect with it, and an
                # upload nobody answers leaves a game waiting on the kernel.
                log(f"a rumble request went wrong and was dropped: {exc}")


class Mirror:
    """One controller from a client and the Steam pad that stands for it."""

    def __init__(self, source, seat, rumble):
        self.source = source
        self.rumble = rumble

        caps = source.capabilities(absinfo=True)
        self.keys = list(caps.get(ecodes.EV_KEY, []))
        self.axes = dict(caps.get(ecodes.EV_ABS, []))
        self.ff = list(caps.get(ecodes.EV_FF, []))

        events = {ecodes.EV_KEY: self.keys,
                  ecodes.EV_ABS: list(self.axes.items())}
        if self.ff:
            events[ecodes.EV_FF] = self.ff

        tag = f" ({seat})" if seat else ""

        self.pad = evdev.UInput(
            events,
            name=f"{PREFIX}steam pad{tag}",
            vendor=VENDOR, product=PRODUCT, version=0x0305,
            bustype=ecodes.BUS_USB,
            max_effects=max(1, source.ff_effects_count) if self.ff else 0,
        )

        # Effects the Steam pad holds, by its id, and what they are on the
        # source. Only the rumble thread touches it.
        self.effects = {}

        self.active = False

    def close(self):
        try:
            self.pad.close()
        except OSError:
            pass

        try:
            self.source.close()
        except OSError:
            pass

    def forward(self, event):
        if not self.active or event.type not in (ecodes.EV_KEY, ecodes.EV_ABS,
                                                 ecodes.EV_SYN):
            return

        self.pad.write(event.type, event.code, event.value)

    def activate(self):
        """Steam is in front: hand it the controller as it is right now.

        As it is right now rather than from the next event on, because the
        player is often holding something at the moment of the switch, and a
        stick that Steam believes is centred while it is not drifts the other
        way the moment it is let go.
        """
        if self.active:
            return

        self.active = True
        held = set(self.source.active_keys())

        for code in self.keys:
            self.pad.write(ecodes.EV_KEY, code, 1 if code in held else 0)

        for code in self.axes:
            self.pad.write(ecodes.EV_ABS, code, self.source.absinfo(code).value)

        self.pad.syn()

    def deactivate(self):
        """Something else is in front: let go of everything.

        A button that was down at the moment of the switch would otherwise stay
        down in Steam until Steam is in front again, and a stick would keep
        steering whatever Steam has open.
        """
        if not self.active:
            return

        self.active = False

        for code in self.keys:
            self.pad.write(ecodes.EV_KEY, code, 0)

        for code, absinfo in self.axes.items():
            self.pad.write(ecodes.EV_ABS, code, rest(code, absinfo))

        self.pad.syn()

        # And whatever Steam had rumbling.
        for effect in list(self.effects.values()):
            self.rumble.put(self._play, effect, 0)

    # ---------------------------------------------------------------- rumble
    #
    # These run on the rumble thread.

    def requests(self):
        """Hand what a game asked of the Steam pad to the rumble thread."""
        try:
            events = list(self.pad.read())
        except (BlockingIOError, InterruptedError):
            return

        for event in events:
            if event.type == ecodes.EV_UINPUT and event.code == ecodes.UI_FF_UPLOAD:
                self.rumble.put(self._upload, event.value)
            elif event.type == ecodes.EV_UINPUT and event.code == ecodes.UI_FF_ERASE:
                self.rumble.put(self._erase, event.value)
            elif event.type == ecodes.EV_FF:
                if event.code in (ecodes.FF_GAIN, ecodes.FF_AUTOCENTER):
                    self.rumble.put(self._play, event.code, event.value)
                else:
                    self.rumble.put(self._play_ours, event.code, event.value)

    def _upload(self, request):
        upload = self.pad.begin_upload(request)
        upload.retval = 0

        try:
            ours = upload.effect.id
            upload.effect.id = self.effects.get(ours, -1)
            self.effects[ours] = self.source.upload_effect(upload.effect)
            upload.effect.id = ours
        except OSError as exc:
            upload.retval = -(exc.errno or errno.EIO)
        finally:
            self.pad.end_upload(upload)

    def _erase(self, request):
        erase = self.pad.begin_erase(request)
        erase.retval = 0

        try:
            theirs = self.effects.pop(erase.effect_id, None)
            if theirs is not None:
                self.source.erase_effect(theirs)
        except OSError as exc:
            erase.retval = -(exc.errno or errno.EIO)
        finally:
            self.pad.end_erase(erase)

    def _play_ours(self, ours, value):
        """An effect a game started on the Steam pad, on the pad behind it.

        Only while Steam is in front. A Steam game left running in the
        background has no business shaking the hands of somebody playing
        something else.
        """
        theirs = self.effects.get(ours)

        if self.active and theirs is not None:
            self._play(theirs, value)

    def _play(self, code, value):
        try:
            self.source.write(ecodes.EV_FF, code, value)
        except OSError:
            pass


def sources(known):
    """Controllers from clients that are not followed yet.

    Every question to a device inside the try: a pad can go between being
    listed and being asked, and Sunshine takes one away the moment a stream
    stops. See the same function in the pointer helper.
    """
    found = []

    for path in evdev.list_devices():
        if path in known:
            continue

        device = None

        try:
            device = evdev.InputDevice(path)
            caps = device.capabilities()
            keys = caps.get(ecodes.EV_KEY, [])
            axes = [code for code, _ in caps.get(ecodes.EV_ABS, [])]

            if is_source(device.name, device.info.vendor, keys, axes):
                found.append(device)
            else:
                device.close()
        except OSError:
            if device is not None:
                try:
                    device.close()
                except OSError:
                    pass

    return found


def main():
    seat = os.environ.get("XDG_SEAT", "")
    rumble = Rumble()
    sway = Sway()

    mirrors = {}         # source path -> Mirror
    by_fd = {}           # source fd or Steam pad fd -> (Mirror, which)

    front = sway.steam_in_front()
    log(f"ready. Steam {'is' if front else 'is not'} in front")

    def follow():
        for device in sources(set(mirrors)):
            try:
                mirror = Mirror(device, seat, rumble)
            except (OSError, evdev.UInputError) as exc:
                log(f"could not make a Steam pad for {device.name}: {exc}")
                device.close()
                continue

            mirrors[device.path] = mirror
            by_fd[device.fd] = (mirror, "source")
            by_fd[mirror.pad.fd] = (mirror, "pad")

            if front:
                mirror.activate()

            log(f"following {device.name}")

    def drop(mirror):
        mirrors.pop(mirror.source.path, None)

        for fd in [fd for fd, (m, _) in by_fd.items() if m is mirror]:
            by_fd.pop(fd, None)

        mirror.close()

    follow()
    last_scan = time.monotonic()

    while True:
        watching = [sway.events.fileno()] if sway.events else []
        timeout = max(0.0, RESCAN - (time.monotonic() - last_scan))
        readable, _, _ = select(list(by_fd) + watching, [], [], timeout)

        if sway.events and sway.events.fileno() in readable:
            while sway.events and sway.drain():
                more, _, _ = select([sway.events.fileno()], [], [], 0)
                if not more:
                    break

            now_front = sway.steam_in_front()

            if now_front != front:
                front = now_front
                log("Steam is in front, it has the controller" if front
                    else "something else is in front, Steam does not hear the controller")

                for mirror in mirrors.values():
                    if front:
                        mirror.activate()
                    else:
                        mirror.deactivate()

        for fd in readable:
            entry = by_fd.get(fd)
            if entry is None:
                continue

            mirror, which = entry

            if which == "pad":
                mirror.requests()
                continue

            try:
                for event in mirror.source.read():
                    mirror.forward(event)
            except (BlockingIOError, InterruptedError):
                continue
            except OSError as exc:
                if exc.errno in (errno.ENODEV, errno.EBADF):
                    log(f"{mirror.source.name} went away, and its Steam pad with it")
                else:
                    log(f"{mirror.source.name} could not be read and is dropped "
                        f"until the next rescan: {exc}")

                drop(mirror)

        if time.monotonic() - last_scan >= RESCAN:
            last_scan = time.monotonic()
            follow()


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit(0)
