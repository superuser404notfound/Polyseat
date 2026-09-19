#!/usr/bin/env python3
"""polyseat-icons - the icon a game wears in the seat's own launcher.

Reads a JSON list of {"key", "steam", "lutris", "icon"} on the command line and
prints {"key": "/path/to/icon.png"} for the ones it could resolve. "steam" is an
application id, "lutris" the slug Lutris knows a game by, and "icon" a file that
is already an icon and only has to be confirmed, which is what an AppImage
arrives with.

**This is not the box art, and that is the whole point.** The app list Moonlight
shows and the grid inside the seat are two menus drawn to different rules. A
client draws every entry as a portrait card, so a cover is exactly right there.
The grid draws every entry as a square icon of a fixed size, so the same cover
arrives as a tall sliver between the square icons of Firefox and Steam, and the
one thing on the screen that looks out of place is the games. Worse, it looked
inconsistent with itself: a game somebody had asked Steam for a shortcut for
already had a proper icon in that grid, because that entry is Steam's and not
ours, so half the games wore icons and half wore covers.

So the grid gets the same picture Steam's own shortcut would have used. Steam
keeps the address of it in appinfo.vdf as `clienticon`, a hash under which the
icon is published as a Windows .ico of up to 256 pixels, or as `linuxclienticon`
for the minority of titles that publish a zip of PNGs instead. Neither is in any
manifest and neither is reachable without reading that file, which is why there
is a parser for a binary format in here.

Everything is best effort and nothing raises: an icon is not worth failing a
seat's app list over, and a game that resolves to nothing keeps the cover it had.
"""

import io
import json
import os
import struct
import sys
import time
import urllib.error
import urllib.request
import zipfile

from PIL import Image

HOME = os.path.expanduser("~")

OUT = os.path.join(HOME, ".local/share/polyseat/icons")

STEAM = os.path.join(HOME, ".local/share/Steam")
APPINFO = os.path.join(STEAM, "appcache/appinfo.vdf")
LIBRARY = os.path.join(STEAM, "appcache/librarycache")

# Where Steam publishes the icon of a title, by the hash appinfo.vdf carries.
# The extension is part of what the hash means: a clienticon is an .ico there
# and nothing else, a linuxclienticon is a .zip there and nothing else, and
# asking for the wrong one is a 404 rather than a conversion.
ICON_URL = ("https://cdn.cloudflare.steamstatic.com/steamcommunity/public"
            "/images/apps/%s/%s.%s")

FETCH_TIMEOUT = 8

# At most this many downloads per pass. The list is rebuilt every minute, so a
# seat that has just been handed forty games is not asked to fetch forty icons
# before it may draw the first one: it draws what it has and is better a minute
# later. Matches polyseat-boxart, which shares the timer.
FETCH_BUDGET = 6

# How long a title whose icon could not be had is left alone. An icon can appear
# later, so never is wrong, and every minute is a request a minute for the life
# of the seat.
RETRY_MISSING = 7 * 24 * 3600

# Nothing in the grid is drawn larger than this, so nothing is kept larger.
MAX = 256

# Icon themes in the order a desktop would search them, largest size first: this
# is scaled up for a television rather than drawn at sixteen pixels in a menu.
ICON_ROOTS = [
    os.path.join(HOME, ".local/share/icons"),
    "/usr/local/share/icons",
    "/usr/share/icons",
]

ICON_SIZES = ["512x512", "384x384", "256x256", "192x192", "128x128", "96x96",
              "64x64", "48x48", "32x32"]


def get(url):
    """One request, or None. Never raises."""
    try:
        with urllib.request.urlopen(url, timeout=FETCH_TIMEOUT) as response:
            return response.read()
    except (urllib.error.URLError, OSError, ValueError):
        return None


def themed(name):
    """The file an icon name means, largest first, or "" for none."""
    for root in ICON_ROOTS:
        for size in ICON_SIZES:
            path = os.path.join(root, "hicolor", size, "apps", name + ".png")
            if os.path.exists(path):
                return path

    return ""


