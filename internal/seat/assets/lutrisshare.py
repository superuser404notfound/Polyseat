#!/usr/bin/env python3
"""polyseat-lutrisshare - read and write one member's Lutris for the pool.

Runs as the person whose Lutris it is: in a seat as the player, on the host as
the owner of the library. Never as root, because everything it touches is that
person's own configuration and a file written by root in there is a file their
Lutris can no longer change.

The pool moves directories and nothing else. A Lutris game is a directory plus
a row in pga.db plus a YAML under ~/.config/lutris/games, and the last two are
per machine, so a folder that arrived somewhere was files nothing in that
Lutris knew about. This is the other half: "scan" says which games a member's
Lutris has and where their files are, and "register" writes an entry for a
folder that arrived, from what the member that installed it had.

Commands, each printing one JSON document:

    scan GAMES_ROOT SHARED       every wine and linux game, with its paths
    register SHARED DOC          add an entry for SHARED/<folder>, DOC being
                                 {"recipe": ..., "folder": ...}
    unregister SHARED FOLDER CONFIGPATH
                                 take back an entry register made
    move GAMES_ROOT SHARED SLUG  host only: move a game's folder into SHARED
                                 and point its entries at the new place
    repoint OLD NEW              point every entry into OLD at NEW instead,
                                 after another launcher moved the folder

What is read from an entry is a short list of keys and nothing else, see KEEP.
Lutris also has hooks that run commands around a launch and an environment it
hands the game, and none of that travels: a folder can come from any seat, and
the entry written here would run its commands as somebody else.
"""

import json
import os
import sqlite3
import sys
import time

import yaml

HOME = os.path.expanduser("~")
PGA = os.path.join(HOME, ".local/share/lutris/pga.db")
GAMES = os.path.join(HOME, ".config/lutris/games")

# The runners whose games are files in a directory. Steam, the emulators and
# the store runners keep theirs elsewhere or download them again.
RUNNERS = ("wine", "linux")

# The keys of an entry's game section worth carrying to another member. exe,
# prefix and working_dir are paths and are carried relative to the folder.
KEEP = ("exe", "prefix", "working_dir", "args", "arch")

# What register names its own entries by, so that a scan can tell an entry
# somebody made from one this made, and a recipe is only ever taken from the
# first kind.
SUFFIX = "-polyseat"


def out(doc):
    json.dump(doc, sys.stdout)
    sys.stdout.write("\n")


def connect():
    db = sqlite3.connect(PGA, timeout=10)
    db.row_factory = sqlite3.Row
    return db


def load_config(configpath):
    """An entry's YAML, or an empty one. A file that does not parse is treated
    as having nothing in it rather than ending the scan of every other game."""
    if not configpath or "/" in configpath or configpath.startswith("."):
        return {}
    try:
        with open(os.path.join(GAMES, configpath + ".yml"), encoding="utf-8") as f:
            data = yaml.safe_load(f)
    except (OSError, yaml.YAMLError, UnicodeDecodeError):
        return {}
    return data if isinstance(data, dict) else {}


def section(config, name):
    value = config.get(name)
    return value if isinstance(value, dict) else {}


def absolute(path, base):
    """A path from an entry as Lutris reads it: ~ expanded, and relative to
    the game's directory when it is relative."""
    if not isinstance(path, str) or not path:
        return ""
    path = os.path.expanduser(path)
    if not os.path.isabs(path):
        if not base:
            return ""
        path = os.path.join(base, path)
    return os.path.normpath(path)


def inside(path, root):
    """The first component of path below root, or empty when it is not below
    it. Paths are compared as written, not resolved: a link inside a folder is
    the folder's business and following it here would let it point anywhere."""
    if not path or not root:
        return ""
    rel = os.path.relpath(path, root)
    if rel == "." or rel.startswith(".." + os.sep) or rel == "..":
        return ""
    return rel.split(os.sep, 1)[0]


def relative(path, folder):
    if not path:
        return ""
    rel = os.path.relpath(path, folder)
    if rel == ".." or rel.startswith(".." + os.sep):
        return ""
    return rel


def games(db):
    marks = ",".join("?" * len(RUNNERS))
    return db.execute(
        "select id, name, slug, runner, platform, directory, configpath "
        "from games where installed = 1 and runner in (%s) order by id" % marks,
        RUNNERS,
    ).fetchall()


