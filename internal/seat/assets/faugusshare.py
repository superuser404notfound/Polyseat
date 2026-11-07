#!/usr/bin/env python3
"""polyseat-faugusshare - read and write one member's Faugus for the pool.

Runs as the person whose Faugus it is: in a seat as the player, on the host as
the owner of the library. Never as root, for the reason lutrisshare.py gives.

Faugus keeps every game as one entry in games.json: a title, an id made from
it, the program to start, the prefix to start it in and the Proton to start it
with. The pool moves the game's directory and not that entry, so this is the
other half, the same four commands lutrisshare.py has:

    scan GAMES_ROOT SHARED       every game, with its paths
    register SHARED DOC          add an entry for SHARED/<folder>
    unregister SHARED FOLDER GAMEID
                                 take back an entry register made
    move GAMES_ROOT SHARED GAMEID
                                 host only: move a game's folder into SHARED
                                 and point its entries at the new place
    repoint OLD NEW              point every entry into OLD at NEW instead,
                                 after another launcher moved the folder

What travels is a short list of keys, see the recipe in describe. Faugus also
runs commands before and after a game and puts launch arguments in front of
it, and none of that is carried: the entry written here is one somebody else
will start.
"""

import json
import os
import re
import shutil
import subprocess
import sys
import tempfile

HOME = os.path.expanduser("~")
DATA = os.path.join(os.environ.get("XDG_DATA_HOME") or os.path.join(HOME, ".local/share"),
                    "faugus-launcher")
CONFIG = os.path.join(os.environ.get("XDG_CONFIG_HOME") or os.path.join(HOME, ".config"),
                      "faugus-launcher", "config.json")
GAMES = os.path.join(DATA, "games.json")
ICONS = os.path.join(DATA, "icons")

# What register names its own entries by, so that a scan can tell an entry
# somebody made from one this made, and a recipe is only ever taken from the
# first kind.
SUFFIX = "-polyseat"

# The runners Faugus has a name for and downloads by itself.
LATEST = ("Proton-GE Latest", "Proton-CachyOS Latest", "DW-Proton Latest",
          "Proton-Wineland Latest")

# Where a seat's Proton builds are, by what Polyseat installs there.
SYSTEM_TOOLS = "/usr/share/steam/compatibilitytools.d"


def out(doc):
    json.dump(doc, sys.stdout)
    sys.stdout.write("\n")


def installed():
    return shutil.which("faugus-launcher") is not None


def load_games():
    """games.json as a list, or None when there is none. One that does not
    parse is an error rather than an empty list: register writes the file
    whole, and writing it over one it could not read would lose every game."""
    try:
        with open(GAMES, encoding="utf-8") as f:
            data = json.load(f)
    except FileNotFoundError:
        return None
    if not isinstance(data, list):
        raise ValueError("%s is not a list of games" % GAMES)
    return [g for g in data if isinstance(g, dict)]


def save_games(games):
    os.makedirs(DATA, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=DATA, prefix=".games.json.")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(games, f, indent=4, ensure_ascii=False)
        os.chmod(tmp, 0o600)
        os.replace(tmp, GAMES)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


def config():
    try:
        with open(CONFIG, encoding="utf-8") as f:
            data = json.load(f)
    except (OSError, ValueError):
        return {}
    return data if isinstance(data, dict) else {}


def text(game, key):
    value = game.get(key)
    return value if isinstance(value, str) else ""


def absolute(path):
    if not path:
        return ""
    path = os.path.expandvars(os.path.expanduser(path))
    return os.path.normpath(path) if os.path.isabs(path) else ""


def inside(path, root):
    """The first component of path below root, or empty when it is not below
    it. Compared as written, not resolved, as lutrisshare.py does."""
    if not path or not root:
        return ""
    rel = os.path.relpath(path, root)
    if rel == "." or rel == ".." or rel.startswith(".." + os.sep):
        return ""
    return rel.split(os.sep, 1)[0]


def relative(path, folder):
    if not path:
        return ""
    rel = os.path.relpath(path, folder)
    if rel == ".." or rel.startswith(".." + os.sep):
        return ""
    return rel


