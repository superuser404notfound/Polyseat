package seat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// heroicSource is the Source of a game that came from Heroic.
const heroicSource = "heroic"

// heroicScan lists what Heroic has installed, as JSON.
//
// Read out of the three lists the store tools keep rather than asked of
// Heroic, which has no listing command and is an Electron program a scan has
// no business starting. DLC is left out, it is part of its game. The name is
// the entry's own where it has one, otherwise the one in Heroic's cache of the
// store's library, otherwise the directory's.
//
// No Heroic means no games, for the reason faugusScan gives.
const heroicScan = `
import json
import os
import shutil

home = os.environ.get("POLYSEAT_HOME") or os.path.expanduser("~")
config = os.path.join(home, ".config/heroic")


def load(*parts):
    try:
        with open(os.path.join(config, *parts), encoding="utf-8") as f:
            return json.load(f)
    except (OSError, ValueError):
        return None


def names():
    found = {}
    for runner, name in (("gog", "gog_library.json"), ("nile", "nile_library.json"),
                         ("legendary", "legendary_library.json")):
        data = load("store_cache", name)
        games = data.get("games") if isinstance(data, dict) else None
        if isinstance(games, dict):
            games = list(games.values())
        for g in games if isinstance(games, list) else []:
            if isinstance(g, dict) and isinstance(g.get("title"), str):
                app = g.get("app_name") or g.get("appName")
                if isinstance(app, str):
                    found[(runner, app)] = g["title"]
    return found


def main():
    if not shutil.which("heroic"):
        print("[]")
        return
    known = names()
    found = []
    legendary = load("legendaryConfig", "legendary", "installed.json")
    for app, e in (legendary.items() if isinstance(legendary, dict) else []):
        if isinstance(e, dict) and not e.get("is_dlc"):
            found.append(("legendary", app, e.get("title"), e.get("install_path")))
    gog = load("gog_store", "installed.json")
    for e in (gog.get("installed") if isinstance(gog, dict) else None) or []:
        if isinstance(e, dict) and not e.get("is_dlc"):
            found.append(("gog", str(e.get("appName", "")), None, e.get("install_path")))
    nile = load("nile_config", "nile", "installed.json")
    for e in nile if isinstance(nile, list) else []:
        if isinstance(e, dict):
            found.append(("nile", str(e.get("id", "")), None, e.get("path")))
    out = []
    for runner, app, title, path in found:
        if not isinstance(title, str) or not title:
            title = known.get((runner, app)) or (os.path.basename(path) if isinstance(path, str) else "")
        if app and title:
            out.append({"runner": runner, "app": app, "title": title[:80]})
    print(json.dumps(out))


main()
`

// heroicGames lists what Heroic has in this seat.
func (p *Provisioner) heroicGames(ctx context.Context) ([]Game, error) {
	out, code, err := look(ctx, p.Client, p.name(), scanPatience,
		pythonScan(scanPatience, heroicScan)...)
	if err != nil {
		return nil, err
	}

	if code != 0 {
		return nil, fmt.Errorf("%s", lastLines(out, 2))
	}

	return heroicListing(out)
}

// heroicListing turns what heroicScan printed into games, each started the
// way Heroic's own shortcuts start one: in the background, through its link.
func heroicListing(out string) ([]Game, error) {
	var found []struct {
		Runner string `json:"runner"`
		App    string `json:"app"`
		Title  string `json:"title"`
	}

	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &found); err != nil {
		return nil, fmt.Errorf("read what Heroic has: %w", err)
	}

	var games []Game

	for _, f := range found {
		if !heroicKey.MatchString(f.Runner+":"+f.App) || strings.TrimSpace(f.Title) == "" {
			continue
		}

		launch, ok := sunshineCommand([]string{
			"heroic", "--no-gui", "heroic://launch?appName=" + f.App + "&runner=" + f.Runner,
		})
		if !ok {
			continue
		}

		games = append(games, Game{Name: f.Title, Launch: launch, Source: heroicSource})
	}

	return games, nil
}
