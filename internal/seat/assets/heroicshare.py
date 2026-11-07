#!/usr/bin/env python3
"""polyseat-heroicshare - read and write one member's Heroic for the pool.

Runs as the person whose Heroic it is: in a seat as the player, on the host as
the owner of the library. Never as root, for the reason lutrisshare.py gives.

Heroic installs games from three stores, each through a tool of its own, and
each tool keeps the games it installed in a list of its own under
~/.config/heroic:

    legendary (Epic)   legendaryConfig/legendary/installed.json, keyed by app
    gogdl (GOG)        gog_store/installed.json, {"installed": [...]}
    nile (Amazon)      nile_config/nile/installed.json, a list

An entry names the directory the game is in, install_path or path, and Heroic
calls the game installed when there is one. The pool moves the directory and
not the entry, so this is the other half, the same commands faugusshare.py
has:

    scan GAMES_ROOT SHARED       every game, with its paths
    register SHARED DOC          add an entry for SHARED/<folder>
    unregister SHARED FOLDER KEY take back an entry register made
    move GAMES_ROOT SHARED KEY   host only: move a game's directory into SHARED
                                 and point its entry at the new place
    repoint OLD NEW              point every entry into OLD at NEW instead,
                                 after another launcher moved the folder
    install-path SHARED          make SHARED where Heroic installs from now on

A game is keyed RUNNER:APP, the store and its own id, because Heroic keys it
by that id and the receiver's entry has to have the same one: the store looks
the game up by it in the account somebody signed in with. That is also why
register cannot name its entries apart the way the other two helpers do, and
keeps the list of what it wrote in a file of its own instead.

What travels is a short list of keys per store, see KEEP. The rest of an entry
is the receiver's: where the game is, and anything that names a path.
"""

import json
import os
import re
import shutil
import sys
import tempfile

HOME = os.path.expanduser("~")
CONFIG = os.path.join(os.environ.get("XDG_CONFIG_HOME") or os.path.join(HOME, ".config"),
                      "heroic")

LEGENDARY = os.path.join(CONFIG, "legendaryConfig", "legendary", "installed.json")
GOG = os.path.join(CONFIG, "gog_store", "installed.json")
NILE = os.path.join(CONFIG, "nile_config", "nile", "installed.json")

# Where register writes down what it wrote, see the top of this file.
WRITTEN = os.path.join(CONFIG, "polyseat-written.json")

# The keys of an entry worth carrying to another member, per store. Every one
# is a plain value or a list of strings, which the daemon checks again. Paths
# are not among them: install_path is set to the receiver's own place, and
# legendary's save_path, manifest_path and prereq_info name the member they
# were written on.
KEEP = {
    "legendary": ("app_name", "title", "version", "executable", "platform",
                  "install_size", "is_dlc", "can_run_offline", "requires_ot",
                  "launch_parameters", "install_tags", "base_urls", "egl_guid",
                  "needs_verification"),
    "gog": ("appName", "platform", "executable", "install_size", "is_dlc",
            "version", "installedWith", "language", "versionEtag", "buildId",
            "pinnedVersion"),
    "nile": ("id", "version", "size"),
}

RUNNERS = tuple(KEEP)

# What an id may look like in a key: Epic's are letters and digits, GOG's
# digits, Amazon's amzn1.adg.product. and a uuid.
APP = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")


def out(doc):
    json.dump(doc, sys.stdout)
    sys.stdout.write("\n")


def installed():
    return shutil.which("heroic") is not None


def read_json(path, default):
    try:
        with open(path, encoding="utf-8") as f:
            return json.load(f)
    except FileNotFoundError:
        return default


def write_json(path, data):
    """Whole and at once, for the reason faugusshare.py gives."""
    os.makedirs(os.path.dirname(path), exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=os.path.dirname(path), prefix=".polyseat.")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(data, f, indent=2, ensure_ascii=False)
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


