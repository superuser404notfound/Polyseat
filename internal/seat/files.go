package seat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DropDir is where a file uploaded from the interface lands inside a seat.
//
// ~/Downloads rather than a directory of Polyseat's own, because the seat
// already treats it as the place where things arrive: a browser inside the seat
// saves there, and the AppImage sweep adopts what it finds there within a
// minute. An emulator dropped on a seat card therefore reaches Moonlight along
// the path that already exists, instead of adding a second one to keep working.
//
// Not the directory the file is eventually for, which is what somebody asking
// this question usually has in mind: a save belongs somewhere that differs per
// emulator, per install method and per title, and a daemon that writes into
// those has to know all three and be wrong about them later. Downloads is a
// place the player can move things from with the file manager the seat already
// has.
const DropDir = "/home/" + Player + "/Downloads"

// What one uploaded path may look like.
//
// The first two are the filesystem's own limits and not a policy of Polyseat's:
// a component over 255 bytes and a path over 4096 are what Linux refuses.
// Refusing them here means the answer arrives as a sentence in the interface
// rather than as an errno from inside a container, where nothing says which of
// several hundred files it was about.
//
// The depth is this project's own. Nothing a seat is given looks like it, and
// every level of a new folder is a round trip to Incus, so a path that is
// mostly folders is either a mistake or somebody spending the daemon's time on
// purpose.
const (
	maxDropSegment = 255
	maxDropPath    = 4096
	maxDropDepth   = 64
)

// Upload is one file arriving from the interface, still on the wire.
type Upload struct {
	// Path is what the browser called it, relative to DropDir. It carries the
	// folder for a folder upload, which is the whole reason this is a path and
	// not a name: a save is a directory named after a title id and a mod is a
	// tree, and asking somebody to flatten one by hand and rebuild it inside
	// the seat is asking for the step where it goes wrong.
	Path string

	// Body is read exactly once, in order, and is not seekable.
	Body io.Reader
}

// Uploads hands over one file at a time and answers io.EOF when there are none
// left.
//
// An interface rather than a slice of files, because nothing here may be held.
// The parts of a multipart body can only be read in the order they arrive and
// only once, and the point of the whole path is that a file of any size reaches
// the container without this process ever holding it.
type Uploads interface {
	Next() (Upload, error)
}

// Skipped is one file that was not accepted, and why.
//
// Recorded rather than fatal. A folder can carry one name this refuses among
// hundreds that are fine, and losing the whole drop over it would mean
// uploading everything again to find out whether there is a second one.
type Skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Received is what one drop turned into.
type Received struct {
	Files   int       `json:"files"`
	Bytes   int64     `json:"bytes"`
	Skipped []Skipped `json:"skipped"`
}

// ValidateDropPath checks the name a browser gave one uploaded file and returns
// the relative path it may be written to.
//
// This is the only thing between a string chosen elsewhere and a write into a
// container as root, so it refuses rather than repairs. A name that is quietly
// corrected is a file somebody then cannot find, and the two components that
// matter most here, "." and "..", are exactly the ones a repair would have to
// guess the intent of.
func ValidateDropPath(rel string) (string, error) {
	switch {
	case rel == "":
		return "", errors.New("a file with no name cannot be put anywhere")
	case len(rel) > maxDropPath:
		return "", fmt.Errorf("that path is %d bytes long and no path may be longer than %d", len(rel), maxDropPath)
	case !utf8.ValidString(rel):
		return "", errors.New("that file name is not text")
	// The empty component rule below already refuses this, since splitting an
	// absolute path puts an empty string first. It is here for the sentence:
	// "that path has an empty folder name in it" is a true thing to say about
	// /etc/passwd and not a useful one.
	case strings.HasPrefix(rel, "/"):
		return "", errors.New("a file may only be given a name, not a place to go")
	}

	parts := strings.Split(rel, "/")

	if len(parts) > maxDropDepth {
		return "", fmt.Errorf("that path is %d folders deep and no more than %d are accepted", len(parts), maxDropDepth)
	}

	for i, part := range parts {
		switch {
		case part == "":
			return "", fmt.Errorf("%q has an empty folder name in it", rel)
		case part == "." || part == "..":
			return "", fmt.Errorf("%q leads out of the folder uploads go to", rel)
		case len(part) > maxDropSegment:
			return "", fmt.Errorf("%q is longer than the %d bytes a name may have", part, maxDropSegment)
		case strings.ContainsFunc(part, unicode.IsControl):
			return "", fmt.Errorf("%q has control characters in its name", part)
		// Only the first one, which is what appears in the seat's Downloads
		// folder. A player who cannot see what they just uploaded has no way to
		// move it anywhere, while a hidden file deeper inside a game's save
		// tree is somebody else's business and belongs to the tree.
		case i == 0 && strings.HasPrefix(part, "."):
			return "", fmt.Errorf("%q would be hidden in the seat's file manager", part)
		}
	}

	return strings.Join(parts, "/"), nil
}

