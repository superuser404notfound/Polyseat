package seat

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// appsInterval is how often the app list is rebuilt from what a seat has.
//
// A minute rather than the sweep's ten seconds, because the scan reads Steam's
// manifests and starts Lutris to ask it for a list. Installing or removing
// something through the interface rebuilds it at once, so this is only the
// path that catches what the player did inside the seat.
const appsInterval = time.Minute

// Game is something installed that can be started directly.
//
// The point is the client with a controller and nothing else. Picking a
// launcher in Moonlight means waiting for it to start, then navigating its
// interface with a thumbstick to reach the game. Picking the game means
// picking the game.
type Game struct {
	Name   string
	Launch string
	Image  string

	// Steam is the application id, when this came from Steam. Carried so that
	// a cover can be fetched for a game whose artwork Steam never downloaded
	// into this seat, which is every game delivered by the shared library and
	// not yet looked at here.
	Steam string
}

// maxGames bounds the app list.
//
// Moonlight will show whatever it is given, but a list of two hundred takes
// longer to scroll through than opening Steam would have, and it is a list
// somebody is scrolling with a thumbstick. Which titles were left out is said
// out loud rather than silently dropped.
const maxGames = 60

// steamScan reads Steam's manifests inside the seat.
//
// Done in the seat with a script rather than from the host, because only one of
// the two library directories is visible from outside: a seat that takes part
// in the shared library has that mounted, and its private one is inside the
// container either way.
//
// Regular expressions on a format with a real parser elsewhere in this project
// is a deliberate trade. The alternative is copying every manifest out of the
// container to parse it properly, and what is wanted here is two fields.
const steamScan = `
import glob, json, os, re

dirs = ["/home/player/.local/share/Steam/steamapps", "/home/player/games/steamapps"]
art = "/home/player/.local/share/Steam/appcache/librarycache"
out = {}

for d in dirs:
    for path in sorted(glob.glob(d + "/appmanifest_*.acf")):
        try:
            text = open(path, encoding="utf-8", errors="replace").read()
        except OSError:
            continue

        def field(key):
            m = re.search(r'"%s"\s+"([^"]*)"' % key, text, re.IGNORECASE)
            return m.group(1) if m else ""

        appid, name, state = field("appid"), field("name"), field("StateFlags")

        # 4 is fully installed. Anything else is mid download or mid update,
        # and offering it would be offering something that cannot start.
        if not appid or not name or state != "4" or appid in out:
            continue

        cover = ""
        for candidate in ("library_600x900.jpg", "library_600x900.png"):
            p = os.path.join(art, appid, candidate)
            if os.path.exists(p):
                cover = p
                break

        out[appid] = {"appid": appid, "name": name, "cover": cover}

print(json.dumps(list(out.values())))
`

// steamTool reports whether an installed Steam app is a tool rather than a game.
//
// There is no field in the manifest that says so, which was measured rather
// than assumed: LastOwner is 0 for tools and a real account for games, but the
// library pool deliberately zeroes it when it clones a title, so every game
// delivered to a seat looks like a tool by that test. DownloadType does not
// separate them either; a game and a tool were both seen with 1.
//
// So it is the name, and the list is deliberately narrow. Offering a tool by
// mistake is a wasted entry in a menu. Hiding a game by mistake is somebody
// unable to start their game and no way to tell why, so anything not
// recognised here is offered.
func steamTool(appid, name string) bool {
	// Steamworks Common Redistributables, installed alongside a great many
	// games and named nothing like the rest of this list.
	if appid == "228980" {
		return true
	}

	for _, prefix := range []string{
		"Proton Experimental",
		"Proton Hotfix",
		"Proton EasyAntiCheat Runtime",
		"Proton BattlEye Runtime",
		"Steam Linux Runtime",
		"Steamworks ",
		"Steam Controller ",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}

	// Proton releases are named "Proton 9.0", "Proton 8.0" and so on. Matched
	// on the digit so that a game called Proton something is left alone, which
	// is not hypothetical: Proton Pulse is a real one.
	if rest, ok := strings.CutPrefix(name, "Proton "); ok && rest != "" {
		if rest[0] >= '0' && rest[0] <= '9' {
			return true
		}
	}

	return false
}

