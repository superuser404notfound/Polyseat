package library

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// StateInstalled is the value of StateFlags for a game that is fully installed
// with nothing pending.
//
// Steam uses this field as a bitmask covering update required, downloading,
// staging, validating and a dozen other transient conditions. Exactly 4 is the
// only value that means the files on disk are the complete, current game. This
// is what keeps the daemon from harvesting a half finished download: a game
// being installed sits at 1026 or similar until the last byte lands.
const StateInstalled = 4

// App is one entry in a Steam library, read from its appmanifest.
type App struct {
	AppID      string `json:"appid"`
	Name       string `json:"name"`
	InstallDir string `json:"installdir"`
	BuildID    string `json:"buildid"`
	StateFlags int    `json:"state_flags"`
	SizeOnDisk int64  `json:"size_on_disk"`

	// Manifest is the file the app was read from, kept so it can be copied
	// verbatim rather than regenerated.
	Manifest string `json:"-"`
}

// Installed reports whether the app is complete and current.
func (a App) Installed() bool {
	return a.StateFlags == StateInstalled && a.InstallDir != "" && a.AppID != ""
}

// ManifestName is the file name Steam uses for an app's manifest.
func ManifestName(appID string) string {
	return "appmanifest_" + appID + ".acf"
}

// Newer reports whether a is a later build than b.
//
// Compared as numbers, because Steam's build ids are increasing integers and
// comparing them as text puts build 9 after build 10. Where either side is not
// a number, the answer is no: an unknown build is not grounds for replacing
// files somebody is playing.
//
// The direction matters more than it looks. The first version of the pool took
// a copy from a seat whenever the build differed at all, in either direction,
// so a seat that was one patch behind would quietly overwrite the pool's newer
// copy and hand that older build to everybody else.
func Newer(a, b string) bool {
	x, err := strconv.ParseInt(a, 10, 64)
	if err != nil {
		return false
	}

	y, err := strconv.ParseInt(b, 10, 64)
	if err != nil {
		return false
	}

	return x > y
}

// ReadApps lists the apps a steamapps directory declares.
//
// Errors on individual manifests are skipped rather than returned. Steam
// writes these files while it works and a torn read is normal; failing the
// whole scan because one file was caught mid-write would make the sync loop
// stop for a reason that fixes itself a second later.
func ReadApps(steamapps string) ([]App, error) {
	matches, err := filepath.Glob(filepath.Join(steamapps, "appmanifest_*.acf"))
	if err != nil {
		return nil, err
	}

	var out []App

	for _, path := range matches {
		app, err := ReadApp(path)
		if err != nil {
			continue
		}

		out = append(out, app)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out, nil
}

// ReadApp parses one appmanifest.
func ReadApp(path string) (App, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return App{}, err
	}

	app, err := parseManifest(data)
	if err != nil {
		return App{}, fmt.Errorf("%s: %w", path, err)
	}

	app.Manifest = path

	return app, nil
}

// parseManifest reads the top level fields of an AppState block.
//
// A deliberately small reader rather than a general VDF parser. The format is
// nested quoted key/value pairs with braces, and the daemon needs six scalars
// out of the outermost block. Descending into InstalledDepots or UserConfig
// would only create a chance of picking up a key of the same name from an inner
// block, so depth is tracked and anything below the top level is ignored.
func parseManifest(data []byte) (App, error) {
	var (
		app   App
		depth int
	)

	fields := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))

	// Manifests for large games carry a depot list that can exceed the default
	// 64 KiB token size across a single line only rarely, but the buffer is
	// cheap and a truncated scan would silently lose fields.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		switch line {
		case "{":
			depth++

			continue

		case "}":
			depth--

			continue
		}

		key, value, ok := parsePair(line)
		if !ok {
			continue
		}

		// depth 1 is inside the AppState block, which is where the fields live.
		// depth 0 is the block's own name, and anything deeper is a subsection.
		if depth != 1 {
			continue
		}

		fields[strings.ToLower(key)] = value
	}

	if err := scanner.Err(); err != nil {
		return app, err
	}

	if len(fields) == 0 {
		return app, fmt.Errorf("no fields found, this does not look like an appmanifest")
	}

	app.AppID = fields["appid"]
	app.Name = fields["name"]
	app.InstallDir = fields["installdir"]
	app.BuildID = fields["buildid"]
	app.StateFlags, _ = strconv.Atoi(fields["stateflags"])
	app.SizeOnDisk, _ = strconv.ParseInt(fields["sizeondisk"], 10, 64)

	if app.AppID == "" {
		return app, fmt.Errorf("the manifest has no appid")
	}

	// The install directory is joined onto a path and written to as root, and
	// it comes out of a file rather than out of the daemon.
	if app.InstallDir != "" {
		if err := safeName(app.InstallDir); err != nil {
			return app, fmt.Errorf("installdir: %w", err)
		}
	}

	if err := safeName(ManifestName(app.AppID)); err != nil {
		return app, fmt.Errorf("appid: %w", err)
	}

	return app, nil
}

// parsePair splits a `"key"<tab>"value"` line.
func parsePair(line string) (key, value string, ok bool) {
	if !strings.HasPrefix(line, `"`) {
		return "", "", false
	}

	rest := line[1:]

	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return "", "", false
	}

	key = rest[:end]
	rest = strings.TrimSpace(rest[end+1:])

	// A key on its own is the name of the block that follows, not a pair.
	if !strings.HasPrefix(rest, `"`) {
		return "", "", false
	}

	rest = rest[1:]

	end = strings.IndexByte(rest, '"')
	if end < 0 {
		return "", "", false
	}

	return key, rest[:end], true
}