// filer is the part of the Incus client an upload writes through.
//
// A seam of the same kind as the manager's libraries field, and nil everywhere
// except in a test. What the loop below decides is what a folder of several
// hundred files costs in round trips and what becomes of the one file in it
// whose name the seat refuses, and neither of those needs a container to be
// worth measuring. *incusx.Client is the real one.
type filer interface {
	Status(name string) (string, error)
	FileType(name, path string) (string, error)
	MakeDir(name, path string, mode int, uid, gid int64) error
	PushStream(name, path string, content io.Reader, mode int, uid, gid int64) error
	Exec(ctx context.Context, name string, argv []string, stdin io.Reader, stdout, stderr io.Writer) (int, error)
}

// dropScript writes one file into the seat as the player, from standard input:
// an upload, or a file of the daemon's own that lives in the player's home.
//
// As the player rather than through the Incus file API, and that is a security
// property rather than a matter of ownership. The file API writes as root and
// follows symlinks, and it gives a file it creates to the uid it was asked for:
// a player who made ~/Downloads/x a link to /etc/ld.so.preload and then had
// somebody upload x would own that file, which in a container is root. Written
// by the player, a link leads only where the player could already write.
//
// Into a temporary name first and then moved over the destination. mv replaces
// a symlink standing at the destination rather than writing through it, -T
// refuses a directory standing there instead of moving the file into it, and a
// failed upload leaves nothing half written with the real name on it.
const dropScript = `d=$(dirname -- "$1")
mkdir -p -- "$d" || exit 1
t=$(mktemp -- "$d/.polyseat-write.XXXXXX") || exit 1
if cat > "$t" && chmod 0644 -- "$t" && mv -fT -- "$t" "$1"; then
	exit 0
fi
rm -f -- "$t"
exit 1
`

// playerDrop is the argv that runs dropScript for one destination.
func playerDrop(dest string) []string {
	return []string{"sudo", "-u", Player, "env", "HOME=/home/" + Player,
		"sh", "-c", dropScript, "sh", dest}
}

func (m *Manager) filer() filer {
	if m.files != nil {
		return m.files
	}

	return m.client
}

// Receive writes an upload into a seat's Downloads folder, as the player.
//
// As the player and not as root for the same reason flatpak is installed with
// --user: what the daemon puts there and what the player puts there have to be
// the same files with the same owner, or the person the upload was for cannot
// move it, rename it or delete it. A tree owned by root inside somebody's home
// is the mistake this project has already made once.
//
// Deliberately not an operation in the sense the rest of the manager uses: it
// takes no busy lock. An upload is minutes of somebody else's uplink, and a
// seat that reported itself busy for the length of it could not be provisioned,
// paired or played on meanwhile, for a write into a directory that nothing else
// touches.
func (m *Manager) Receive(name string, files Uploads) (Received, error) {
	// The empty list is a list. A field that turns into null when there is
	// nothing in it is what once stopped the seat list rendering at all, and
	// the interface calls map on this one too.
	got := Received{Skipped: []Skipped{}}

	s, err := m.store.Get(name)
	if err != nil {
		return got, err
	}

	// The same condition the shared library has, and for the same reason:
	// without a recorded uid there is nothing to own the files with, and
	// learning it means running a command inside a container that may be off.
	// Provisioning records it, and the interface already marks a seat that
	// predates that as stale.
	if s.PlayerUID == 0 {
		return got, errors.New("this seat has no recorded uid yet, provision it once")
	}

	into := m.filer()

	status, err := into.Status(name)
	if err != nil {
		return got, err
	}

	if status == "" {
		return got, fmt.Errorf("%s has no container yet", name)
	}

	// A seat that is switched off is deliberately allowed. Incus mounts the
	// volume for the file API whether or not the container runs, and it says so
	// in its own source, which creates the runtime directory "in case the
	// instance has never been started before". Loading a seat up before turning
	// it on is a reasonable thing to want, and refusing it here would be
	// Polyseat inventing a restriction Incus does not have.

	// Every directory is created once. A folder of three hundred saves is three
	// hundred files in one place, and asking Incus to create it each time is
	// three hundred round trips for the two hundred and ninety nine answers
	// nobody reads.
	made := map[string]bool{}
	first := ""

	for {
		up, err := files.Next()

		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return got, err
		}

		rel, err := ValidateDropPath(up.Path)
		if err != nil {
			got.Skipped = append(got.Skipped, Skipped{Path: up.Path, Reason: err.Error()})

			m.logf(name, "! a file was not accepted: %v", err)

			continue
		}

		dest := DropDir + "/" + rel

		counted := &counter{r: up.Body}

		// Asked again for every file, because the answer decides how the file
		// is written and a seat can be started in the middle of a folder.
		status, err := into.Status(name)
		if err != nil {
			return got, err
		}

		// A failure here stops the whole drop rather than joining Skipped. What
		// reaches this point has a name the seat accepts, so what fails is the
		// container, the connection, the disk, or a link the player put in the
		// way, and none of those is going to be different for the next file.
		switch status {
		case "Running":
			// No deadline: an upload lasts as long as somebody's uplink needs,
			// and the body ends when the browser stops sending.
			err = writeAsPlayer(context.Background(), into, name, dest, counted)
		case "Stopped":
			err = dropIntoStopped(into, name, dest, made, s.PlayerUID, counted)
		default:
			err = fmt.Errorf("the seat is %s, try again in a moment", strings.ToLower(status))
		}

		if err != nil {
			return got, fmt.Errorf("%s could not be written into the seat: %w", rel, err)
		}

		if first == "" {
			first = rel
		}

		got.Files++
		got.Bytes += counted.n
	}

	if got.Files == 0 && len(got.Skipped) == 0 {
		return got, errors.New("nothing was uploaded")
	}

	if got.Files > 0 {
		m.logf(name, "%s arrived in %s", describeDrop(first, got), DropDir)
	}

	// An AppImage that arrives here is deliberately left to the sweep, which
	// runs a minute apart and is the thing that knows how to adopt one: it
	// checks the magic before trusting the name, moves the file to
	// ~/Applications and rebuilds both menus. Asking for that rebuild here as
	// the AppImage download does would not bring it forward anyway. The sweep
	// refuses a file whose mtime is under ten seconds old, because it cannot
	// tell one still being written from one that is finished, and a file this
	// function just wrote is always inside that window.

	m.notify()

	return got, nil
}

