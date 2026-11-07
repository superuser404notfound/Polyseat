package seat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// desktopSource is the Source of a game that came from a desktop entry.
const desktopSource = "desktop"

// desktopScan lists the shortcuts to games in the player's own applications
// directory, as JSON.
//
// A desktop entry's Exec line is taken apart here, by the rules of the desktop
// entry specification, because what Sunshine wants is not the same format: see
// sunshineCommand. The directory is the player's and so is what is in it, so
// the file is opened non-blocking and looked at before it is read, for the same
// reason foreignScan has grep do it: a FIFO called anything.desktop would
// otherwise hold this scan until it timed out.
const desktopScan = `
import json
import os
import re
import shutil
import stat

home = os.environ.get("POLYSEAT_HOME") or os.path.expanduser("~")
apps = os.path.join(home, ".local/share/applications")

LIMIT = 64 * 1024
ESCAPES = {"s": " ", "n": "\n", "t": "\t", "r": "\r", "\\": "\\"}


def read(path):
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NONBLOCK)
    except OSError:
        return ""

    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode):
            return ""

        return os.read(fd, LIMIT).decode("utf-8", "replace")
    except OSError:
        return ""
    finally:
        os.close(fd)


def entry_keys(text):
    """The keys of the main group, the first of each, and no action's."""
    keys = {}
    inside = False

    for line in text.splitlines():
        line = line.strip()

        if line.startswith("["):
            inside = line == "[Desktop Entry]"
        elif inside and "=" in line and not line.startswith("#"):
            key, value = line.split("=", 1)
            keys.setdefault(key.strip(), value.strip())

    return keys


def unescape(value):
    """The first of the two layers of escaping an Exec value has."""
    return re.sub(r"\\(.)", lambda m: ESCAPES.get(m.group(1), m.group(0)), value)


def split_exec(value):
    """The second layer: words, and quotes. None for an unbalanced line."""
    words = []
    word = None
    quoted = False
    chars = iter(value)

    for c in chars:
        if quoted:
            if c == '"':
                quoted = False
            elif c == "\\":
                word += next(chars, "")
            else:
                word += c
        elif c == '"':
            quoted = True
            word = word or ""
        elif c in " \t":
            if word is not None:
                words.append(word)
                word = None
        else:
            word = (word or "") + c

    if quoted:
        return None

    if word is not None:
        words.append(word)

    return words


def expand(words):
    """Without field codes: nobody hands a file to a card in Moonlight."""
    out = []

    for word in words:
        done = re.sub(r"%(.)", lambda m: "%" if m.group(1) == "%" else "", word)

        if done or not word:
            out.append(done)

    return out


def shortcut(keys):
    """The entry as a game, or None for one that is not a shortcut to a game."""
    if keys.get("Type") != "Application" or keys.get("Terminal") == "true":
        return None

    if keys.get("Hidden") == "true" or keys.get("NoDisplay") == "true":
        return None

    # Ours, whatever its file is called: steam.desktop is written from here.
    if any(key.startswith("X-Polyseat") for key in keys):
        return None

    if "Game" not in keys.get("Categories", "").split(";"):
        return None

    name = keys.get("Name", "")[:80]
    words = split_exec(unescape(keys.get("Exec", "")))

    if not name or not words:
        return None

    argv = expand(words)

    # Steam, Lutris and Faugus are asked directly and answer with more than
    # this has. Steam's shortcut says "Cities Skylines II" where its manifest
    # says "Cities: Skylines II", so the names would not have caught the
    # repeat. Faugus writes one of these per game when asked to, and the name
    # in it is the player's to change.
    if not argv or any("steam://" in a or a.startswith("lutris:") for a in argv):
        return None

    if os.path.basename(argv[0]) == "faugus-launcher" and "--game" in argv:
        return None

    # Heroic's shortcuts start a game through its link, and Heroic is asked
    # directly as well.
    if any(a.startswith("heroic://") for a in argv):
        return None

    # What the launcher does with TryExec, and what it should do here too: an
    # emulator updated to a new file name leaves shortcuts to the old one.
    if not shutil.which(argv[0]):
        return None

    if keys.get("TryExec") and not shutil.which(keys["TryExec"]):
        return None

    icon = keys.get("Icon", "")

    return {
        "name": name,
        "argv": argv,
        "icon": icon if os.path.isabs(icon) and os.path.isfile(icon) else "",
    }


def main():
    try:
        names = sorted(os.listdir(apps))
    except OSError:
        names = []

    found = []

    for name in names:
        if not name.endswith(".desktop") or name.startswith("polyseat-game-"):
            continue

        game = shortcut(entry_keys(read(os.path.join(apps, name))))

        if game:
            found.append(game)

    print(json.dumps(found))


if __name__ == "__main__":
    main()
`

// desktopPatience bounds the scan, which reads one directory of small files.
const desktopPatience = 30 * time.Second

// desktopGames is the games the player made a shortcut to and that no launcher
// can be asked about.
//
// Eden writes a desktop entry per game when asked to, each one the emulator's
// path and a ROM. The grid draws those because it draws every entry, and
// Moonlight's list is built from scans, so the game was in one menu and not the
// other. The emulator itself is a card through ~/Applications, but the card for
// one game of it has no other source than this file.
//
// known is what the other scans found: an entry for the emulator alone starts
// the same thing as its AppImage and is not a second game under another name.
func (p *Provisioner) desktopGames(ctx context.Context, known []Game) []Game {
	out, code, err := look(ctx, p.Client, p.name(), desktopPatience,
		pythonScan(desktopPatience, desktopScan)...)
	if err == nil && code != 0 {
		err = fmt.Errorf("%s", lastLines(out, 2))
	}

	var games []Game

	if err == nil {
		games, err = parseDesktopGames(out, known)
	}

	if err != nil {
		p.Log("! the seat's shortcuts to games could not be read: %v", err)
	}

	return games
}

// parseDesktopGames turns what desktopScan printed into games.
func parseDesktopGames(out string, known []Game) ([]Game, error) {
	var found []struct {
		Name string   `json:"name"`
		Argv []string `json:"argv"`
		Icon string   `json:"icon"`
	}

	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &found); err != nil {
		return nil, err
	}

	taken := map[string]bool{}

	for _, g := range known {
		taken[g.Launch] = true
	}

	games := make([]Game, 0, len(found))

	for _, f := range found {
		launch, ok := sunshineCommand(f.Argv)
		if !ok || taken[launch] {
			continue
		}

		games = append(games, Game{
			Name:   f.Name,
			Launch: launch,
			Image:  f.Icon,
			Source: desktopSource,
		})
	}

	return games, nil
}

// sunshineCommand writes a command the way Sunshine will read it back, and
// reports false for one that cannot be written.
//
// Sunshine runs no shell. It expands $(NAME) and $$ in the line itself, and then
// Boost.Process splits it at spaces outside double quotes and strips quotes that
// surround a whole word. There is no escape that survives: Boost counts every
// double quote towards being inside one, including one written as \", so a word
// with both a space and a quote in it comes apart whatever is done to it. Such a
// word is rare enough to leave out rather than start the wrong thing.
func sunshineCommand(argv []string) (string, bool) {
	words := make([]string, 0, len(argv))

	for _, word := range argv {
		if strings.Contains(word, `"`) {
			return "", false
		}

		word = strings.ReplaceAll(word, "$", "$$")

		if word == "" || strings.Contains(word, " ") {
			word = `"` + word + `"`
		}

		words = append(words, word)
	}

	return strings.Join(words, " "), len(words) > 0
}
