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

// DropLibraryFolder takes a library folder back out, reporting whether anything
// changed.
//
// The counterpart of MergeLibraryFolder, and it exists because the shared
// library moved. It used to be registered as a second folder next to Steam's
// own; it is now mounted as Steam's own, so the old entry points at the same
// files through a second path. Left in place it would show every shared game
// twice and offer two indistinguishable destinations in the install dialog,
// which is the thing this was meant to get rid of.
//
// The remaining folders are renumbered. Steam reads them as "0", "1", "2" and
// stops at the first number that is missing, so removing the middle entry of
// three without renumbering would silently unregister the last one, which may
// well be a folder somebody added themselves.
func DropLibraryFolder(existing []byte, path string) ([]byte, bool) {
	if !bytes.Contains(existing, []byte(`"`+path+`"`)) {
		return existing, false
	}

	lines := strings.Split(string(existing), "\n")

	var (
		out     []string
		blocks  int
		dropped bool
		i       int
	)

	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])

		// A numbered entry, which is the only thing this touches. Anything else,
		// including the header and the outermost braces, passes through.
		if _, err := strconv.Atoi(strings.Trim(trimmed, `"`)); err != nil ||
			!strings.HasPrefix(trimmed, `"`) ||
			i+1 >= len(lines) || strings.TrimSpace(lines[i+1]) != "{" {
			out = append(out, lines[i])
			i++

			continue
		}

		// The whole block, brace counted rather than assumed to be a fixed
		// number of lines: Steam writes an apps list inside it whose length is
		// the number of games installed there.
		depth, j := 0, i+1

		for j < len(lines) {
			depth += strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
			j++

			if depth == 0 {
				break
			}
		}

		block := strings.Join(lines[i:j], "\n")

		if strings.Contains(block, `"`+path+`"`) {
			dropped = true
			i = j

			continue
		}

		// Renumbered as it is kept, so the survivors stay contiguous.
		indent := lines[i][:len(lines[i])-len(strings.TrimLeft(lines[i], " \t"))]
		out = append(out, indent+`"`+strconv.Itoa(blocks)+`"`)
		out = append(out, lines[i+1:j]...)
		blocks++
		i = j
	}

	if !dropped {
		return existing, false
	}

	return []byte(strings.Join(out, "\n")), true
}
