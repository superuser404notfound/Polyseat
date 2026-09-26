#!/usr/bin/env python3
"""Polyseat - keep Steam Big Picture owning the screen.

**A fallback since Steam moved into gamescope, and kept as one.** sway sees a
single window with the app_id gamescope there, never a Steam window, so on the
ordinary path this matches nothing and sleeps on sway's socket. It still
matters when polyseat-steam ends up with a Steam outside gamescope: gamescope
failing on its second try, or a Steam that would not close. A Big Picture that
owns the screen is then what stands between the player and a window in a
corner. Everything below was written for that arrangement, when it was the
only one, and still describes it.

Two things reported from a couch, both of which look like a Steam problem and
are neither.

The first: start a game from Big Picture, quit the game, and Big Picture comes
back as an ordinary tiled window next to the welcome terminal. sway allows a
single fullscreen container per workspace, so when the game maps its window and
goes fullscreen, sway takes fullscreen away from Big Picture. Nothing gives it
back when the game exits, because nothing was watching: the rule in the sway
configuration runs when a window is mapped, and this window was mapped long
before.

The first version of this watched for a window called "Steam Big Picture Mode"
and never fired, because Steam translates that title. A German session calls the
window "Big-Picture-Modus". So the part that matters here reads sway's own
events rather than any sentence:

    fullscreen_mode  id=11  fullscreen_mode=0    Big Picture is dethroned
    fullscreen_mode  id=13  fullscreen_mode=1    the game takes it
    close            id=13                       the game ends, so put 11 back

Two things about that, both learned from a seat that kept the bug after it was
supposedly fixed. Only Big Picture is ever remembered as the window that lost
fullscreen: a game hands fullscreen between its own windows, and a version that
remembered whichever window it saw lose it spent the memory on the game's
launcher and had nothing left for Big Picture. The journal of that seat says so
in one line, `put window 21 back to fullscreen`, where 21 was a window of Forza
Horizon 6 and Big Picture sat tiled behind it.

And every window that takes fullscreen afterwards is remembered, not just the
first, because a game is often two windows: a launcher that goes fullscreen, and
the game it starts, which takes it from the launcher. Big Picture is put back
when the last of them is gone, so a launcher closing while the game runs no
longer pulls the player out of the game.

Pressing $mod+f is the case this must never undo. It looks like the first line
above and nothing follows it, so no window is ever remembered as having taken
fullscreen, and nothing is put back.

The other thing a cold Big Picture does - owning the whole screen while
painting 1280x800 of it - lives in polyseat-bigpicture and not here. It is not
about fullscreen at all: the window has it. Big Picture is a fixed 1280x800
interface that Steam scales to its window, and on a cold start it works that
scale out once, while the window is still its own 1280x800, and never again.
The launcher is the one that knows whether Steam was started cold, which is
the only thing that turned out to matter, so it is the one that does something
about it.

The title appears below for the one case the events cannot describe: a cold
Steam maps its window before it has a title, so the rule matching on the title
never fires, and the window stays in a corner with nothing having taken
anything from it. That match is a pattern rather than an English sentence, and
a test holds the three files that carry it to the same one.
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

# How long after Big Picture loses fullscreen another window may take it and
# still count as having taken it. sway does both in one transaction, so the
# events arrive together; this is loose only to survive a busy seat.
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


def talk(path, kind, payload=b""):
    """One request to sway, on a connection of its own, and its answer or None.

    Of its own because the connection in the loop below is subscribed to
    events, and a reply would arrive in the middle of them.
    """
    try:
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
            sock.connect(path)
            send(sock, kind, payload)

            return recv(sock)
    except (OSError, ValueError) as exc:
        log(f"sway did not answer: {exc}")

        return None


def order(path, command):
    """Tell sway to do something. What it did shows on the screen."""
    talk(path, RUN_COMMAND, command.encode())


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
    a Big Picture window that should have fullscreen back, or None.

    The state is a plain dict so that a test can drive a whole sequence of
    events through this and look at what falls out, which is the only way to
    check a rule that spans several of them.
    """
    change = event.get("change")
    container = event.get("container") or {}
    ident = container.get("id")
    full = bool(container.get("fullscreen_mode"))
    took = state.get("took")

    if change == "close":
        if ident == state.get("victim"):
            # Big Picture is gone, and its id belongs to the next window sway
            # makes.
            state.clear()

            return None

        if took and ident in took:
            took.discard(ident)

            if not took:
                # The last window that had taken the screen is gone, so there
                # is nobody holding it and Big Picture can have it back.
                victim = state.get("victim")
                state.clear()

                return victim

        return None

    if change != "fullscreen_mode":
        return None

    if not full:
        # Only Big Picture is remembered. A game hands fullscreen from its
        # launcher to itself, and a version of this that remembered whichever
        # window it saw lose it spent the memory on the launcher and put that
        # window back in the middle of the game.
        if is_big_picture(container):
            state.clear()
            state.update(victim=ident, since=now, took=set())

        return None

    victim = state.get("victim")

    if victim is None or ident == victim:
        return None

    if not took and now - state["since"] > TOGETHER:
        # Nobody took the screen in the moment Big Picture lost it, so it was
        # $mod+f, and this window minutes later has nothing to do with it.
        state.clear()

        return None

    # Everything that takes the screen after Big Picture lost it, not only the
    # first: a game is often a launcher that goes fullscreen and a game that
    # takes it from the launcher, and Big Picture is due the screen back when
    # the last of them is gone.
    took.add(ident)

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


def held(tree, ident):
    """Whether some window other than this one is fullscreen.

    Asked before putting Big Picture back, because this can only be as right as
    the events it saw, and taking the screen away from a game somebody is
    playing is worse than the bug it is here to fix.
    """
    if (tree.get("pid") and tree.get("id") != ident
            and tree.get("fullscreen_mode")):
        return True

    for key in ("nodes", "floating_nodes"):
        for child in tree.get(key) or []:
            if held(child, ident):
                return True

    return False


def ask(path, pick, why):
    """Ask sway for fullscreen.

    pick is given the tree and answers with a window id or None, because an
    event says what changed and this needs to know what is there afterwards.
    """
    tree = talk(path, GET_TREE)
    if tree is None:
        return

    target = pick(tree)
    if target is None:
        return

    order(path, f"[con_id={target}] fullscreen enable")
    log(f"put window {target} back to fullscreen after {why}")


def again(ident):
    """Pick that window, if it is still there, still Big Picture, still
    windowed, and nothing else is holding the screen."""
    def pick(tree):
        node = window(tree, ident)

        if node is None or node.get("fullscreen_mode"):
            return None

        if not is_big_picture(node) or held(tree, ident):
            return None

        return ident

    return pick


def main():
    path = socket_path()
    if not path:
        log("no sway socket, so Big Picture stays wherever a game leaves it")

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
            # sway going away is the session ending, and the session starts
            # this again when it comes back.
            return 0

        # Both, always, even when the first one answers: the second is also
        # what keeps track of which windows have been seen, and a close that
        # ends a game is a close it has to hear about.
        back = dethroned(state, event, time.monotonic())
        fresh = cold_start(seen, event)

        if back is not None:
            ask(path, again(back),
                "the window that took fullscreen from it closed")
        elif fresh is not None:
            ask(path, again(fresh), "Big Picture appeared without fullscreen")


if __name__ == "__main__":
    sys.exit(main())
