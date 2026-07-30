#!/usr/bin/env python3
"""polyseat-boxart - turn whatever artwork a seat has into cards Moonlight shows.

Reads a JSON list of {"key", "source", "label", "steam", "fallback"} on the
command line and prints {"key": "/path/to/card.png"} for the ones it could
make. "source" is artwork already on disk, "steam" is an application id to
fetch a cover for when there is none, and "fallback" is an icon to use when
there is no cover to be had anywhere.

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
import time
import urllib.error
import urllib.parse
import urllib.request

from PIL import Image, ImageDraw, ImageFont

WIDTH, HEIGHT = 600, 900
BACKGROUND = (27, 36, 48)
TEXT = (216, 222, 233)

OUT = os.path.expanduser("~/.local/share/polyseat/art")

# Where Steam publishes the portrait cover of a title.
#
# Fetched because a seat only caches the artwork of games it has displayed in
# Steam, and the shared library delivers games nobody has looked at there. Their
# cards were coming out as a name on a dark background while the picture sat one
# request away.
#
# Not every title has one: a cover that does not exist answers 404, which is an
# answer and is remembered so that the same 404 is not fetched every minute for
# the life of the seat.
COVER_URL = ("https://shared.cloudflare.steamstatic.com/store_item_assets"
             "/steam/apps/%s/library_600x900.jpg")

# Where the same picture lives for a title that does not publish it under that
# name.
#
# Which is not rare, and a game with no card at all is what made this necessary:
# Assassin's Creed Black Flag Resynced answers 404 for every filename under
# .../apps/3751950/, because its assets sit one level down in a directory named
# after a hash that nothing in a manifest or on the store page contains.
#
# This service does contain it. It is public, it needs no key, and it answers
# with an asset_url_format like "steam/apps/3751950/${FILENAME}?t=..." plus the
# filename of each asset, library_capsule being the portrait cover: the 2x
# variant is exactly the 600x900 wanted here and the plain one is half that.
# The hash it gives back is the same one Steam itself had cached in a seat where
# the picture did show, which is how it was confirmed to be the right asset
# rather than merely an asset.
ITEMS_URL = ("https://api.steampowered.com/IStoreBrowseService/GetItems/v1/"
             "?input_json=%s")

ASSET_BASE = "https://shared.cloudflare.steamstatic.com/store_item_assets/"

FETCH_TIMEOUT = 8

# How long a title with no cover is left alone before asking again. Art does
# appear for a game after release, so never is the wrong answer, and so is
# every minute.
RETRY_MISSING = 7 * 24 * 3600

# At most this many downloads per pass, so that a seat that has just been given
# forty games does not spend a minute of every minute fetching pictures.
FETCH_BUDGET = 6

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


def get(url):
    """One request, or None. Never raises, for the reason below."""
    try:
        with urllib.request.urlopen(url, timeout=FETCH_TIMEOUT) as response:
            return response.read()
    except (urllib.error.URLError, OSError, ValueError):
        return None


def hashed_cover(appid):
    """The portrait cover of a title that publishes it under a hash.

    Returns the bytes, or None. Two requests at worst, and only reached for a
    title the plain address does not have, which is the minority.
    """
    query = json.dumps({
        "ids": [{"appid": int(appid)}],
        "context": {"language": "english", "country_code": "US"},
        "data_request": {"include_assets": True},
    }, separators=(",", ":"))

    answer = get(ITEMS_URL % urllib.parse.quote(query))
    if not answer:
        return None

    try:
        items = json.loads(answer)["response"]["store_items"]
        assets = items[0]["assets"]
        template = assets["asset_url_format"]
    except (ValueError, KeyError, IndexError, TypeError):
        return None

    # The 2x variant is the 600x900 this composes at; the plain one is 300x450
    # and is still a great deal better than a name on a dark background.
    for name in ("library_capsule_2x", "library_capsule"):
        if name not in assets:
            continue

        data = get(ASSET_BASE + template.replace("${FILENAME}", assets[name]))
        if data:
            return data

    return None


def fetched_cover(appid, budget):
    """The cover for a Steam title, downloaded once and kept.

    Returns the path, or None. Never raises: a picture is not worth failing an
    app list over.
    """
    if not appid or not appid.isdigit():
        return None

    target = os.path.join(OUT, "steam-%s.jpg" % appid)
    missing = target + ".none"

    if os.path.exists(target):
        return target

    try:
        if os.path.exists(missing) and time.time() - os.path.getmtime(missing) < RETRY_MISSING:
            return None
    except OSError:
        pass

    if budget["left"] <= 0:
        return None

    budget["left"] -= 1

    os.makedirs(OUT, exist_ok=True)

    data = get(COVER_URL % appid) or hashed_cover(appid)

    if not data:
        try:
            open(missing, "wb").close()
        except OSError:
            pass

        return None

    tmp = target + ".tmp"

    try:
        with open(tmp, "wb") as fh:
            fh.write(data)

        # Confirm it is really an image before letting it become a card, so
        # that an error page saved as a jpg does not become somebody's box art.
        Image.open(tmp).load()
        os.replace(tmp, target)
    except Exception:
        try:
            os.unlink(tmp)
        except OSError:
            pass

        return None

    return target


def card_name(key, source):
    """What a card is called, which has to change when the picture does.

    The name used to be the title alone, and that is how a game ended up
    wearing the Steam logo on a client while the right cover sat on disk next
    to it. Artwork arrives late: a game delivered by the shared library has
    none until Steam is opened and caches it, or until the fetch below finds
    it, and until then the card is the launcher's icon with a name under it.
    When the real cover then replaced it, the card was rewritten under the same
    name, so the app list Sunshine serves was unchanged, so nothing told it to
    reload, so Moonlight went on showing the picture it had cached. The file on
    disk was right and the client was wrong, which is the hardest kind of wrong
    to see.

    So the source goes into the name, identified the way a cache does it, by
    what it is and when it changed. A new picture is a new card is a new app
    list, and the client fetches it because it has never seen that path before.
    """
    parts = [key, source]

    try:
        info = os.stat(source)
        parts += [str(int(info.st_mtime)), str(info.st_size)]
    except OSError:
        pass

    return hashlib.sha1("\0".join(parts).encode()).hexdigest() + ".png"


def build(item, budget):
    key = item.get("key") or ""
    label = item.get("label") or key
    source = item.get("source") or ""

    if not key:
        return None

    if not source:
        source = fetched_cover(item.get("steam") or "", budget) or ""

    # Nothing published anywhere, which does happen: a title was found with no
    # artwork at all, every size answering 404 on both content networks. A card
    # with only a name tells you less than the row it sits in, where every
    # neighbour has a picture, so it wears its launcher's icon instead and ends
    # up looking like the launcher's own card rather than like a gap.
    if not source:
        source = item.get("fallback") or ""

    target = os.path.join(OUT, card_name(key, source))

    # Already drawn from exactly this source, which on a timer is almost every
    # time. The name carries the source with it, so there is nothing to compare.
    if os.path.exists(target):
        return target

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


def sweep(keep):
    """Remove cards nothing points at any more.

    Now that a card is named after the picture it was drawn from, every time a
    cover improves it leaves the previous card behind, and a seat that never
    tidied up would accumulate one per title per change for as long as it
    exists. Only the cards go: the downloaded covers next to them are the
    sources, and a "*.none" is a title known to have none.

    The list passed in is always the whole app list, so anything not in it is
    genuinely unreferenced, and anything wrongly removed is drawn again on the
    next pass.
    """
    try:
        names = os.listdir(OUT)
    except OSError:
        return

    for name in names:
        if not name.endswith(".png"):
            continue

        path = os.path.join(OUT, name)

        if path in keep:
            continue

        try:
            os.unlink(path)
        except OSError:
            pass


def main():
    if len(sys.argv) < 2:
        print("{}")
        return

    out = {}
    budget = {"left": FETCH_BUDGET}

    for item in json.loads(sys.argv[1]):
        try:
            path = build(item, budget)
        except Exception:
            # One unreadable image must not cost a seat its whole app list.
            path = None

        if path:
            out[item.get("key")] = path

    sweep(set(out.values()))

    print(json.dumps(out))


if __name__ == "__main__":
    main()