def describe(row, games_root, shared):
    config = load_config(row["configpath"])
    game = section(config, "game")
    wine = section(config, "wine")

    directory = absolute(row["directory"] or "", None)
    exe = absolute(game.get("exe"), directory)
    prefix = absolute(game.get("prefix"), directory)
    working = absolute(game.get("working_dir"), directory)

    # What places the game: its directory when Lutris has one, else the
    # program it starts, else the prefix it runs in. A game added by hand has
    # no directory, only an exe, and that is the case that started this.
    anchor = directory or exe or prefix

    item = {
        "id": row["id"],
        "name": row["name"] or "",
        "slug": row["slug"] or "",
        "runner": row["runner"] or "",
        "platform": row["platform"] or "",
        "configpath": row["configpath"] or "",
        "ours": (row["configpath"] or "").endswith(SUFFIX),
        "anchor": anchor,
        "folder": inside(anchor, shared),
        "home_folder": inside(anchor, games_root),
    }

    if item["folder"]:
        base = os.path.join(shared, item["folder"])
        item["exe"] = relative(exe, base)
        item["prefix"] = relative(prefix, base)
        item["working_dir"] = relative(working, base)
        # A prefix or working directory outside the folder is not carried: it
        # is something on this member's disk, and the receiver has its own.
        # Lutris then falls back to its default for both.
        for key in ("args", "arch"):
            value = game.get(key)
            if isinstance(value, str):
                item[key] = value
        version = wine.get("version")
        if isinstance(version, str):
            item["wine_version"] = version
    return item


def scan(games_root, shared):
    if not os.path.isfile(PGA):
        out({"present": False, "games": []})
        return
    db = connect()
    try:
        found = [describe(row, games_root, shared) for row in games(db)]
    finally:
        db.close()
    out({"present": True, "games": found})


# ------------------------------------------------------------------ register


def compat_tools():
    """Every Wine and Proton build this member's Lutris can name, by the name
    it uses: the directory name, which is what wine.version holds."""
    names = []
    for root, marker in (
        (os.path.join(HOME, ".local/share/lutris/runners/wine"), None),
        (os.path.join(HOME, ".local/share/Steam/compatibilitytools.d"), "proton"),
        (os.path.join(HOME, ".steam/root/compatibilitytools.d"), "proton"),
        ("/usr/share/steam/compatibilitytools.d", "proton"),
        (os.path.join(HOME, ".local/share/Steam/steamapps/common"), "proton"),
    ):
        try:
            entries = sorted(os.listdir(root))
        except OSError:
            continue
        for name in entries:
            path = os.path.join(root, name)
            if not os.path.isdir(path):
                continue
            if marker and not os.path.isfile(os.path.join(path, marker)):
                continue
            if name not in names:
                names.append(name)
    return names


def family(name):
    lower = name.lower()
    if "cachyos" in lower:
        return "cachyos"
    if "ge-proton" in lower or "proton-ge" in lower or lower.startswith("ge-"):
        return "ge"
    if lower.startswith("proton"):
        return "proton"
    return ""


def pick_version(wanted, available):
    """The build to name in the new entry.

    The same name where it exists. Otherwise the same kind of build, because
    the host's Lutris calls Proton CachyOS "Proton-CachyOS Latest" and a seat's
    calls it "proton-cachyos", and a name nothing here has would make Lutris
    refuse the game. Nothing at all when there is no such build either, which
    leaves the choice to this member's own default."""
    if not wanted:
        return ""
    # Lutris's own name for the newest GE build, which it resolves itself.
    if wanted == "ge-proton":
        return wanted
    if wanted in available:
        return wanted
    kind = family(wanted)
    if kind:
        for name in available:
            if family(name) == kind:
                return name
    return ""


def covers(row, folder_dir):
    """Whether one existing entry already points into the folder."""
    config = load_config(row["configpath"])
    game = section(config, "game")
    directory = absolute(row["directory"] or "", None)
    for path in (directory, absolute(game.get("exe"), directory),
                 absolute(game.get("prefix"), directory)):
        if path and (path == folder_dir or path.startswith(folder_dir + os.sep)):
            return True
    return False


def register(shared, doc):
    recipe, folder = doc["recipe"], doc["folder"]
    base = os.path.join(shared, folder)

    if not os.path.isdir(base):
        out({"status": "missing"})
        return

    if not os.path.isfile(PGA):
        # Lutris has never run here, so it has no database to add to yet. The
        # daemon asks again on a later pass.
        out({"status": "absent"})
        return

    db = connect()
    try:
        for row in db.execute("select id, slug, directory, configpath from games"):
            if covers(row, base):
                out({"status": "present", "written": row["configpath"] or ""})
                return
            if row["slug"] == recipe["slug"]:
                out({"status": "taken", "written": row["configpath"] or ""})
                return

        configpath = recipe["slug"] + SUFFIX
        game = {}
        for key in ("exe", "prefix", "working_dir"):
            rel = recipe.get(key) or ""
            if rel:
                game[key] = os.path.normpath(os.path.join(base, rel))
        for key in ("args", "arch"):
            if recipe.get(key):
                game[key] = recipe[key]

        config = {"game": game}
        version = pick_version(recipe.get("wine_version") or "", compat_tools())
        if recipe["runner"] == "wine" and version:
            config["wine"] = {"version": version}

        os.makedirs(GAMES, exist_ok=True)
        path = os.path.join(GAMES, configpath + ".yml")
        tmp = path + ".tmp"
        with open(tmp, "w", encoding="utf-8") as f:
            f.write("# Written by Polyseat when this folder arrived from the shared\n")
            f.write("# library. Change it in Lutris like any other game.\n")
            yaml.safe_dump(config, f, allow_unicode=True, sort_keys=False)
        os.replace(tmp, path)

        with db:
            db.execute(
                "insert into games (name, slug, runner, platform, directory, "
                "installed, installed_at, configpath) values (?, ?, ?, ?, ?, 1, ?, ?)",
                (recipe["name"], recipe["slug"], recipe["runner"],
                 recipe.get("platform") or "", base, int(time.time()), configpath),
            )
    finally:
        db.close()

    out({"status": "added", "written": configpath, "wine_version": version})