def busy():
    """What of Faugus is running as this person, said the way a refusal
    names it, or empty when nothing is.

    Faugus holds games.json in memory and writes all of it back when it
    saves, the runner every few seconds while a game plays, so an entry added
    while it runs is gone the next time it does. The runner is named by the
    game it runs, because that is all there is to see of it: a launcher like
    the EA App keeps one alive from the tray with no Faugus window anywhere,
    and "Faugus is open" sent people looking for a window to close."""
    me = os.getuid()
    for pid in os.listdir("/proc"):
        if not pid.isdigit():
            continue
        try:
            if os.stat("/proc/" + pid).st_uid != me:
                continue
            with open("/proc/%s/cmdline" % pid, "rb") as f:
                argv = f.read().split(b"\0")
        except OSError:
            continue
        for i, word in enumerate(argv[:-1]):
            if word != b"-m" or not argv[i + 1].startswith(b"faugus."):
                continue
            module = argv[i + 1]
            if module == b"faugus.backup_daemon":
                continue
            if module == b"faugus.runner":
                rest = argv[i + 2:]
                for j, arg in enumerate(rest[:-1]):
                    if arg == b"--game" and rest[j + 1]:
                        return "Faugus is running " + game_title(
                            rest[j + 1].decode("utf-8", "replace"))
                return "Faugus is starting a game"
            return "Faugus is open"
    return ""


def game_title(gameid):
    """The title Faugus shows for a game, or its id when there is none."""
    try:
        for game in load_games() or []:
            if text(game, "gameid") == gameid and text(game, "title"):
                return text(game, "title")
    except (OSError, ValueError):
        pass
    return gameid


def describe(game, games_root, shared, default_runner):
    gameid = text(game, "gameid")
    exe = absolute(text(game, "path"))
    prefix = absolute(text(game, "prefix"))

    runner = text(game, "runner")
    if runner == "Default":
        runner = default_runner

    item = {
        "gameid": gameid,
        "title": text(game, "title"),
        "ours": gameid.endswith(SUFFIX),
        "folder": inside(exe, shared),
        "home_folder": inside(exe, games_root),
    }

    if item["folder"]:
        base = os.path.join(shared, item["folder"])
        item["exe"] = relative(exe, base)
        # A prefix outside the folder is not carried: it is this member's, and
        # where the saves are. The receiver makes its own in its own place.
        item["prefix"] = relative(prefix, base)
        item["runner"] = runner
        item["protonfix"] = text(game, "protonfix")
        item["game_arguments"] = text(game, "game_arguments")
        item["runtime"] = text(game, "runtime")
        item["disable_umu"] = bool(game.get("disable_umu"))
    return item


def scan(games_root, shared):
    # Installed is enough: unlike Lutris, a Faugus that has never been opened
    # starts a game from a games.json written for it, and a new seat should
    # have the games before anybody has opened Faugus there.
    if not installed():
        out({"present": False, "games": []})
        return
    games = load_games() or []
    default_runner = text(config(), "default-runner").strip('"')
    out({"present": True,
         "games": [describe(g, games_root, shared, default_runner) for g in games]})


# ------------------------------------------------------------------ register


def family(name):
    lower = os.path.basename(name.rstrip("/")).lower()
    if "cachyos" in lower:
        return "cachyos"
    if "ge-proton" in lower or "proton-ge" in lower or lower.startswith("ge-"):
        return "ge"
    if "wineland" in lower:
        return "wineland"
    if lower.startswith("dw-proton"):
        return "dw"
    if lower.startswith("umu-proton") or lower.startswith("proton"):
        return "proton"
    return ""


def tools():
    """Every Proton this member's Faugus can be pointed at: the ones in Steam's
    own directory by name, which is how Faugus names them, and the ones a seat
    has under /usr/share by path, which Faugus takes as well."""
    found = []
    user = os.path.join(HOME, ".local/share/Steam/compatibilitytools.d")
    for root, by_path in ((user, False), (SYSTEM_TOOLS, True)):
        try:
            names = sorted(os.listdir(root))
        except OSError:
            continue
        for name in names:
            path = os.path.join(root, name)
            if os.path.isfile(os.path.join(path, "proton")):
                found.append(path if by_path else name)
    return found


def pick_runner(wanted, available):
    """The runner to name in the new entry.

    The same one where this member has it. Otherwise a build of the same kind
    that is already here, because Faugus's "Latest" names download a Proton of
    their own on the first start, half a gigabyte in every seat that already
    has one. A "Latest" name when nothing of its kind is here, since Faugus can
    fetch that. And "Default" for anything else, which leaves the choice to
    this member's own setting."""
    if wanted in ("", "Default"):
        return "Default"
    if wanted == "Linux-Native":
        return wanted
    if wanted in available:
        return wanted
    if wanted == "Proton-CachyOS (System)" and os.path.isdir(
            os.path.join(SYSTEM_TOOLS, "proton-cachyos-slr")):
        return wanted
    kind = family(wanted)
    if kind:
        for name in available:
            if family(name) == kind:
                return name
    if wanted in LATEST:
        return wanted
    if kind == "cachyos":
        return "Proton-CachyOS Latest"
    if kind == "ge":
        return "Proton-GE Latest"
    return "Default"