def busy():
    """What of Heroic is running as this person, said the way a refusal
    names it, or empty when nothing is.

    Heroic and the tools it runs hold these lists while they work and write
    them whole, so an entry added under a running Heroic can be gone the next
    time it saves."""
    me = os.getuid()
    for pid in os.listdir("/proc"):
        if not pid.isdigit() or str(os.getpid()) == pid:
            continue
        try:
            if os.stat("/proc/" + pid).st_uid != me:
                continue
            with open("/proc/%s/cmdline" % pid, "rb") as f:
                argv = [a for a in f.read().split(b"\0") if a]
        except OSError:
            continue
        if not argv:
            continue
        name = os.path.basename(argv[0])
        if name in (b"heroic", b"Heroic", b"legendary", b"gogdl", b"nile"):
            return "Heroic is open"
        if any(a.endswith(b"heroic-games-launcher/app.asar") or a.endswith(b"Heroic/resources/app.asar")
               for a in argv[1:3]):
            return "Heroic is open"
    return ""


# ---------------------------------------------------------------- the lists


class Lists:
    """The three lists, read once, written back only where something changed."""

    def __init__(self):
        self.legendary = read_json(LEGENDARY, {})
        if not isinstance(self.legendary, dict):
            raise ValueError("%s is not a map of games" % LEGENDARY)
        gog = read_json(GOG, {"installed": []})
        if not isinstance(gog, dict) or not isinstance(gog.get("installed", []), list):
            raise ValueError("%s is not a list of games" % GOG)
        self.gog = gog
        self.gog.setdefault("installed", [])
        self.nile = read_json(NILE, [])
        if not isinstance(self.nile, list):
            raise ValueError("%s is not a list of games" % NILE)
        self.changed = set()

    def entries(self):
        """(runner, app, entry) for every game, DLC left out: it is files in
        its game's directory and goes where that goes."""
        for app, e in self.legendary.items():
            if isinstance(e, dict) and not e.get("is_dlc"):
                yield "legendary", app, e
        for e in self.gog["installed"]:
            if isinstance(e, dict) and not e.get("is_dlc"):
                yield "gog", str(e.get("appName", "")), e
        for e in self.nile:
            if isinstance(e, dict):
                yield "nile", str(e.get("id", "")), e

    @staticmethod
    def path_key(runner):
        return "path" if runner == "nile" else "install_path"

    def find(self, runner, app):
        for r, a, e in self.entries():
            if r == runner and a == app:
                return e
        return None

    def add(self, runner, app, entry):
        if runner == "legendary":
            self.legendary[app] = entry
        elif runner == "gog":
            self.gog["installed"].append(entry)
        else:
            self.nile.append(entry)
        self.changed.add(runner)

    def remove(self, runner, app):
        if runner == "legendary":
            self.legendary.pop(app, None)
        elif runner == "gog":
            self.gog["installed"] = [e for e in self.gog["installed"]
                                     if not (isinstance(e, dict) and str(e.get("appName")) == app)]
        else:
            self.nile = [e for e in self.nile
                         if not (isinstance(e, dict) and str(e.get("id")) == app)]
        self.changed.add(runner)

    def save(self):
        if "legendary" in self.changed:
            write_json(LEGENDARY, self.legendary)
        if "gog" in self.changed:
            write_json(GOG, self.gog)
        if "nile" in self.changed:
            write_json(NILE, self.nile)
        self.changed = set()


def titles():
    """The names Heroic shows, from its caches of the stores' libraries.
    legendary's entry has its own; the other two only an id."""
    found = {}
    cache = os.path.join(CONFIG, "store_cache")
    for runner, name in (("gog", "gog_library.json"), ("nile", "nile_library.json"),
                         ("legendary", "legendary_library.json")):
        data = read_json(os.path.join(cache, name), {})
        games = data.get("games") if isinstance(data, dict) else None
        if isinstance(games, dict):
            games = list(games.values())
        for g in games if isinstance(games, list) else []:
            if isinstance(g, dict) and isinstance(g.get("title"), str):
                app = g.get("app_name") or g.get("appName")
                if isinstance(app, str):
                    found[(runner, app)] = g["title"]
    return found


def written():
    data = read_json(WRITTEN, {})
    return data if isinstance(data, dict) else {}


# ------------------------------------------------------------------- paths


def absolute(path):
    if not isinstance(path, str) or not path:
        return ""
    path = os.path.expanduser(path)
    return os.path.normpath(path) if os.path.isabs(path) else ""