// steamGames lists the games Steam has installed in this seat.
func (p *Provisioner) steamGames(ctx context.Context) ([]Game, error) {
	out, code, err := p.Client.Try(ctx, p.name(), "sudo", "-u", Player, "env",
		"HOME=/home/"+Player, "python3", "-c", steamScan)
	if err != nil {
		return nil, err
	}

	if code != 0 {
		return nil, nil
	}

	var found []struct {
		AppID string `json:"appid"`
		Name  string `json:"name"`
		Cover string `json:"cover"`
	}

	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &found); err != nil {
		return nil, nil // an unreadable answer is not worth failing a start over
	}

	var games []Game

	for _, f := range found {
		if steamTool(f.AppID, f.Name) {
			continue
		}

		games = append(games, Game{
			Name:   f.Name,
			Launch: "steam steam://rungameid/" + f.AppID,
			Image:  f.Cover,
			Steam:  f.AppID,
		})
	}

	return games, nil
}

// lutrisGames lists what Lutris has installed.
//
// Through Lutris itself rather than by reading its database, because the
// database is its own business and the command is documented. It needs a
// display even to print a list, since it is one GTK application either way,
// which is part of why the whole scan is on a slow timer.
func (p *Provisioner) lutrisGames(ctx context.Context) ([]Game, error) {
	out, code, err := p.Client.Try(ctx, p.name(), "sudo", "-u", Player, "env",
		"HOME=/home/"+Player,
		"DISPLAY=:0",
		fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", p.uid),
		"lutris", "--list-games", "--installed", "--json")
	if err != nil {
		return nil, err
	}

	if code != 0 {
		return nil, nil
	}

	// Lutris prints warnings before the JSON, so take the document rather than
	// the output.
	start := strings.Index(out, "[")
	if start < 0 {
		return nil, nil
	}

	var found []struct {
		ID        int    `json:"id"`
		Name      string `json:"name"`
		CoverPath string `json:"coverPath"`
	}

	if err := json.Unmarshal([]byte(out[start:]), &found); err != nil {
		return nil, nil
	}

	var games []Game

	for _, f := range found {
		if f.Name == "" {
			continue
		}

		games = append(games, Game{
			Name:   f.Name,
			Launch: fmt.Sprintf("lutris lutris:rungameid/%d", f.ID),
			Image:  f.CoverPath,
		})
	}

	return games, nil
}

// installedGames is everything a seat can start directly, from every launcher
// that can be asked.
func (p *Provisioner) installedGames(ctx context.Context) []Game {
	var games []Game

	steam, err := p.steamGames(ctx)
	if err != nil {
		p.Log("! Steam's installed games could not be read: %v", err)
	}

	games = append(games, steam...)

	lutris, err := p.lutrisGames(ctx)
	if err != nil {
		p.Log("! Lutris could not be asked what it has: %v", err)
	}

	games = append(games, lutris...)

	games = dedupeGames(games)

	sort.Slice(games, func(i, j int) bool { return games[i].Name < games[j].Name })

	if len(games) > maxGames {
		p.Log("! %d games installed, offering the first %d by name",
			len(games), maxGames)

		games = games[:maxGames]
	}

	return games
}

// dedupeGames drops repeats by name.
//
// The same game reaches a seat twice often enough to matter: through Steam and
// again through Lutris, which can be pointed at a Steam installation. Two
// entries with the same name in Moonlight is a coin toss over which one works.
func dedupeGames(games []Game) []Game {
	seen := map[string]bool{}
	out := games[:0]

	for _, g := range games {
		key := strings.ToLower(strings.TrimSpace(g.Name))
		if key == "" || seen[key] {
			continue
		}

		seen[key] = true

		out = append(out, g)
	}

	return out
}
