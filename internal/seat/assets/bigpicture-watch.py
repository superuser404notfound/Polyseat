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
this window was mapped long before.

The first version of this watched for a window called "Steam Big Picture Mode"
and never fired, because Steam translates that title. A German session calls the
window "Big-Picture-Modus". So the part that matters here does not look at the
title at all. It remembers, from sway's own events, which window had fullscreen
taken away and who took it, and puts the first one back when the second closes:

    fullscreen_mode  id=11  fullscreen_mode=0    Big Picture is dethroned
    fullscreen_mode  id=13  fullscreen_mode=1    the game takes it
    close            id=13                       the game ends, so put 11 back

That works whatever the two windows are called and whatever language Steam is
in, and it is the only reading of those events that is not somebody's decision.
The two have to be close together in time, or a window somebody left fullscreen
by hand an hour ago would be dragged back the next time any game ended.

Pressing $mod+f is the case this must never undo. It looks like the first line
above and nothing follows it, so no window is ever remembered as the one that
took fullscreen, and nothing is put back.

The title still appears below, for the one case the events cannot describe: a
cold Steam maps its window before it has a title, so the rule in the sway
configuration matching on the title never fires, and the window stays in a
corner with nothing having taken anything from it. That match is now a pattern
rather than an English sentence, and a test holds the three files that carry it
to the same one.
"""

import glob
import json
import os
import re
import socket
import struct
import sys
import time

MAGIC = b"i3-ipc"
RUN_COMMAND = 0
SUBSCRIBE = 2
GET_TREE = 4

# Steam translates the window title, so this matches the part of it that every
# translation keeps: "Steam Big Picture Mode" in English, "Big-Picture-Modus" in
# German, "Mode Big Picture" in French. The sway configuration and
# polyseat-bigpicture carry the same pattern, and a test checks that the three
# agree.
TITLE = re.compile(r"[Bb]ig[ _-][Pp]icture")

# How long after a window loses fullscreen another window may take it and still
# count as having taken it from that one. sway does both in one transaction, so
# the events arrive together; this is loose only to survive a busy seat.
TOGETHER = 2.0


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


def is_big_picture(node):
    """Whether this window is Big Picture, in any language Steam speaks.

    The class as well as the title, so that a browser showing a page with those
    words in its tab is not mistaken for it.
    """
    if not node.get("pid"):
        return False

    properties = node.get("window_properties") or {}
    if properties.get("class") != "steam":
        return False

    return bool(TITLE.search(node.get("name") or ""))


def dethroned(state, event, now):
    """Fold one window event into what is remembered, and answer with the id of
    a window that should have fullscreen back, or None.

    The state is a plain dict so that a test can drive a whole sequence of
    events through this and look at what falls out, which is the only way to
    check a rule that spans several of them.
    """
    change = event.get("change")
    container = event.get("container") or {}
    ident = container.get("id")
    full = bool(container.get("fullscreen_mode"))

    if change == "close":
        if ident == state.get("thief"):
            # What took fullscreen is gone. Whether it was still fullscreen
            # when it went does not matter: either way nobody holds it now.
            victim = state.get("victim")
            state.clear()

            return victim

        if ident == state.get("victim"):
            state.clear()

        return None

    if change == "fullscreen_mode" and not full and ident != state.get("thief"):
        # Somebody lost fullscreen. Whether that was sway making room for
        # another window or somebody pressing the key is decided by what
        # happens next, or by nothing happening.
        state.clear()
        state.update(victim=ident, since=now)

        return None

    if (full and state.get("victim") is not None and state.get("thief") is None
            and ident != state.get("victim") and now - state["since"] <= TOGETHER):
        state["thief"] = ident

    return None


def cold_start(seen, event):
    """The id of a Big Picture window that has just appeared without fullscreen
    and has not been seen before, or None.

    This is the one case the events above cannot describe: a cold Steam maps
    its window before it has a title, so the rule in the sway configuration
    never fires for it and nobody took anything from anybody. The window is
    mapped as "Steam" and renamed a moment later, so the rename is what has to
    be caught.

    Only the first rename counts. Steam renames its window while it runs, and
    without this the player who leaves Big Picture with $mod+f would be dragged
    back into it by the next one.
    """
    change = event.get("change")
    container = event.get("container") or {}
    ident = container.get("id")

    if change == "close":
        seen.discard(ident)

        return None

    if change not in ("new", "title") or not is_big_picture(container):
        return None

    if ident in seen:
        return None

    seen.add(ident)

    return None if container.get("fullscreen_mode") else ident


def window(tree, ident):
    """The node with this id, or None."""
    if tree.get("id") == ident and tree.get("pid"):
        return tree

    for key in ("nodes", "floating_nodes"):
        for child in tree.get(key) or []:
            found = window(child, ident)
            if found is not None:
                return found

    return None


def ask(path, pick, why):
    """Ask sway for fullscreen, on its own connection.

    pick is given the tree and answers with a window id or None, because an
    event says what changed and this needs to know what is there afterwards.
    """
    try:
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
            sock.connect(path)
            send(sock, GET_TREE)
            target = pick(recv(sock))

            if target is None:
                return

            send(sock, RUN_COMMAND, f"[con_id={target}] fullscreen enable".encode())
            recv(sock)
    except (OSError, ValueError) as exc:
        log(f"could not ask sway for fullscreen: {exc}")

        return

    log(f"put window {target} back to fullscreen after {why}")


def again(ident):
    """Pick that window, but only if it is still there and still windowed."""
    def pick(tree):
        node = window(tree, ident)

        if node is None or node.get("fullscreen_mode"):
            return None

        return ident

    return pick


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

    state, seen = {}, set()

    while True:
        try:
            event = recv(events)
        except (OSError, ValueError):
            # sway going away is the session ending, and the session starts this
            # again when it comes back.
            return 0

        # Both, always, even when the first one answers: the second is also
        # what keeps track of which windows have been seen, and a close that
        # ends a game is a close it has to hear about.
        back = dethroned(state, event, time.monotonic())
        fresh = cold_start(seen, event)

        if back is not None:
            ask(path, again(back), "the window that took fullscreen from it closed")
        elif fresh is not None:
            ask(path, again(fresh), "Big Picture appeared without fullscreen")


if __name__ == "__main__":
    sys.exit(main())