def vdf_apps(data, wanted):
    """The `common` section of each wanted app in an appinfo.vdf.

    The format is Valve's binary key values with an app index in front of it:
    a magic and a universe, then, since the version that introduced it, the
    offset of a table of every key name in the file, and then one record per
    app. A record says how long it is before it says anything else, which is
    what makes it cheap to read a few apps out of a file describing six
    hundred: the ones not asked for are stepped over rather than parsed.

    Returns {appid: {...}} and nothing else is promised. A file in a version
    this does not know, or a record that does not parse, is an empty answer.
    """
    magic, _universe = struct.unpack_from("<II", data, 0)

    # 0x07564427 through 0x07564429 are the three versions seen in the wild:
    # the second added a checksum to every record, the third moved key names
    # into a table and left an index in their place.
    if magic not in (0x07564427, 0x07564428, 0x07564429):
        return {}

    off = 8
    table = None

    if magic >= 0x07564429:
        (start,) = struct.unpack_from("<q", data, off)
        off += 8

        (count,) = struct.unpack_from("<I", data, start)
        start += 4

        table = []

        for _ in range(count):
            end = data.index(b"\0", start)
            table.append(data[start:end].decode("utf-8", "replace"))
            start = end + 1

    # Everything in a record before the tree itself, none of which is wanted
    # here: the state, the timestamps, the access token, the text checksum, the
    # change number, and, from the second version on, a checksum of the tree.
    header = 4 + 4 + 8 + 20 + 4 + (20 if magic >= 0x07564428 else 0)

    out = {}

    while True:
        (appid,) = struct.unpack_from("<I", data, off)
        off += 4

        if appid == 0:
            break

        (size,) = struct.unpack_from("<I", data, off)
        off += 4

        if str(appid) in wanted:
            try:
                out[str(appid)] = vdf_tree(data, off + header, off + size, table)
            except (ValueError, IndexError, struct.error, UnicodeError):
                pass

        off += size

    return out


def vdf_tree(data, pos, end, table):
    """One binary key value tree, as nested dictionaries.

    Types are a byte in front of every entry: a subtree, a string, and the
    numbers. Only the three that appear in the part of appinfo this reads are
    turned into values; the rest are stepped over at their fixed width so that
    an unexpected one does not lose the entries after it.
    """
    widths = {2: 4, 3: 4, 4: 4, 6: 4, 7: 8, 10: 8}

    def key(pos):
        if table is not None:
            (index,) = struct.unpack_from("<I", data, pos)

            return table[index], pos + 4

        stop = data.index(b"\0", pos)

        return data[pos:stop].decode("utf-8", "replace"), stop + 1

    def node(pos):
        out = {}

        while pos < end:
            kind = data[pos]
            pos += 1

            if kind == 8:  # the end of this subtree
                return out, pos

            name, pos = key(pos)

            if kind == 0:
                out[name], pos = node(pos)
            elif kind == 1:
                stop = data.index(b"\0", pos)
                out[name] = data[pos:stop].decode("utf-8", "replace")
                pos = stop + 1
            elif kind == 5:  # a wide string, which nothing here wants
                stop = data.index(b"\0\0", pos)
                pos = stop + 2
            elif kind in widths:
                pos += widths[kind]
            else:
                raise ValueError("unknown value type %d" % kind)

        return out, pos

    tree, _ = node(pos)

    # The tree under a record is one subtree named after the app, so the part
    # every caller wants is one level in. Written to survive either shape,
    # since it is a file somebody else writes.
    if len(tree) == 1:
        inner = next(iter(tree.values()))

        if isinstance(inner, dict):
            return inner

    return tree


class AppInfo:
    """What appinfo.vdf says about the apps this run asks about.

    Read once and only if something is missing an icon. It is a megabyte and a
    half and this runs on a minute timer, so the file is not opened at all for
    a seat whose icons are all in hand, which after the first pass is every
    seat.
    """

    def __init__(self, wanted):
        self.wanted = wanted
        self.common = None

    def of(self, appid):
        if self.common is None:
            self.common = {}

            try:
                with open(APPINFO, "rb") as fh:
                    apps = vdf_apps(fh.read(), self.wanted)
            except (OSError, ValueError, IndexError, struct.error):
                apps = {}

            for key, app in apps.items():
                section = app.get("common")
                self.common[key] = section if isinstance(section, dict) else {}

        return self.common.get(appid, {})


def largest(data, name):
    """The biggest picture in a downloaded icon, as an image, or None.

    An .ico is several sizes in one file and Pillow opens it at one of them; a
    linuxclienticon is a zip of PNGs and is opened at all of them. Both are
    asked for the largest, because this is scaled up rather than down.
    """
    try:
        if name.endswith(".zip"):
            best = None

            with zipfile.ZipFile(io.BytesIO(data)) as archive:
                for member in archive.namelist():
                    try:
                        with archive.open(member) as fh:
                            image = Image.open(io.BytesIO(fh.read()))
                            image.load()
                    except Exception:
                        continue

                    if best is None or image.width > best.width:
                        best = image

            return best

        image = Image.open(io.BytesIO(data))

        sizes = sorted(image.ico.sizes())

        if sizes:
            return image.ico.getimage(sizes[-1])

        image.load()

        return image
    except Exception:
        return None


