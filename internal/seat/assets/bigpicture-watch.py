#!/usr/bin/env python3
"""Polyseat - keep Steam Big Picture owning the screen.

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

The second: start the seat, pick Big Picture in Moonlight before anything else,
and it paints a small picture in the top left corner of a black screen. That one
is not about fullscreen at all, which is why it survived every fix above.
Measured in a seat rather than guessed:

    container        id=6   rect 3840x2160   geometry 1280x800   fullscreen 1

Steam maps the window at its own 1280x800, the rule in the sway configuration
makes it fullscreen before the Steam UI process is running, and what finally
paints keeps the size the window had when it was mapped. Every pixel outside
that corner is exactly black, and `fullscreen_mode` is 1, so the launcher and
this watcher both thought the job was done. Asking sway for fullscreen a second
time, once the UI is alive, is a configure Steam does react to: one off-and-on
and the picture fills the screen. That is what a player does by hand when they
restart Big Picture and it "fixes itself".

Nothing in sway's tree says what a client painted, so the screen itself is
asked, with grim, at four points. The title still appears below for the one
case the events cannot describe: a cold Steam maps its window before it has a
title, so the rule matching on the title never fires, and the window stays in a
corner with nothing having taken anything from it. That match is a pattern
rather than an English sentence, and a test holds the three files that carry it
to the same one.
"""

import glob
import json
import os
import re
import select
import socket
import struct
import subprocess
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

# How long to let Steam paint before looking at the screen. A cold Steam is
# still unpacking its UI process when the window is mapped, and a black screen
# is what it looks like either way until that finishes.
SETTLE = 4.0

# How long the window is left windowed between the two halves of an off-and-on.
# Long enough to be a configure of its own rather than one sway folds into the
# next, short enough not to read as a flicker.
OFF = 0.4

# How many times to insist. One off-and-on was enough every time this was
# reproduced; after three the black is somebody else's and the seat is better
# left alone than flashed at.
TRIES = 3

# What counts as painted. The surface nobody drew on is exactly black, and
# Steam's own darkest corners are not: measured at 28 out of 255 on the bottom
# edge of a Big Picture that fills the screen.
FAINT = 8

# How far outside the painted corner to look, so that a border or a rounded
# edge is not what answers.
MARGIN = 20

# How long to keep looking at a freshly fullscreened Big Picture before leaving
# it alone. A cold Steam unpacks and updates itself first, and the same number
# is what polyseat-bigpicture waits for the window itself.
PATIENCE = 120.0


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


def look(x, y, size=12):
    """The brightest channel in a small square of the screen, or None.

    grim is in a seat because the desktop portal needs it, and it is asked for
    a dozen pixels rather than a screen so that this costs nothing worth
    measuring. A PPM because reading one needs no library: two lines of header
    and then the bytes.
    """
    try:
        shot = subprocess.run(
            ["grim", "-g", f"{x},{y} {size}x{size}", "-t", "ppm", "-"],
            capture_output=True, timeout=10).stdout
    except (OSError, subprocess.SubprocessError) as exc:
        log(f"could not look at the screen: {exc}")

        return None

    head = shot.split(b"\n", 3)

    if len(head) < 4 or head[0] != b"P6" or not head[3]:
        return None

    return max(head[3])


def unpainted(node, sample):
    """Whether this window holds the screen and has drawn on a corner of it.

    True when it has, False when the picture fills the screen, and None while
    there is nothing to judge yet, which on a cold Steam is the first half
    minute: the window is up and the UI process behind it is still unpacking.

    It has to be read off the screen, because nothing in sway's tree carries
    it. The window is the size sway gave it either way; what Steam painted into
    it is the size the window had when it was mapped. So the screen is asked
    directly, at one point inside the corner Steam would have drawn in and
    three outside it, and only the exact black of a surface nobody painted
    counts as outside.

    sample answers with the brightest channel at a point, and is an argument so
    that a test can describe a picture nobody has to render.
    """
    if not node.get("fullscreen_mode"):
        return False

    rect = node.get("rect") or {}
    drawn = node.get("geometry") or {}

    width, height = rect.get("width") or 0, rect.get("height") or 0
    small, short = drawn.get("width") or 0, drawn.get("height") or 0

    if not (0 < small < width and 0 < short < height):
        # The window was mapped at the size of the screen, so there is no
        # smaller picture it could be stuck at.
        return False

    x, y = rect.get("x") or 0, rect.get("y") or 0

    inside = sample(x + small // 2, y + short // 2)
    if inside is None or inside <= FAINT:
        return None

    outside = [sample(x + small + MARGIN, y + short // 2),
               sample(x + small // 2, y + short + MARGIN),
               sample(x + width - MARGIN, y + height - MARGIN)]

    return all(value == 0 for value in outside)


def holding(event):
    """The id of a Big Picture window that is holding the screen now, or None.

    Every event carries the whole window, so this needs no tree: whatever just
    happened, if Big Picture is fullscreen it is worth looking at what it has
    painted there.
    """
    container = event.get("container") or {}

    if event.get("change") == "close" or not is_big_picture(container):
        return None

    return container.get("id") if container.get("fullscreen_mode") else None


def step(path, plan):
    """Do what the plan says is due, and answer with what is due next, or None
    when there is nothing left to do about that window."""
    ident, now = plan["id"], time.monotonic()

    if plan["step"] == "on":
        order(path, f"[con_id={ident}] fullscreen enable")

        return dict(plan, step="look", at=now + SETTLE, tries=plan["tries"] + 1)

    tree = talk(path, GET_TREE)
    if tree is None:
        return None

    node = window(tree, ident)
    if node is None:
        return None

    verdict = unpainted(node, look)

    if verdict is None:
        # Nothing on the screen to judge yet. On a cold Steam that is the
        # ordinary first half minute, so it is worth waiting out.
        if now > plan["until"]:
            return None

        return dict(plan, at=now + SETTLE)

    if not verdict:
        if plan["tries"]:
            log(f"window {ident} fills the screen after "
                f"{plan['tries']} off and on")

        return None

    if plan["tries"] >= TRIES:
        log(f"window {ident} still paints a corner of the screen after "
            f"{TRIES} tries, leaving it as it is")

        return None

    order(path, f"[con_id={ident}] fullscreen disable")

    return dict(plan, step="on", at=now + OFF)


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

    state, seen, looked, plan = {}, set(), set(), None

    while True:
        wait = None if plan is None else max(0.0, plan["at"] - time.monotonic())

        try:
            ready, _, _ = select.select([events], [], [], wait)
        except OSError:
            return 0

        if not ready:
            ident = plan["id"]
            plan = step(path, plan)

            if plan is None:
                # Whatever it found, that window has been dealt with. Steam
                # renames its window while it runs, and looking again at every
                # rename would mean a screenshot a minute forever.
                looked.add(ident)

            continue

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

        if event.get("change") == "close":
            looked.discard((event.get("container") or {}).get("id"))

        ident = holding(event)

        if (ident is not None and ident not in looked
                and (plan is None or plan["id"] != ident)):
            now = time.monotonic()
            plan = {"id": ident, "at": now + SETTLE, "until": now + PATIENCE,
                    "step": "look", "tries": 0}


if __name__ == "__main__":
    sys.exit(main())
