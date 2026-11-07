package seat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Carrying Heroic's games between the members.
//
// The same thing faugusshare.go does for Faugus, through the same rounds in
// share.go. Heroic installs Epic, GOG and Amazon games, each through a tool of
// its own that keeps a list of what it installed, and calls a game installed
// when its list names a directory for it. The pool moves the directory, so the
// member whose Heroic installed the game is read for a recipe, and wherever the
// folder arrives an entry is written into the same list, as that member's own
// person.
//
// Unlike the other two, the entry cannot be renamed: the store looks a game up
// by its own id in the account somebody signed in with, so the receiver's
// entry has the same id as the original, and the helper keeps a list of the
// ones it wrote to tell them apart. Signing in is by hand in every member, with
// that member's account: a licence is somebody's.
//
// Heroic installs into ~/Games/Heroic until told otherwise, which is no shared
// folder. Before every scan the helper points it at the member's shared
// directory instead, unless somebody chose another directory, so a game
// installed from then on is shared from its first byte. One installed before
// is moved with the Share button on the host, like a Lutris or Faugus game.

// heroicShareFile is where the recipes and the record of written entries are
// kept, beside the seat records.
const heroicShareFile = "heroic-share.json"

// heroicShareScript is the helper, run with python3 -I as the member's person.
var heroicShareScript = string(asset("assets/heroicshare.py"))

// heroicShares is the book of Heroic entries carried between the members.
type heroicShares = shareBook[heroicRecipe, heroicEntry]

// heroicKey is what the helper calls a game: the store's tool and the store's
// own id for it.
var heroicKey = regexp.MustCompile(`^(legendary|gog|nile):[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func openHeroicShares(path string) (*heroicShares, error) {
	return openShares[heroicRecipe, heroicEntry](path, shareLauncher{
		name:   "Heroic",
		script: heroicShareScript,
		// The three lists, one per store.
		stamp: []string{
			".config/heroic/legendaryConfig/legendary/installed.json",
			".config/heroic/gog_store/installed.json",
			".config/heroic/nile_config/nile/installed.json",
		},
		validKey: heroicKey.MatchString,
		prepare:  "install-path",
	})
}

// heroicRecipe is how one shared folder is an installed game, as the member
// whose Heroic installed it had it.
type heroicRecipe struct {
	Runner string `json:"runner"`
	App    string `json:"app"`
	Title  string `json:"title"`

	// Entry is the store's entry for the game, the keys worth carrying and no
	// path, as canonical JSON: a string, so that the recipe stays comparable.
	Entry string `json:"entry"`

	// From is the member it was read from, which is never written to from it.
	From string `json:"from"`
}

func (r heroicRecipe) author() string { return r.From }

func (r heroicRecipe) authored(from string) heroicRecipe {
	r.From = from

	return r
}

// heroicEntry is one game in a member's Heroic as the helper reports it.
type heroicEntry struct {
	Key    string `json:"key"`
	Runner string `json:"runner"`
	App    string `json:"app"`
	Title  string `json:"title"`
	Ours   bool   `json:"ours"`

	Folder     string `json:"folder"`
	HomeFolder string `json:"home_folder"`

	// Entry is set only for a game that is a shared folder, see describe.
	Entry string `json:"entry"`
}

func (e heroicEntry) key() string                  { return e.Key }
func (e heroicEntry) title() string                { return e.Title }
func (e heroicEntry) place() (folder, home string) { return e.Folder, e.HomeFolder }
func (e heroicEntry) written() bool                { return e.Ours }

// heroicKeep is what an entry may carry per store, mirroring KEEP in the
// helper. Checked here as well because a recipe can come out of any seat and
// goes into somebody else's Heroic.
var heroicKeep = map[string]map[string]bool{
	"legendary": keySet("app_name", "title", "version", "executable", "platform",
		"install_size", "is_dlc", "can_run_offline", "requires_ot",
		"launch_parameters", "install_tags", "base_urls", "egl_guid",
		"needs_verification"),
	"gog": keySet("appName", "platform", "executable", "install_size", "is_dlc",
		"version", "installedWith", "language", "versionEtag", "buildId",
		"pinnedVersion"),
	"nile": keySet("id", "version", "size"),
}

func keySet(keys ...string) map[string]bool {
	out := make(map[string]bool, len(keys))
	for _, k := range keys {
		out[k] = true
	}

	return out
}

// recipe checks an entry and makes a recipe of it.
func (e heroicEntry) recipe(from string) (heroicRecipe, error) {
	keep, ok := heroicKeep[e.Runner]

	switch {
	case !ok:
		return heroicRecipe{}, fmt.Errorf("store tool %q", e.Runner)
	case e.Key != e.Runner+":"+e.App || !heroicKey.MatchString(e.Key):
		return heroicRecipe{}, fmt.Errorf("game %q", e.Key)
	case !plainText(e.Title, 200) || strings.TrimSpace(e.Title) == "":
		return heroicRecipe{}, errors.New("title")
	case e.Entry == "" || len(e.Entry) > 16<<10:
		return heroicRecipe{}, errors.New("entry")
	}

	var fields map[string]any
	if err := json.Unmarshal([]byte(e.Entry), &fields); err != nil {
		return heroicRecipe{}, fmt.Errorf("entry: %w", err)
	}

	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, k := range keys {
		if !keep[k] {
			return heroicRecipe{}, fmt.Errorf("entry carries %q", k)
		}

		if !heroicValue(fields[k]) {
			return heroicRecipe{}, fmt.Errorf("entry's %q", k)
		}
	}

	// The program is named relative to the game's directory, and the store's
	// tool starts whatever that resolves to: never anything outside it.
	if exe, ok := fields["executable"].(string); ok && exe != "" &&
		!insideFolder(strings.ReplaceAll(exe, `\`, "/"), false) {
		return heroicRecipe{}, fmt.Errorf("program %q", exe)
	}

	canon, err := json.Marshal(fields)
	if err != nil {
		return heroicRecipe{}, err
	}

	return heroicRecipe{
		Runner: e.Runner, App: e.App, Title: e.Title, Entry: string(canon),
		From: from,
	}, nil
}

// heroicValue is what a carried key may hold: nothing, a plain value, or a
// list of short strings. Nothing that names a path is among the keys, and no
// value is long enough to be one in disguise.
func heroicValue(v any) bool {
	switch x := v.(type) {
	case nil, bool, float64:
		return true
	case string:
		return plainText(x, 500)
	case []any:
		if len(x) > 64 {
			return false
		}

		for _, item := range x {
			s, ok := item.(string)
			if !ok || !plainText(s, 500) {
				return false
			}
		}

		return true
	}

	return false
}

// ShareHostHeroicGame moves one game of the host's Heroic into ~/Games/shared,
// see shareHost.
func (m *Manager) ShareHostHeroicGame(ctx context.Context, key string) error {
	if m.heroic == nil {
		return fmt.Errorf("%w: %s", ErrNoLibrary, m.libraryErr)
	}

	return m.heroic.shareHost(ctx, m, key)
}
