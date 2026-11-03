package seat

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// Carrying Lutris entries between the members.
//
// The pool moves a folder and nothing else, and for Steam that is enough: the
// appmanifest travels inside the library and Steam reads the library itself.
// Lutris keeps what it knows about a game outside the folder, as a row in
// pga.db and a YAML under ~/.config/lutris/games, so a folder that reached a
// seat was files that seat's Lutris had never heard of. The answer until now
// was that registering it is the folder's own job, which meant a hand written
// polyseat-setup.sh per game, and in practice that a game installed through
// Lutris did not appear anywhere else.
//
// So the daemon carries the entry. The member whose Lutris installed the game
// has one, and from it the daemon keeps a recipe: name, slug, runner, the
// program to start relative to the folder, and the few settings that say how.
// Wherever the folder arrives and nothing in that Lutris points into it, an
// entry is written from the recipe, as that member's own person.
//
// What is carried is a short list, see KEEP in lutrisshare.py. Lutris also
// runs commands around a launch and hands the game an environment, and neither
// travels: a folder can come from any seat, and the entry written here is one
// somebody else will start. The paths are held to the folder, so an entry
// written from a recipe starts the folder's own files and nothing else, which
// is exactly what a Steam game from the pool does. Nothing runs when an entry
// is written; that is why this needs no approval and polyseat-setup.sh does.
//
// A folder that carries a polyseat-setup.sh is left to it. Those exist because
// a game needed more than an entry, and the script writes its own.

// lutrisShareFile is where the recipes and the record of written entries are
// kept, beside the seat records.
const lutrisShareFile = "lutris-share.json"

// lutrisSuffix is what the helper names its own entries by; see SUFFIX there.
const lutrisSuffix = shareSuffix

// lutrisShareScript is the helper, run with python3 -I as the member's person.
var lutrisShareScript = string(asset("assets/lutrisshare.py"))

// lutrisShares is the book of Lutris entries carried between the members.
type lutrisShares = shareBook[lutrisRecipe, lutrisEntry]

func openLutrisShares(path string) (*lutrisShares, error) {
	return openShares[lutrisRecipe, lutrisEntry](path, shareLauncher{
		name:   "Lutris",
		script: lutrisShareScript,
		// Its database and the directory of game configurations.
		stamp:    []string{".local/share/lutris/pga.db", ".config/lutris/games"},
		validKey: lutrisSlug.MatchString,
	})
}

// lutrisRecipe is how one shared folder is started, as the member that
// installed it had it.
type lutrisRecipe struct {
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Runner   string `json:"runner"`
	Platform string `json:"platform,omitempty"`

	// Exe, Prefix and WorkingDir are relative to the folder. Prefix and
	// WorkingDir may be "." for the folder itself, and are empty when the
	// original pointed outside it.
	Exe        string `json:"exe"`
	Prefix     string `json:"prefix,omitempty"`
	WorkingDir string `json:"working_dir,omitempty"`

	Args        string `json:"args,omitempty"`
	Arch        string `json:"arch,omitempty"`
	WineVersion string `json:"wine_version,omitempty"`

	// From is the member it was read from, which is never written to from it.
	From string `json:"from"`
}

func (r lutrisRecipe) author() string { return r.From }

func (r lutrisRecipe) authored(from string) lutrisRecipe {
	r.From = from

	return r
}

// lutrisEntry is one game in a member's Lutris as the helper reports it.
type lutrisEntry struct {
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	Runner     string `json:"runner"`
	Platform   string `json:"platform"`
	Configpath string `json:"configpath"`
	Ours       bool   `json:"ours"`

	// Folder is the shared folder its files are in, HomeFolder the directory
	// below the games root they are in, either empty when they are not.
	Folder     string `json:"folder"`
	HomeFolder string `json:"home_folder"`

	Exe         string `json:"exe"`
	Prefix      string `json:"prefix"`
	WorkingDir  string `json:"working_dir"`
	Args        string `json:"args"`
	Arch        string `json:"arch"`
	WineVersion string `json:"wine_version"`
}

func (e lutrisEntry) key() string                  { return e.Slug }
func (e lutrisEntry) title() string                { return e.Name }
func (e lutrisEntry) place() (folder, home string) { return e.Folder, e.HomeFolder }
func (e lutrisEntry) written() bool                { return e.Ours }

// lutrisSlug is what Lutris makes of a name: lower case, digits and dashes.
var lutrisSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)

// recipe checks an entry and makes a recipe of it.
//
// Every field is held to what Lutris itself would write, because what is read
// here can come out of any seat and goes into somebody else's Lutris. The paths
// above all: a relative path that stays inside the folder is the only kind
// accepted, so an entry made from it can start nothing but the folder's files.
func (e lutrisEntry) recipe(from string) (lutrisRecipe, error) {
	switch {
	case !lutrisSlug.MatchString(e.Slug):
		return lutrisRecipe{}, fmt.Errorf("slug %q", e.Slug)
	case strings.HasSuffix(e.Slug, lutrisSuffix):
		return lutrisRecipe{}, fmt.Errorf("slug %q is the daemon's own", e.Slug)
	case e.Runner != "wine" && e.Runner != "linux":
		return lutrisRecipe{}, fmt.Errorf("runner %q", e.Runner)
	case !plainText(e.Name, 200) || strings.TrimSpace(e.Name) == "":
		return lutrisRecipe{}, errors.New("name")
	case !insideFolder(e.Exe, false):
		return lutrisRecipe{}, fmt.Errorf("exe %q", e.Exe)
	case e.Prefix != "" && !insideFolder(e.Prefix, true):
		return lutrisRecipe{}, fmt.Errorf("prefix %q", e.Prefix)
	case e.WorkingDir != "" && !insideFolder(e.WorkingDir, true):
		return lutrisRecipe{}, fmt.Errorf("working_dir %q", e.WorkingDir)
	case !plainText(e.Args, 1000), !plainText(e.Platform, 100), !plainText(e.WineVersion, 200):
		return lutrisRecipe{}, errors.New("a field with control characters or too long")
	}

	switch e.Arch {
	case "", "auto", "win32", "win64":
	default:
		return lutrisRecipe{}, fmt.Errorf("arch %q", e.Arch)
	}

	return lutrisRecipe{
		Name: e.Name, Slug: e.Slug, Runner: e.Runner, Platform: e.Platform,
		Exe: e.Exe, Prefix: e.Prefix, WorkingDir: e.WorkingDir,
		Args: e.Args, Arch: e.Arch, WineVersion: e.WineVersion,
		From: from,
	}, nil
}

// insideFolder reports whether a path relative to a folder stays in it. dot
// is whether the folder itself counts, which it does for a directory and not
// for the program to start.
func insideFolder(rel string, dot bool) bool {
	if rel == "." {
		return dot
	}

	return rel != "" && filepath.IsLocal(rel) && plainText(rel, 4096)
}

// plainText reports whether s is short enough and has no control characters,
// which a YAML value or a path in somebody's configuration has no use for.
func plainText(s string, limit int) bool {
	if len(s) > limit {
		return false
	}

	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}

	return true
}

// ShareHostLutrisGame moves one game of the host's Lutris into ~/Games/shared,
// see shareHost.
func (m *Manager) ShareHostLutrisGame(ctx context.Context, slug string) error {
	if m.lutris == nil {
		return fmt.Errorf("%w: %s", ErrNoLibrary, m.libraryErr)
	}

	return m.lutris.shareHost(ctx, m, slug)
}