def covers(game, folder_dir):
    """Whether one existing entry already starts something in the folder."""
    for key in ("path", "prefix"):
        path = absolute(text(game, key))
        if path and (path == folder_dir or path.startswith(folder_dir + os.sep)):
            return True
    return False


def default_prefix():
    value = absolute(text(config(), "default-prefix"))
    return value or os.path.join(HOME, "Faugus")


def extract_icon(exe, gameid):
    """The program's own icon, the way Faugus takes it when a game is added.
    Without one the game has no picture in Faugus, the grid or Moonlight."""
    if not shutil.which("icoextract"):
        return ""
    target = os.path.join(ICONS, gameid + ".png")
    tmp = tempfile.mkdtemp()
    try:
        ico = os.path.join(tmp, "icon.ico")
        subprocess.run(["icoextract", exe, ico], capture_output=True, timeout=30, check=True)
        from PIL import Image
        with Image.open(ico) as icon:
            sizes = sorted(set(icon.info.get("sizes", [icon.size])), reverse=True)
            if sizes:
                icon.size = sizes[0]
            frame = icon.convert("RGBA").resize((256, 256))
        os.makedirs(ICONS, exist_ok=True)
        frame.save(target, "PNG")
        return target
    except Exception:
        return ""
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def entry(recipe, base, gameid):
    """A whole entry with every key Faugus writes, so that nothing it reads
    has to fall back on a default it does not have."""
    exe = os.path.normpath(os.path.join(base, recipe["exe"]))
    rel = recipe.get("prefix") or ""
    prefix = (os.path.normpath(os.path.join(base, rel)) if rel
              else os.path.join(default_prefix(), gameid))
    return {
        "gameid": gameid,
        "title": recipe["title"],
        "path": exe,
        "prefix": prefix,
        "launch_arguments": "",
        "game_arguments": recipe.get("game_arguments") or "",
        "mangohud": "",
        "gamemode": "",
        "sdl_enabled": "",
        "protonfix": recipe.get("protonfix") or "",
        "runner": pick_runner(recipe.get("runner") or "", tools()),
        "addapp_enabled": "",
        "addapp": "",
        "addapp_bat": "",
        "addapp_delay": "",
        "addapp_first": False,
        "cover": "",
        "lossless_enabled": False,
        "lossless_multiplier": 1,
        "lossless_flow": 100,
        "lossless_performance": False,
        "lossless_hdr": False,
        "lossless_present": False,
        "playtime": 0,
        "hidden": False,
        "no_sleep": "",
        "category": False,
        "icon": extract_icon(exe, gameid),
        "steamgriddb_id": "",
        "pre_launch": "",
        "post_launch": "",
        "steam_user": "",
        "disable_umu": "disable_umu" if recipe.get("disable_umu") else "",
        "runtime": recipe.get("runtime") or "",
        "last_played": "",
    }


def register(shared, doc):
    recipe, folder = doc["recipe"], doc["folder"]
    base = os.path.join(shared, folder)

    if not os.path.isdir(base):
        out({"status": "missing"})
        return

    if not installed():
        out({"status": "absent"})
        return

    if busy():
        out({"status": "busy"})
        return

    games = load_games() or []
    gameid = recipe["gameid"] + SUFFIX

    for game in games:
        if covers(game, base):
            out({"status": "present", "written": text(game, "gameid")})
            return
        if text(game, "gameid") == gameid:
            out({"status": "taken", "written": gameid})
            return

    new = entry(recipe, base, gameid)
    games.append(new)
    save_games(games)
    out({"status": "added", "written": gameid, "runner": new["runner"]})


def unregister(shared, folder, gameid):
    """Removes the entry register made for a folder that is gone, and only that
    one: it has to carry both the id register gave it and a path into the
    folder it was written for, so an entry somebody made is never touched."""
    base = os.path.join(shared, folder)
    if not gameid.endswith(SUFFIX) or "/" in gameid:
        out({"status": "refused"})
        return
    games = load_games() if installed() else None
    if games is None:
        out({"status": "gone"})
        return
    who = busy()
    if who:
        out({"error": "%s, so its list cannot be changed under it" % who})
        return
    kept = [g for g in games if not (text(g, "gameid") == gameid and covers(g, base))]
    if len(kept) == len(games):
        out({"status": "gone"})
        return
    save_games(kept)
    icon = os.path.join(ICONS, gameid + ".png")
    try:
        os.unlink(icon)
    except OSError:
        pass
    out({"status": "removed"})


