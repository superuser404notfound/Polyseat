package seat

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Carrying Faugus entries between the members.
//
// The same thing lutrisshare.go does for Lutris, through the same rounds in
// share.go. Faugus keeps a game as one entry in games.json, outside the
// folder, so a folder that reached a seat was files its Faugus had never heard
// of. The member whose Faugus has the game is read for a recipe, and wherever
// the folder arrives an entry is written from it, as that member's own person,
// under the game's id with the daemon's suffix.
//
// What is carried is shorter than what Faugus keeps, see describe in
// faugusshare.py. Faugus puts launch arguments in front of the game and runs
// commands before and after it, and none of that travels, for the reason
// lutrisshare.go gives: the entry written here is one somebody else starts.
// What does travel is held to what Faugus itself would write, because Faugus
// reads a word of the form NAME=value anywhere in the command it builds as a
// variable for the game's environment.
//
// The prefix stays behind when it is outside the folder, which is how Faugus
// makes them: ~/Faugus/<game>. The receiver gets one of its own in its own
// place, and so its own saves, see private.go for why that matters.
//
// Store launchers are the one kind of game whose prefix cannot stay behind
// for a new one: the EA App or Ubisoft Connect is installed and signed in in
// it, and the game will not start without them. Their games are carried
// anyway, by making the place a store installs its games in a link to the
// member's shared folders, see link_store in faugusshare.py. A game the store
// installs is then a shared folder like any other, the store finds it where
// it put it, and a receiver that has the same store gets an entry in its own
// prefix of that store. The store itself is installed and signed in to by
// hand in every member, with that member's account: a licence is somebody's.

// faugusShareFile is where the recipes and the record of written entries are
// kept, beside the seat records.
const faugusShareFile = "faugus-share.json"

// faugusShareScript is the helper, run with python3 -I as the member's person.
var faugusShareScript = string(asset("assets/faugusshare.py"))

// faugusShares is the book of Faugus entries carried between the members.
type faugusShares = shareBook[faugusRecipe, faugusEntry]

func openFaugusShares(path string) (*faugusShares, error) {
	return openShares[faugusRecipe, faugusEntry](path, shareLauncher{
		name:   "Faugus",
		script: faugusShareScript,
		// Everything Faugus knows about its games is this one file.
		stamp:    []string{".local/share/faugus-launcher/games.json"},
		validKey: faugusID.MatchString,
		prepare:  "stores",
	})
}

// faugusRecipe is how one shared folder is started, as the member that added
// it to Faugus had it.
type faugusRecipe struct {
	GameID string `json:"gameid"`
	Title  string `json:"title"`

	// Exe and Prefix are relative to the folder. Prefix may be "." for the
	// folder itself, and is empty when the original was outside it.
	Exe    string `json:"exe"`
	Prefix string `json:"prefix,omitempty"`

	// Runner is the original's, with Faugus's "Default" already resolved to
	// what that member's default was. The receiver picks its nearest one.
	Runner     string `json:"runner,omitempty"`
	Protonfix  string `json:"protonfix,omitempty"`
	GameArgs   string `json:"game_arguments,omitempty"`
	Runtime    string `json:"runtime,omitempty"`
	DisableUMU bool   `json:"disable_umu,omitempty"`

	// Store names the store launcher that installed the game, see STORES in
	// faugusshare.py. The receiver starts it in its own prefix of that store,
	// so Prefix is always empty then.
	Store string `json:"store,omitempty"`

	// From is the member it was read from, which is never written to from it.
	From string `json:"from"`
}

func (r faugusRecipe) author() string { return r.From }

func (r faugusRecipe) authored(from string) faugusRecipe {
	r.From = from

	return r
}

// faugusEntry is one game in a member's Faugus as the helper reports it.
type faugusEntry struct {
	GameID string `json:"gameid"`
	Title  string `json:"title"`
	Ours   bool   `json:"ours"`

	// Folder is the shared folder the program is in, HomeFolder the directory
	// below the games root it is in, either empty when it is not.
	Folder     string `json:"folder"`
	HomeFolder string `json:"home_folder"`

	Exe        string `json:"exe"`
	Prefix     string `json:"prefix"`
	Runner     string `json:"runner"`
	Protonfix  string `json:"protonfix"`
	GameArgs   string `json:"game_arguments"`
	Runtime    string `json:"runtime"`
	DisableUMU bool   `json:"disable_umu"`
	Store      string `json:"store"`
}