// execer is the one call writeAsPlayer needs, which both the Incus client and
// the upload's test seam have.
type execer interface {
	Exec(ctx context.Context, name string, argv []string, stdin io.Reader, stdout, stderr io.Writer) (int, error)
}

// writeAsPlayer writes one file into a running seat through dropScript.
//
// Used for everything the daemon puts in the player's home while the seat
// runs, uploads and the app list alike, for the reason dropScript gives.
func writeAsPlayer(ctx context.Context, into execer, name, dest string, body io.Reader) error {
	complaint := &cappedBuffer{limit: 4096}

	code, err := into.Exec(ctx, name, playerDrop(dest), body, io.Discard, complaint)
	if err != nil {
		return err
	}

	if code != 0 {
		return fmt.Errorf("the seat said: %s", lastLines(complaint.String(), 2))
	}

	return nil
}

// dropIntoStopped writes one file into a seat that is switched off, through
// the Incus file API, after making sure no link stands anywhere on the way.
//
// There is no player to write as when nothing in the seat runs, and the file
// API follows links and hands what it creates to the player, see dropScript.
// So every folder from the home down and the destination itself is looked at
// first, without following anything, and a link anywhere stops the file.
//
// Looking first and writing second is only sound because the seat is off:
// nothing inside it runs that could put a link there in between. That is also
// why the caller asks for the state before every file rather than once, and
// takes the other road the moment the seat is running.
func dropIntoStopped(into filer, name, dest string, made map[string]bool, uid int64, body io.Reader) error {
	dir := path.Dir(dest)

	if !made[dir] {
		walked := ""

		for _, part := range strings.Split(strings.Trim(dir, "/"), "/") {
			walked += "/" + part

			// Everything above the home is the image's, and nothing the
			// player can change.
			if !strings.HasPrefix(walked+"/", "/home/"+Player+"/") {
				continue
			}

			if made[walked] {
				continue
			}

			kind, err := into.FileType(name, walked)
			if err != nil {
				return err
			}

			switch kind {
			case "directory":
			case "":
				if err := into.MakeDir(name, walked, 0o755, uid, uid); err != nil {
					return err
				}
			default:
				return fmt.Errorf("%s in the seat is a %s rather than a folder, so nothing is written through it", walked, kind)
			}

			made[walked] = true
		}
	}

	switch kind, err := into.FileType(name, dest); {
	case err != nil:
		return err
	case kind != "" && kind != "file":
		return fmt.Errorf("%s in the seat is a %s, so it was not overwritten", dest, kind)
	}

	return into.PushStream(name, dest, body, 0o644, uid, uid)
}

// describeDrop is the log line's subject: the file when there was one, and how
// many when there were more.
func describeDrop(first string, got Received) string {
	size := humanBytes(got.Bytes)
	if size != "" {
		size = " (" + size + ")"
	}

	if got.Files == 1 {
		return first + size
	}

	return fmt.Sprintf("%d files%s", got.Files, size)
}

// counter is how many bytes went past, for the log line and the answer.
//
// Counted here rather than taken from the part's own header, because a
// multipart part does not carry a length: the browser sends one body and the
// sizes are only known by having read them.
type counter struct {
	r io.Reader
	n int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)

	return n, err
}
