#!/usr/bin/env python3
"""polyseat-boxart - turn whatever artwork a seat has into cards Moonlight shows.

Reads a JSON list of {"key", "source", "label"} on the command line and prints
{"key": "/path/to/card.png"} for the ones it could make.

Two things were learned by looking at a client rather than by reading a
specification, and both of them are why this exists at all.

**Sunshine only shows PNG.** Steam caches its covers as JPEG, so pointing an
app entry straight at one produced a card with no picture on it, which looks
exactly like an entry that was never given artwork.

**Everything is drawn as a portrait card**, roughly two by three. A launcher's
icon is square, and stretched into that shape it fills the card edge to edge,
which also hides the name: Moonlight draws the title only when there is nothing
else to draw. So a Lutris entry became an enormous otter with no word on it.

Hence a card is composed rather than passed through. A real cover is already the
right shape and only wants converting. An icon is placed on a dark card with the
name under it, which is what the cards Sunshine ships for Desktop and Steam look
like, so the result sits in the same row without standing out.
"""

import hashlib
import json
import os
import sys

from PIL import Image, ImageDraw, ImageFont

WIDTH, HEIGHT = 600, 900
BACKGROUND = (27, 36, 48)
TEXT = (216, 222, 233)

OUT = os.path.expanduser("~/.local/share/polyseat/art")

FONTS = [
    "/usr/share/fonts/noto/NotoSans-Bold.ttf",
    "/usr/share/fonts/TTF/DejaVuSans-Bold.ttf",
    "/usr/share/fonts/liberation/LiberationSans-Bold.ttf",
    "/usr/share/fonts/Adwaita/AdwaitaSans-Regular.ttf",
]


def font(size):
    for path in FONTS:
        if os.path.exists(path):
            try:
                return ImageFont.truetype(path, size)
            except OSError:
                continue

    # Better an unreadably small name than no card at all.
    return ImageFont.load_default()


def wrap(draw, text, face, width):
    lines, line = [], ""

    for word in text.split():
        candidate = (line + " " + word).strip()
        if draw.textlength(candidate, font=face) <= width or not line:
            line = candidate
        else:
            lines.append(line)
            line = word

    if line:
        lines.append(line)

    return lines[:4]


def is_cover(image):
    """A picture already shaped like a card, rather than an icon.

    Steam's covers are 2:3 and so are Lutris's. Anything squarer than 4:5 is
    treated as an icon and given a card of its own, which is the case that was
    getting stretched.
    """
    w, h = image.size

    return h > w and (w / h) < 0.8


def compose(source, label):
    card = Image.new("RGB", (WIDTH, HEIGHT), BACKGROUND)
    draw = ImageDraw.Draw(card)

    if source is not None:
        icon = source.convert("RGBA")

        # Large enough to read across a room, with room left for the name.
        #
        # Scaled rather than thumbnailed, because thumbnail only ever shrinks
        # and most of these are 128 pixels: the first version left a stamp in
        # the middle of a card the size of a television.
        box = 380
        scale = min(box / icon.width, box / icon.height)
        icon = icon.resize(
            (max(1, round(icon.width * scale)), max(1, round(icon.height * scale))),
            Image.LANCZOS,
        )

        card.paste(icon, ((WIDTH - icon.width) // 2, 260 - icon.height // 2), icon)

    face = font(52)
    lines = wrap(draw, label, face, WIDTH - 80)

    y = 520
    for line in lines:
        length = draw.textlength(line, font=face)
        draw.text(((WIDTH - length) / 2, y), line, font=face, fill=TEXT)
        y += 64

    return card


def build(item):
    key = item.get("key") or ""
    label = item.get("label") or key
    source = item.get("source") or ""

    if not key:
        return None

    target = os.path.join(OUT, hashlib.sha1(key.encode()).hexdigest() + ".png")

    # Rebuilt only when the source is newer, because this runs on a timer and
    # redrawing every card every minute would be work for nothing.
    try:
        if os.path.exists(target):
            if not source or os.path.getmtime(target) >= os.path.getmtime(source):
                return target
    except OSError:
        pass

    image = None

    if source:
        try:
            image = Image.open(source)
            image.load()
        except Exception:
            image = None

    if image is None and not label:
        return None

    if image is not None and is_cover(image):
        card = image.convert("RGB").resize((WIDTH, HEIGHT), Image.LANCZOS)
    else:
        card = compose(image, label)

    os.makedirs(OUT, exist_ok=True)

    tmp = target + ".tmp"
    card.save(tmp, "PNG")
    os.replace(tmp, target)

    return target


def main():
    if len(sys.argv) < 2:
        print("{}")
        return

    out = {}

    for item in json.loads(sys.argv[1]):
        try:
            path = build(item)
        except Exception:
            # One unreadable image must not cost a seat its whole app list.
            path = None

        if path:
            out[item.get("key")] = path

    print(json.dumps(out))


if __name__ == "__main__":
    main()
