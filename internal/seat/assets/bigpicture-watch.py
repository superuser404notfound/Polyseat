#!/usr/bin/env python3
"""Polyseat - put Steam Big Picture back to fullscreen when it loses it.

Reported from a couch: start a game from Big Picture, quit the game, and Big
Picture comes back as an ordinary tiled window sharing the screen with the
welcome terminal. Restarting it fixes it, which is what made it look like a
Steam problem.

It is not one. sway allows a single fullscreen container per workspace, so when
the game maps its window and goes fullscreen, sway takes fullscreen away from
Big Picture. Nothing gives it back when the game exits, because nothing was
watching: the rule in the sway configuration runs when a window is mapped, and
this window was mapped long before, while polyseat-bigpicture stops insisting as
soon as it has seen fullscreen once, on purpose, so that leaving Big Picture for
the desktop does not drag you back into it.

So this watches instead of polling, and only for the two things that are not
somebody's decision:

  - a fullscreen window closed. That is a game ending, and if Big Picture is
    there and windowed it is windowed because sway took it away.
  - a window turned into Big Picture, by being mapped as it or by being renamed
    to it. Renaming is the cold Steam case the sway rule cannot catch: the
    window is mapped with the title "Steam" and renamed a moment later, so the
    rule matching on the title never fires.

What it deliberately does not react to is fullscreen_mode changing on a window
that stays. That is $mod+f, somebody asking for the window not to be fullscreen,
and a helper that undoes it half a second later is a helper that has to be
killed to use the seat.
"""

import glob
import json
import os
import socket
import struct
import sys

MAGIC = b"i3-ipc"
RUN_COMMAND = 0
SUBSCRIBE = 2
GET_TREE = 4

# What Steam calls the window. The same string the sway rule and
# polyseat-bigpicture match on, and a test checks that the three agree.
WANT = "Steam Big Picture Mode"


def log(message):
    print(f"polyseat-bigpicture-watch: {message}", file=sys.stderr, flush=True)


def socket_path():
    path = os.environ.get("SWAYSOCK", "")
    if path and os.path.exists(path):
        return path

    runtime = os.environ.get("XDG_RUNTIME_DIR") or f"/run/user/{os.getuid()}"
    found = sorted(glob.glob(f"{runtime}/sway-ipc.*"),
                   key=os.path.getmtime, reverse=True)

    return found[0] if found else ""


def send(sock, kind, payload=b""):
    sock.sendall(MAGIC + struct.pack("=II", len(payload), kind) + payload)


def exactly(sock, count):
    buf = b""

    while len(buf) < count:
        chunk = sock.recv(count - len(buf))
        if not chunk:
            raise OSError("sway closed the connection")

        buf += chunk

    return buf


def recv(sock):
    head = exactly(sock, len(MAGIC) + 8)
    length, _ = struct.unpack("=II", head[len(MAGIC):])

    return json.loads(exactly(sock, length) or b"{}")


def worth_looking(event):
    """Whether this window event is one of the two that mean anything here."""
    change = event.get("change")
    container = event.get("container") or {}

    if change == "close":
        return bool(container.get("fullscreen_mode"))

    if change in ("new", "title"):
        return container.get("name") == WANT and not container.get("fullscreen_mode")

    return False


def windowed_big_picture(tree):
    """The id of a Big Picture window that is not fullscreen, or None.

    Asked of the tree rather than taken from the event, because an event says
    what changed and this needs to know what is there afterwards: the window
    that closed is not the window this is about.
    """
    if tree.get("name") == WANT and tree.get("pid"):
        return None if tree.get("fullscreen_mode") else tree.get("id")

    for key in ("nodes", "floating_nodes"):
        for child in tree.get(key) or []:
            found = windowed_big_picture(child)
            if found is not None:
                return found

    return None


def insist(path, why):
    """Ask sway for fullscreen, on its own connection."""
    try:
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
            sock.connect(path)
            send(sock, GET_TREE)
            target = windowed_big_picture(recv(sock))

            if target is None:
                return

            send(sock, RUN_COMMAND, f"[con_id={target}] fullscreen enable".encode())
            recv(sock)
    except (OSError, ValueError) as exc:
        log(f"could not ask sway for fullscreen: {exc}")

        return

    log(f"Big Picture was windowed after {why}, so it is fullscreen again")


def main():
    path = socket_path()
    if not path:
        log("no sway socket, Big Picture will keep whatever size it is given")

        return 0

    try:
        events = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        events.connect(path)
        send(events, SUBSCRIBE, b'["window"]')
        recv(events)
    except OSError as exc:
        log(f"could not subscribe to sway: {exc}")

        return 0

    while True:
        try:
            event = recv(events)
        except (OSError, ValueError):
            # sway going away is the session ending, and the session starts this
            # again when it comes back.
            return 0

        if not worth_looking(event):
            continue

        why = "a fullscreen window closed" if event.get("change") == "close" \
            else "it appeared without fullscreen"

        insist(path, why)


if __name__ == "__main__":
    sys.exit(main())