def inside(path, root):
    """The first component of path below root, or empty when it is not below
    it. Compared as written, as the other helpers do."""
    if not path or not root:
        return ""
    rel = os.path.relpath(path, root)
    if rel == "." or rel == ".." or rel.startswith(".." + os.sep):
        return ""
    return rel.split(os.sep, 1)[0]


def below(path, root):
    """path relative to root when it is below it, or empty."""
    if not path or not root:
        return ""
    rel = os.path.relpath(path, root)
    if rel == "." or rel == ".." or rel.startswith(".." + os.sep):
        return ""
    return rel


# ----------------------------------------------------------------- commands


def describe(runner, app, entry, games_root, shared, names, ours):
    path = absolute(entry.get(Lists.path_key(runner)))
    key = runner + ":" + app
    title = entry.get("title") if isinstance(entry.get("title"), str) else ""
    title = title or names.get((runner, app)) or os.path.basename(path) or app
    folder = inside(path, shared)
    item = {
        "key": key,
        "runner": runner,
        "app": app,
        "title": title,
        "ours": ours.get(key) == folder and bool(folder),
        "folder": folder,
        # The game's own directory below the games root. Heroic puts every
        # game in one of its own, ~/Games/Heroic/<game> unless told otherwise,
        # so it is the whole path and not its first component: Heroic is more
        # than this one game.
        "home_folder": "" if folder else below(path, games_root),
    }
    if folder and os.path.normpath(os.path.join(shared, folder)) == path:
        item["entry"] = json.dumps({k: entry[k] for k in KEEP[runner] if k in entry},
                                   sort_keys=True, ensure_ascii=False)
    elif folder:
        # Somewhere further down in the folder: not a game this carries.
        item["folder"] = ""
    return item


def scan(games_root, shared):
    if not installed():
        out({"present": False, "games": []})
        return
    lists = Lists()
    names = titles()
    ours = written()
    out({"present": True,
         "games": [describe(r, a, e, games_root, shared, names, ours)
                   for r, a, e in lists.entries() if APP.match(a)]})


def register(shared, doc):
    recipe, folder = doc["recipe"], doc["folder"]
    runner, app = recipe["runner"], recipe["app"]
    base = os.path.join(shared, folder)

    if runner not in RUNNERS or not APP.match(app) or "/" in folder:
        out({"status": "refused"})
        return
    if not os.path.isdir(base):
        out({"status": "missing"})
        return
    if not installed():
        out({"status": "absent"})
        return
    if busy():
        out({"status": "busy"})
        return

    lists = Lists()
    have = lists.find(runner, app)
    if have is not None:
        here = absolute(have.get(Lists.path_key(runner)))
        if here == os.path.normpath(base):
            out({"status": "present", "written": ""})
        else:
            out({"status": "taken", "written": ""})
        return

    entry = json.loads(recipe["entry"])
    entry = {k: entry[k] for k in KEEP[runner] if k in entry}
    entry[Lists.path_key(runner)] = os.path.normpath(base)
    if runner == "legendary":
        entry["app_name"] = app
        entry.update({"save_path": None, "manifest_path": None, "prereq_info": None})
    elif runner == "gog":
        entry["appName"] = app
    else:
        entry["id"] = app
    lists.add(runner, app, entry)
    lists.save()

    ours = written()
    key = runner + ":" + app
    ours[key] = folder
    write_json(WRITTEN, ours)
    out({"status": "added", "written": key})


def unregister(shared, folder, key):
    """Removes an entry register made, and only that: it has to be on the
    list register keeps, for that folder, and still point into it."""
    ours = written()
    if ours.get(key) != folder or ":" not in key:
        out({"status": "refused"})
        return
    if not installed():
        out({"status": "gone"})
        return
    if busy():
        out({"error": "Heroic is open, so its list cannot be changed under it"})
        return
    runner, app = key.split(":", 1)
    lists = Lists()
    have = lists.find(runner, app)
    base = os.path.normpath(os.path.join(shared, folder))
    if have is not None and absolute(have.get(Lists.path_key(runner))) == base:
        lists.remove(runner, app)
        lists.save()
        status = "removed"
    else:
        status = "gone"
    del ours[key]
    write_json(WRITTEN, ours)
    out({"status": status})