# ---------------------------------------------------------------------- move


def rewrite(value, old, new):
    if isinstance(value, str):
        if value == old or value.startswith(old + os.sep):
            return new + value[len(old):]
        return value
    if isinstance(value, list):
        return [rewrite(v, old, new) for v in value]
    if isinstance(value, dict):
        return {k: rewrite(v, old, new) for k, v in value.items()}
    return value


def shortcuts():
    """The desktop entries Faugus writes for its games, which name the game's
    directory as the one to start in."""
    dirs = [os.path.join(HOME, ".local/share/applications")]
    try:
        with open(os.path.join(HOME, ".config/user-dirs.dirs"), encoding="utf-8") as f:
            m = re.search(r'^XDG_DESKTOP_DIR="([^"]*)"', f.read(), re.M)
        if m:
            dirs.append(m.group(1).replace("$HOME", HOME))
    except OSError:
        dirs.append(os.path.join(HOME, "Desktop"))
    for d in dirs:
        try:
            names = os.listdir(d)
        except OSError:
            continue
        for name in names:
            path = os.path.join(d, name)
            if name.endswith(".desktop") and os.path.isfile(path) and not os.path.islink(path):
                yield path


def repoint_shortcuts(old, new):
    for path in shortcuts():
        try:
            with open(path, encoding="utf-8") as f:
                lines = f.readlines()
        except (OSError, UnicodeDecodeError):
            continue
        if not any("faugus-launcher" in line for line in lines if line.startswith("Exec=")):
            continue
        changed = []
        for line in lines:
            key, sep, value = line.partition("=")
            if sep and key in ("Path", "Icon"):
                value = rewrite(value.rstrip("\n"), old, new) + "\n"
                line = key + "=" + value
            changed.append(line)
        if changed != lines:
            tmp = path + ".polyseat"
            with open(tmp, "w", encoding="utf-8") as f:
                f.writelines(changed)
            os.replace(tmp, path)


def repoint_games(old, new):
    games = load_games()
    if not games:
        return []
    changed = rewrite(games, old, new)
    moved = [text(n, "gameid") for g, n in zip(games, changed) if g != n]
    if moved:
        save_games(changed)
    return moved


def move(games_root, shared, gameid):
    who = busy()
    if who:
        out({"error": "%s here; close it first, it would write the old place back" % who})
        return
    games = load_games() or []
    target = None
    for game in games:
        if text(game, "gameid") == gameid:
            target = describe(game, games_root, shared, "")
            break
    if target is None:
        out({"error": "Faugus has no game called %s" % gameid})
        return
    name = target["home_folder"]
    if target["folder"] or not name or name == os.path.basename(shared):
        out({"error": "%s is not in a folder of its own below %s" % (gameid, games_root)})
        return

    old = os.path.join(games_root, name)
    new = os.path.join(shared, name)
    if os.path.islink(old) or not os.path.isdir(old):
        out({"error": "%s is not a plain directory" % old})
        return
    if os.path.lexists(new):
        out({"error": "%s already exists" % new})
        return

    # A rename, never a copy, for the reason lutrisshare.py gives.
    os.rename(old, new)
    moved = repoint_games(old, new)
    repoint_shortcuts(old, new)
    out({"status": "moved", "folder": name, "entries": moved, "old": old, "new": new})


def repoint(old, new):
    if not os.path.isabs(old) or not os.path.isabs(new):
        out({"error": "repoint wants two absolute paths"})
        return
    if load_games() is None:
        out({"status": "gone"})
        return
    who = busy()
    if who:
        out({"error": "%s, so its list cannot be changed under it" % who})
        return
    moved = repoint_games(os.path.normpath(old), os.path.normpath(new))
    repoint_shortcuts(os.path.normpath(old), os.path.normpath(new))
    out({"status": "repointed" if moved else "gone"})


def main(argv):
    try:
        if argv[:1] == ["scan"] and len(argv) == 3:
            scan(argv[1], argv[2])
        elif argv[:1] == ["register"] and len(argv) == 3:
            register(argv[1], json.loads(argv[2]))
        elif argv[:1] == ["unregister"] and len(argv) == 4:
            unregister(argv[1], argv[2], argv[3])
        elif argv[:1] == ["move"] and len(argv) == 4:
            move(argv[1], argv[2], argv[3])
        elif argv[:1] == ["repoint"] and len(argv) == 3:
            repoint(argv[1], argv[2])
        else:
            out({"error": "usage: scan|register|unregister|move|repoint ..."})
            return 2
    except (OSError, ValueError, KeyError, TypeError) as e:
        out({"error": "%s: %s" % (type(e).__name__, e)})
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