def unregister(shared, folder, configpath):
    """Removes the entry register made for a folder that is gone, and only that
    one: the row has to carry both the name register gave it and the folder it
    was written for, so an entry somebody made by hand is never touched."""
    base = os.path.join(shared, folder)
    if not configpath.endswith(SUFFIX) or "/" in configpath:
        out({"status": "refused"})
        return
    if not os.path.isfile(PGA):
        out({"status": "gone"})
        return
    db = connect()
    try:
        with db:
            n = db.execute("delete from games where configpath = ? and directory = ?",
                           (configpath, base)).rowcount
    finally:
        db.close()
    if n:
        try:
            os.unlink(os.path.join(GAMES, configpath + ".yml"))
        except OSError:
            pass
    out({"status": "removed" if n else "gone"})


# ---------------------------------------------------------------------- move


def rewrite(value, old, new):
    """Every path in an entry that points into old, pointed into new instead.
    Recursive because an installer's own script, kept in the same file, names
    the directory as often as the game section does."""
    if isinstance(value, str):
        if value == old or value.startswith(old + os.sep):
            return new + value[len(old):]
        return value
    if isinstance(value, list):
        return [rewrite(v, old, new) for v in value]
    if isinstance(value, dict):
        return {k: rewrite(v, old, new) for k, v in value.items()}
    return value


def move(games_root, shared, slug):
    db = connect()
    try:
        rows = games(db)
        target = None
        for row in rows:
            if row["slug"] == slug:
                target = describe(row, games_root, shared)
                break
        if target is None:
            out({"error": "Lutris has no game called %s" % slug})
            return
        name = target["home_folder"]
        if target["folder"] or not name or name == os.path.basename(shared):
            out({"error": "%s is not in a folder of its own below %s" % (slug, games_root)})
            return

        old = os.path.join(games_root, name)
        new = os.path.join(shared, name)
        if os.path.islink(old) or not os.path.isdir(old):
            out({"error": "%s is not a plain directory" % old})
            return
        if os.path.lexists(new):
            out({"error": "%s already exists" % new})
            return

        # A rename, never a copy: the two are on one filesystem by
        # construction, shared being inside games_root, and a game that has to
        # be copied in full to be shared is a decision about somebody's disk.
        os.rename(old, new)
        moved = repoint_entries(db, old, new)
    finally:
        db.close()

    out({"status": "moved", "folder": name, "entries": moved, "old": old, "new": new})


def repoint_entries(db, old, new):
    """Every row and YAML pointing into old, pointed into new instead."""
    moved = []
    with db:
        for row in db.execute("select id, directory, configpath from games"):
            directory = row["directory"] or ""
            if directory == old or directory.startswith(old + os.sep):
                db.execute("update games set directory = ? where id = ?",
                           (rewrite(directory, old, new), row["id"]))
            configpath = row["configpath"] or ""
            config = load_config(configpath)
            if not config:
                continue
            changed = rewrite(config, old, new)
            if changed == config:
                continue
            path = os.path.join(GAMES, configpath + ".yml")
            tmp = path + ".tmp"
            with open(tmp, "w", encoding="utf-8") as f:
                yaml.safe_dump(changed, f, allow_unicode=True, sort_keys=False)
            os.replace(tmp, path)
            moved.append(configpath)
    return moved


def repoint(old, new):
    if not os.path.isabs(old) or not os.path.isabs(new):
        out({"error": "repoint wants two absolute paths"})
        return
    if not os.path.isfile(PGA):
        out({"status": "gone"})
        return
    db = connect()
    try:
        moved = repoint_entries(db, os.path.normpath(old), os.path.normpath(new))
    finally:
        db.close()
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
    except (OSError, sqlite3.Error, ValueError, KeyError) as e:
        out({"error": "%s: %s" % (type(e).__name__, e)})
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
