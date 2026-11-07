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
    stores SHARED                make every store launcher's place for its
                                 games a link to SHARED, moving the games
                                 already there into it first

What travels is a short list of keys, see the recipe in describe. Faugus also
runs commands before and after a game and puts launch arguments in front of
it, and none of that is carried: the entry written here is one somebody else
will start.
"""

import errno
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time

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


def describe(game, games_root, shared, default_runner, prefixes=()):
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

    # A game a store launcher installed, started through the link that store's
    # directory is, see link_store. Its files are a shared folder; the prefix is
    # the store's, which is where it is signed in, and stays behind like any
    # other prefix outside the folder.
    found = store_of(exe, prefixes)
    if found:
        kind, root, folder, rel = found
        if same_place(os.path.join(root, folder), os.path.join(shared, folder)):
            item["folder"] = folder
            item["store"] = kind
            item["exe"] = rel
            item["prefix"] = ""
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
         "games": [describe(g, games_root, shared, default_runner, store_prefixes(games))
                   for g in games]})


# -------------------------------------------------------------------- stores

# The store launchers Faugus installs whose games each get a directory of
# their own in one place: the launcher's program and that place, both below
# drive_c. The place is made a link to the shared folders, so whatever the
# store installs is a shared folder from its first byte, on every member, and
# the store finds it where it put it. Epic and Rockstar keep the launcher
# itself among their games and Battle.net puts every game straight into
# Program Files, so none of those can be linked whole.
STORES = {
    "ea": ("Program Files/Electronic Arts/EA Desktop/EA Desktop/EALauncher.exe",
           "Program Files/EA Games"),
    "ubisoft": ("Program Files (x86)/Ubisoft/Ubisoft Game Launcher/UbisoftConnect.exe",
                "Program Files (x86)/Ubisoft/Ubisoft Game Launcher/games"),
}

# How long a directory in a store's place has to be left alone before it is
# moved into the shared folders. A store writes a game in place while it
# downloads, and a download can pause.
QUIET = 10 * 60


def store_prefixes(games):
    """(kind, prefix) for every prefix in games.json that has a store
    launcher installed, in the order of the list, each once. Read off the
    prefix rather than off the launcher's entry, which somebody may have
    renamed or removed."""
    seen, found = set(), []
    for game in games:
        prefix = absolute(text(game, "prefix"))
        if not prefix or prefix in seen:
            continue
        seen.add(prefix)
        for kind, (exe, _) in STORES.items():
            if os.path.isfile(os.path.join(prefix, "drive_c", exe)):
                found.append((kind, prefix))
                break
    return found


def store_root(kind, prefix):
    return os.path.join(prefix, "drive_c", STORES[kind][1])


def store_of(exe, prefixes):
    """(kind, root, folder, exe relative to the folder) when exe is a game in
    one of the stores' places, as written, and None otherwise."""
    for kind, prefix in prefixes:
        root = store_root(kind, prefix)
        folder = inside(exe, root)
        if folder:
            rel = relative(exe, os.path.join(root, folder))
            if rel:
                return kind, root, folder, rel
    return None


def same_place(a, b):
    try:
        return os.path.samefile(a, b)
    except OSError:
        return False


def store_runner(games, kind, prefix):
    """The runner the store's own entry has, which is what its games are
    started with too: one prefix is one wine."""
    exe = os.path.join(prefix, "drive_c", STORES[kind][0])
    for game in games:
        if absolute(text(game, "path")) == exe:
            return text(game, "runner") or "Default"
    return "Default"


def in_use(folder):
    """Whether a process of this person runs out of folder: its working
    directory, its program or a file it has mapped is inside it."""
    me = os.getuid()
    inner = folder.rstrip(os.sep) + os.sep
    for pid in os.listdir("/proc"):
        if not pid.isdigit() or str(os.getpid()) == pid:
            continue
        base = "/proc/" + pid
        try:
            if os.stat(base).st_uid != me:
                continue
        except OSError:
            continue
        for link in ("cwd", "exe"):
            try:
                target = os.readlink(os.path.join(base, link))
            except OSError:
                continue
            if target == folder or target.startswith(inner):
                return True
        try:
            with open(os.path.join(base, "maps"), encoding="utf-8", errors="replace") as f:
                for line in f:
                    if " " + inner in line:
                        return True
        except OSError:
            continue
    return False


def quiet(folder, seconds):
    """Whether nothing below folder has changed for that long."""
    limit = time.time() - seconds
    for top, dirs, files in os.walk(folder):
        for name in dirs + files:
            try:
                if os.lstat(os.path.join(top, name)).st_mtime > limit:
                    return False
            except OSError:
                return False
    return True