def repoint_lists(lists, old, new):
    moved = []
    for runner, app, entry in list(lists.entries()):
        k = Lists.path_key(runner)
        path = absolute(entry.get(k))
        if path == old or path.startswith(old + os.sep):
            entry[k] = new + path[len(old):]
            lists.changed.add(runner)
            moved.append(runner + ":" + app)
    return moved


def move(games_root, shared, key):
    who = busy()
    if who:
        out({"error": "%s here; close it first, it would write the old place back" % who})
        return
    if ":" not in key:
        out({"error": "Heroic has no game called %s" % key})
        return
    runner, app = key.split(":", 1)
    lists = Lists()
    entry = lists.find(runner, app)
    if entry is None:
        out({"error": "Heroic has no game called %s" % key})
        return
    old = absolute(entry.get(Lists.path_key(runner)))
    rel = below(old, games_root)
    if not rel or inside(old, shared):
        out({"error": "%s is not in a directory of its own below %s" % (key, games_root)})
        return
    name = os.path.basename(old)
    new = os.path.join(shared, name)
    if os.path.islink(old) or not os.path.isdir(old):
        out({"error": "%s is not a plain directory" % old})
        return
    if os.path.lexists(new):
        out({"error": "%s already exists" % new})
        return

    # A rename, never a copy, for the reason lutrisshare.py gives.
    os.rename(old, new)
    moved = repoint_lists(lists, old, new)
    lists.save()
    out({"status": "moved", "folder": name, "entries": moved, "old": old, "new": new})


def repoint(old, new):
    if not os.path.isabs(old) or not os.path.isabs(new):
        out({"error": "repoint wants two absolute paths"})
        return
    if not installed():
        out({"status": "gone"})
        return
    who = busy()
    if who:
        out({"error": "%s, so its list cannot be changed under it" % who})
        return
    lists = Lists()
    moved = repoint_lists(lists, os.path.normpath(old), os.path.normpath(new))
    lists.save()
    out({"status": "repointed" if moved else "gone"})


def install_path(shared):
    """Makes SHARED the directory Heroic installs into, so that a game
    installed from now on is a shared folder from its first byte.

    Left alone when somebody chose a directory of their own, anything but
    Heroic's ~/Games/Heroic: that was a decision. Heroic reads its settings
    from config.json and fills what is missing with its defaults, so setting
    one before Heroic has ever been started is safe, and it keeps a second
    copy of them in store/config.json, which the install dialog reads."""
    if not installed():
        out({"status": "absent", "stores": []})
        return
    who = busy()
    if who:
        out({"status": "busy", "who": who, "stores": []})
        return
    default = os.path.join(HOME, "Games", "Heroic")
    linked = False
    problem = ""

    path = os.path.join(CONFIG, "config.json")
    config = read_json(path, {})
    if not isinstance(config, dict):
        out({"status": "done", "stores": [{"kind": "heroic", "problem": "%s is not a map" % path}]})
        return
    settings = config.setdefault("defaultSettings", {})
    current = absolute(settings.get("defaultInstallPath")) or default
    if current == os.path.normpath(shared):
        pass
    elif current == default:
        settings["defaultInstallPath"] = shared
        config.setdefault("version", "v0")
        write_json(path, config)
        linked = True
    else:
        problem = "Heroic installs into %s, which somebody chose" % current

    store = os.path.join(CONFIG, "store", "config.json")
    data = read_json(store, None)
    if isinstance(data, dict) and isinstance(data.get("settings"), dict) and not problem:
        s = data["settings"]
        if absolute(s.get("defaultInstallPath")) in ("", default):
            s["defaultInstallPath"] = shared
            write_json(store, data)
            linked = True

    report = {"kind": "heroic", "moved": [], "problem": problem}
    if linked:
        report["linked"] = True
    out({"status": "done", "stores": [report]})


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
        elif argv[:1] == ["install-path"] and len(argv) == 2:
            install_path(argv[1])
        else:
            out({"error": "usage: scan|register|unregister|move|repoint|install-path ..."})
            return 2
    except (OSError, ValueError, KeyError, TypeError) as e:
        out({"error": "%s: %s" % (type(e).__name__, e)})
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