// neutralised are the fields rewritten when a manifest is handed to another
// seat, with the value each is reset to.
//
// LastOwner is a SteamID64. Copied as it stands, every seat would claim the
// account that first installed the game, and Steam uses that field when it
// decides whose license covers an install. Setting it to zero makes the local
// client fill it in with whoever is actually signed in, which is the truth.
//
// LastPlayed is not a correctness problem, only a privacy one: without this,
// everyone in the house can see when everyone else last played something.
var neutralised = map[string]string{
	"lastowner":  "0",
	"lastplayed": "0",
}

// Rewrite returns a manifest with the fields that belong to one account
// neutralised, leaving everything else untouched.
//
// Rewritten line by line rather than regenerated from the parsed fields,
// because the parts this package does not model are the parts that matter most
// to Steam. InstalledDepots carries the manifest id of every depot on disk, and
// it is what lets the receiving client conclude the files are current instead
// of downloading the game again. Reconstructing the file from six scalars would
// throw that away and defeat the entire feature.
func Rewrite(data []byte) []byte {
	var out bytes.Buffer

	depth := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		switch trimmed {
		case "{":
			depth++

		case "}":
			depth--

		default:
			if key, _, ok := parsePair(trimmed); ok && depth == 1 {
				if replacement, found := neutralised[strings.ToLower(key)]; found {
					indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
					line = indent + `"` + key + `"` + "\t\t" + `"` + replacement + `"`
				}
			}
		}

		out.WriteString(line)
		out.WriteByte('\n')
	}

	return out.Bytes()
}

// libraryFolders is the file that tells Steam where else to look for games.
//
// Written by the daemon rather than left to the user to add through Steam's
// own dialog. A seat is meant to come up with its shared library already
// present; making somebody open the settings in a streamed session and browse
// to a path would be a poor first five minutes.
const libraryFoldersTemplate = `"libraryfolders"
{
	"0"
	{
		"path"		"%s"
		"label"		""
		"contentid"		"0"
		"totalsize"		"0"
		"update_clean_bytes_tally"		"0"
		"time_last_update_verified"		"0"
		"apps"
		{
		}
	}
	"1"
	{
		"path"		"%s"
		"label"		"Polyseat"
		"contentid"		"0"
		"totalsize"		"0"
		"update_clean_bytes_tally"		"0"
		"time_last_update_verified"		"0"
		"apps"
		{
		}
	}
}
`

// LibraryFolders renders the file registering the shared library alongside
// Steam's own.
func LibraryFolders(steamRoot, shared string) []byte {
	return []byte(fmt.Sprintf(libraryFoldersTemplate, steamRoot, shared))
}

// folderEntry is one library folder as Steam writes it. Deliberately sparse:
// Steam fills in the sizes and the timestamps itself on the next start, and
// inventing values for them would only be a lie it has to correct.
const folderEntry = `	"%d"
	{
		"path"		"%s"
		"label"		"Polyseat"
		"contentid"		"0"
		"totalsize"		"0"
		"update_clean_bytes_tally"		"0"
		"time_last_update_verified"		"0"
		"apps"
		{
		}
	}
`

// MergeLibraryFolder adds a library folder to a file Steam already wrote,
// reporting whether anything changed.
//
// Merging rather than replacing, and this is the difference between the feature
// working and the feature working provided somebody also opens Steam's settings
// and browses to a path. A seat that has run Steam once already has this file,
// and the first version of this code refused to touch it, which left every seat
// but a brand new one needing that manual step.
//
// Replacing it outright is not an option either: the file is Steam's record of
// every library folder it knows, including any the person added themselves, and
// overwriting it would quietly unregister them.
func MergeLibraryFolder(existing []byte, path string) ([]byte, bool) {
	if bytes.Contains(existing, []byte(`"`+path+`"`)) {
		return existing, false
	}

	lines := strings.Split(string(existing), "\n")

	var (
		depth  int
		next   int
		closer = -1
	)

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		switch trimmed {
		case "{":
			depth++

			continue

		case "}":
			depth--

			// The brace that ends the outermost block is where a new entry has
			// to go in front of. Recorded rather than assumed to be the last
			// line, because Steam leaves a trailing newline and an editor may
			// have left more than one.
			if depth == 0 {
				closer = i
			}

			continue
		}

		// Entries are numbered, and the next one has to follow the highest in
		// use. Reusing a number would replace a folder rather than add one.
		if depth == 1 && strings.HasPrefix(trimmed, `"`) {
			if key, _, ok := parsePair(trimmed); !ok {
				if n, err := strconv.Atoi(strings.Trim(trimmed, `"`)); err == nil && n >= next {
					next = n + 1
				}
			} else {
				_ = key
			}
		}
	}

	if closer < 0 {
		// Not a shape this understands. Leaving it alone is the only safe
		// answer: this file is Steam's, and a guess written into it costs
		// somebody their library list.
		return existing, false
	}

	entry := strings.Split(strings.TrimRight(fmt.Sprintf(folderEntry, next, path), "\n"), "\n")

	merged := make([]string, 0, len(lines)+len(entry))
	merged = append(merged, lines[:closer]...)
	merged = append(merged, entry...)
	merged = append(merged, lines[closer:]...)

	return []byte(strings.Join(merged, "\n")), true
}