def link_store(kind, prefix, shared):
    """Makes one store's place a link to the shared folders.

    Games already in it are moved there first, a rename on one filesystem,
    each leaving a link behind until the whole place can become one. A game
    still being written or played stays where it is, and so does anything
    that would land on a folder of the same name: both are answered as a
    problem and tried again next time."""
    root = store_root(kind, prefix)
    report = {"kind": kind, "prefix": prefix, "moved": [], "problem": ""}

    if os.path.islink(root):
        if not same_place(root, shared):
            report["problem"] = "%s is a link to somewhere else" % root
        return report
    if not os.path.lexists(root):
        os.makedirs(os.path.dirname(root), exist_ok=True)
        os.symlink(shared, root)
        report["linked"] = True
        return report
    if not os.path.isdir(root):
        report["problem"] = "%s is not a directory" % root
        return report

    for name in sorted(os.listdir(root)):
        path = os.path.join(root, name)
        target = os.path.join(shared, name)
        if os.path.islink(path):
            if not same_place(path, target):
                report["problem"] = "%s is a link to somewhere else" % path
                return report
            continue
        if not os.path.isdir(path):
            report["problem"] = "%s is not a game's directory" % path
            return report
        if os.path.lexists(target):
            report["problem"] = "%s is in the shared folders already" % name
            return report
        if in_use(path) or not quiet(path, QUIET):
            report["problem"] = "%s is still being written or played" % name
            return report
        try:
            os.rename(path, target)
        except OSError as e:
            if e.errno == errno.EXDEV:
                report["problem"] = "%s is on another filesystem than the shared folders" % name
                return report
            raise
        os.symlink(target, path)
        report["moved"].append(name)

    # Nothing but links into the shared folders left: the place becomes one.
    for name in os.listdir(root):
        os.unlink(os.path.join(root, name))
    os.rmdir(root)
    os.symlink(shared, root)
    report["linked"] = True
    return report


def stores(shared):
    """Links every store's place in this member's Faugus, see link_store."""
    if not installed():
        out({"status": "absent", "stores": []})
        return
    games = load_games()
    if games is None:
        out({"status": "absent", "stores": []})
        return
    found = store_prefixes(games)
    if not found:
        out({"status": "none", "stores": []})
        return
    # Nothing is moved or linked under a running store: it holds the place
    # open and writes into it.
    who = busy()
    if who:
        out({"status": "busy", "who": who, "stores": []})
        return
    out({"status": "done", "stores": [link_store(k, p, shared) for k, p in found]})


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
    """Whether one existing entry already starts something in the folder,
    as written or through a link, which is how a store's games are named."""
    real = os.path.realpath(folder_dir)
    for key in ("path", "prefix"):
        path = absolute(text(game, key))
        if not path:
            continue
        for p, d in ((path, folder_dir), (os.path.realpath(path), real)):
            if p == d or p.startswith(d + os.sep):
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


def entry(recipe, base, gameid, store=None):
    """A whole entry with every key Faugus writes, so that nothing it reads
    has to fall back on a default it does not have.

    store is (prefix, runner) for a store's game: started through the store's
    place in this member's own store prefix, where the store is signed in."""
    if store:
        prefix, runner = store
        exe = os.path.normpath(os.path.join(
            store_root(recipe["store"], prefix), os.path.basename(base), recipe["exe"]))
    else:
        exe = os.path.normpath(os.path.join(base, recipe["exe"]))
        rel = recipe.get("prefix") or ""
        prefix = (os.path.normpath(os.path.join(base, rel)) if rel
                  else os.path.join(default_prefix(), gameid))
        runner = pick_runner(recipe.get("runner") or "", tools())
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
        "runner": runner,
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
        "icon": extract_icon(os.path.realpath(exe), gameid),
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

    store = None
    kind = recipe.get("store") or ""
    if kind:
        if kind not in STORES:
            out({"status": "refused"})
            return
        # The first prefix this member has the store in, once its place is
        # the link: before that the game would be started from a directory
        # the store does not have it in. Asked again on a later round.
        here = [p for k, p in store_prefixes(games) if k == kind
                and same_place(store_root(k, p), shared)]
        if not here:
            out({"status": "absent"})
            return
        store = (here[0], store_runner(games, kind, here[0]))

    new = entry(recipe, base, gameid, store)
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
        elif argv[:1] == ["stores"] and len(argv) == 2:
            stores(argv[1])
        else:
            out({"error": "usage: scan|register|unregister|move|repoint|stores ..."})
            return 2
    except (OSError, ValueError, KeyError, TypeError) as e:
        out({"error": "%s: %s" % (type(e).__name__, e)})
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