def store(image, target):
    """Write an icon where the launcher will read it. Returns the path or ""."""
    try:
        icon = image.convert("RGBA")

        if icon.width > MAX or icon.height > MAX:
            scale = min(MAX / icon.width, MAX / icon.height)
            icon = icon.resize(
                (max(1, round(icon.width * scale)), max(1, round(icon.height * scale))),
                Image.LANCZOS,
            )

        os.makedirs(OUT, exist_ok=True)

        tmp = target + ".tmp"

        icon.save(tmp, "PNG")
        os.replace(tmp, target)

        return target
    except Exception:
        try:
            os.unlink(target + ".tmp")
        except OSError:
            pass

        return ""


def missed(marker):
    """Whether a lookup that failed recently should be left alone."""
    try:
        return time.time() - os.path.getmtime(marker) < RETRY_MISSING
    except OSError:
        return False


def remember_miss(marker):
    try:
        os.makedirs(OUT, exist_ok=True)
        open(marker, "wb").close()
    except OSError:
        pass


def cached_icon(appid, info):
    """The small icon Steam already downloaded for its own library list.

    Thirty two pixels, which is a poor thing to draw at ninety six, and it is
    still the game's own icon rather than a picture of something else. Only
    reached when the real one cannot be had: no network, no budget left this
    pass, or a title that publishes no icon at all.
    """
    name = info.get("icon") or ""

    if not name:
        return ""

    for ext in (".jpg", ".png"):
        source = os.path.join(LIBRARY, appid, name + ext)

        if not os.path.exists(source):
            continue

        target = os.path.join(OUT, "steam-%s-%s-small.png" % (appid, name))

        if os.path.exists(target):
            return target

        try:
            image = Image.open(source)
            image.load()
        except Exception:
            return ""

        return store(image, target)

    return ""


def steam_icon(appid, info, budget):
    """The icon Steam's own shortcut for a title would wear."""
    if not appid.isdigit():
        return ""

    # A title somebody has already asked Steam for a shortcut for. Steam wrote
    # the icon into the theme when it made that shortcut, so the file is there,
    # it is the right one, and it costs a stat to find.
    themed_icon = themed("steam_icon_" + appid)

    if themed_icon:
        return themed_icon

    common = info.of(appid)

    # The Linux icon first where there is one, because it is the one Steam
    # itself uses in this seat; the Windows one for everything else, which is
    # nearly everything.
    for key, ext in (("linuxclienticon", "zip"), ("clienticon", "ico")):
        name = common.get(key) or ""

        if not name:
            continue

        target = os.path.join(OUT, "steam-%s-%s.png" % (appid, name))

        if os.path.exists(target):
            return target

        if missed(target + ".none"):
            continue

        if budget["left"] <= 0:
            break

        budget["left"] -= 1

        data = get(ICON_URL % (appid, name, ext))
        image = largest(data, "x." + ext) if data else None

        if image is None:
            remember_miss(target + ".none")
            continue

        kept = store(image, target)

        if kept:
            return kept

    return cached_icon(appid, common)


def resolve(item, info, budget):
    given = item.get("icon") or ""

    if given and os.path.exists(given):
        return given

    appid = item.get("steam") or ""

    if appid:
        return steam_icon(appid, info, budget)

    # Lutris keeps an icon per game in the theme, named after the slug it knows
    # the game by. Nothing has to be fetched for one: if it is not there, the
    # game has none and keeps its cover.
    slug = item.get("lutris") or ""

    if slug:
        return themed("lutris_" + slug)

    return ""


def sweep(keep):
    """Remove icons nothing points at any more.

    Only inside this directory, and only the pictures: the misses beside them
    are the memory of a title that has none, and the icons found in a theme
    belong to whoever put them there.
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

    items = json.loads(sys.argv[1])

    info = AppInfo({str(item.get("steam")) for item in items if item.get("steam")})
    budget = {"left": FETCH_BUDGET}

    out = {}

    for item in items:
        key = item.get("key") or ""

        if not key:
            continue

        try:
            path = resolve(item, info, budget)
        except Exception:
            # One unreadable icon must not cost the seat every other one.
            path = ""

        if path:
            out[key] = path

    # Nothing at all means the scan came back empty rather than every icon
    # being gone: a seat where Steam is signed out lists no games for as long
    # as that lasts, and sweeping on that answer would throw away every icon in
    # the seat and fetch them all again afterwards. A short list is different
    # and is swept: a game that is really gone has really gone.
    if out:
        sweep(set(out.values()))

    print(json.dumps(out))


if __name__ == "__main__":
    main()