func (e faugusEntry) key() string                  { return e.GameID }
func (e faugusEntry) title() string                { return e.Title }
func (e faugusEntry) place() (folder, home string) { return e.Folder, e.HomeFolder }
func (e faugusEntry) written() bool                { return e.Ours }

// faugusRunner is what a runner may be called: one of Faugus's own names, or
// the name or path of a Proton build. No "=" and no quote, see the top of this
// file, and not "Steam", whose games are no folder.
var faugusRunner = regexp.MustCompile(`^/?[A-Za-z0-9 ._()+-]+(/[A-Za-z0-9 ._()+-]+)*$`)

// faugusProtonfix is the id umu looks its fixes up by, umu-1234 or a store's.
var faugusProtonfix = regexp.MustCompile(`^[A-Za-z0-9_-]{0,64}$`)

// faugusArg is one word of a game's arguments. A word with "=" in it has to
// start with a dash, which no variable name does.
var faugusArg = regexp.MustCompile(`^(-[A-Za-z0-9_./:+,@%=-]*|[A-Za-z0-9_./:+,@%][A-Za-z0-9_./:+,@%-]*)$`)

// faugusStores are the store launchers whose games are carried, by the names
// faugusshare.py gives them, and "" for a game that is no store's.
var faugusStores = map[string]bool{"": true, "ea": true, "ubisoft": true}

// faugusRuntimes are the Steam runtimes a native game can be started in.
var faugusRuntimes = map[string]bool{
	"": true, "umu-steamrt4": true, "umu-sniper": true, "umu-soldier": true,
	"umu-scout": true, "umu-host": true,
}

// recipe checks an entry and makes a recipe of it, for the same reason the
// Lutris one does: what is read here can come out of any seat and goes into
// somebody else's Faugus.
func (e faugusEntry) recipe(from string) (faugusRecipe, error) {
	switch {
	case !faugusID.MatchString(e.GameID):
		return faugusRecipe{}, fmt.Errorf("game id %q", e.GameID)
	case strings.HasSuffix(e.GameID, shareSuffix):
		return faugusRecipe{}, fmt.Errorf("game id %q is the daemon's own", e.GameID)
	case !plainText(e.Title, 200) || strings.TrimSpace(e.Title) == "":
		return faugusRecipe{}, errors.New("title")
	case !insideFolder(e.Exe, false):
		return faugusRecipe{}, fmt.Errorf("path %q", e.Exe)
	case e.Prefix != "" && !insideFolder(e.Prefix, true):
		return faugusRecipe{}, fmt.Errorf("prefix %q", e.Prefix)
	case e.Runner == "Steam" || (e.Runner != "" && (len(e.Runner) > 200 || !faugusRunner.MatchString(e.Runner))):
		return faugusRecipe{}, fmt.Errorf("runner %q", e.Runner)
	case !faugusProtonfix.MatchString(e.Protonfix):
		return faugusRecipe{}, fmt.Errorf("protonfix %q", e.Protonfix)
	case !faugusRuntimes[e.Runtime]:
		return faugusRecipe{}, fmt.Errorf("runtime %q", e.Runtime)
	case !faugusArgs(e.GameArgs):
		return faugusRecipe{}, fmt.Errorf("arguments %q", e.GameArgs)
	case !faugusStores[e.Store]:
		return faugusRecipe{}, fmt.Errorf("store %q", e.Store)
	case e.Store != "" && e.Prefix != "":
		return faugusRecipe{}, fmt.Errorf("a store's game with a prefix of its own, %q", e.Prefix)
	}

	return faugusRecipe{
		GameID: e.GameID, Title: e.Title, Exe: e.Exe, Prefix: e.Prefix,
		Runner: e.Runner, Protonfix: e.Protonfix, GameArgs: e.GameArgs,
		Runtime: e.Runtime, DisableUMU: e.DisableUMU, Store: e.Store,
		From: from,
	}, nil
}

// faugusArgs reports whether a game's arguments are words Faugus passes to
// the game and nothing it would take for itself.
func faugusArgs(args string) bool {
	if len(args) > 1000 {
		return false
	}

	for _, word := range strings.Fields(args) {
		if !faugusArg.MatchString(word) {
			return false
		}
	}

	return true
}

// ShareHostFaugusGame moves one game of the host's Faugus into ~/Games/shared,
// see shareHost.
func (m *Manager) ShareHostFaugusGame(ctx context.Context, gameid string) error {
	if m.faugus == nil {
		return fmt.Errorf("%w: %s", ErrNoLibrary, m.libraryErr)
	}

	return m.faugus.shareHost(ctx, m, gameid)
}
