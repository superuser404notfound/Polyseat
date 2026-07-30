package seat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"

	"github.com/superuser404notfound/Polyseat/internal/incusx"
)

// AppImageDir is where a seat keeps the AppImages it can start.
//
// One directory rather than "wherever the file happens to be", because two
// different things have to agree on the answer: the daemon, which turns what is
// there into entries in the Moonlight list and the seat's own launcher, and the
// player, who put the file there with a browser. ~/Applications is the name the
// rest of the world already uses for this, so somebody who has met AppImages
// before will guess right.
//
// ~/Downloads is the other half of the same idea and is only in the scan below.
// A browser saves there, and asking somebody who is streaming a desktop to a
// television to then open a file manager and move a file is asking for the step
// where it goes wrong: the scan moves them, so an AppImage downloaded inside the
// seat turns up in Moonlight a minute later without anybody having been told
// about this directory at all.
const AppImageDir = "/home/" + Player + "/Applications"

// AppImage is one file in that directory, as the interface sees it.
type AppImage struct {
	// File is the file name, which is also the id: within one directory it is
	// unique, and it is what somebody sees if they look with a file manager.
	File string `json:"file"`
	Name string `json:"name"`
	Path string `json:"path"`
	Icon string `json:"icon,omitempty"`
	Size string `json:"size,omitempty"`
}

// appImageName is what a file in ~/Applications may be called.
//
// Narrow on purpose. The name reaches three places that each parse differently:
// a shell-ish command in Sunshine's app list, an Exec= line in a desktop entry,
// and a URL path in this daemon's own API. A name that needs quoting in one of
// them is a name that will one day be unquoted in another, so names that need
// quoting do not exist: whatever comes in is mapped to this set on the way in.
// A leading dot or dash is what is excluded rather than everything unusual: a
// name starting with a dash is an option to whatever runs it, and one starting
// with a dot is a file the player cannot see in a file manager. The scan strips
// exactly those two, and a test checks that the two agree.
var appImageName = regexp.MustCompile(`^[A-Za-z0-9_+][A-Za-z0-9._+-]{0,119}$`)

// ValidateAppImageFile checks a file name that came from outside.
func ValidateAppImageFile(file string) error {
	if !appImageName.MatchString(file) {
		return fmt.Errorf("that is not the name of an AppImage in this seat")
	}

	return nil
}

// safeAppImageName maps anything to a name that needs no quoting anywhere.
//
// Kept next to the scan's version of the same rule, and checked against it by a
// test, because the two run in different languages on different machines and a
// name that only one of them accepts is a file the interface can list and not
// remove.
func safeAppImageName(name string) string {
	var b strings.Builder

	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '_' || r == '+' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}

	out := strings.TrimLeft(b.String(), ".-")

	if out == "" {
		out = "download"
	}

	if !strings.EqualFold(path.Ext(out), ".appimage") {
		out += ".AppImage"
	}

	// Shortened in the middle rather than at the end, and after the extension
	// has been settled rather than before. Cutting the end first is what the
	// test caught: a very long name came back at 129 characters, one over what
	// the pattern allows, so the daemon refused a name the seat had just
	// written and the file could be listed and never removed.
	if len(out) > 120 {
		out = out[:120-len(".AppImage")] + out[len(out)-len(".AppImage"):]
	}

	// Belt and braces: the trim above can leave a name starting with something
	// the pattern rejects only if the pattern and this function disagree, and
	// this is the cheaper place to find that out.
	if !appImageName.MatchString(out) {
		return "download.AppImage"
	}

	return out
}

// ValidateAppImageURL checks a download address and says what it will be called.
//
// https only, and https across redirects too, which is enforced again in curl's
// arguments. An AppImage is an executable somebody is about to run as
// themselves; fetching it over a connection anybody on the way can rewrite is a
// different thing from fetching a flatpak, where the repository is signed.
func ValidateAppImageURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)

	if raw == "" {
		return "", fmt.Errorf("paste the address of an AppImage to install")
	}

	if len(raw) > 2000 {
		return "", fmt.Errorf("that address is too long")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("that is not an address: %w", err)
	}

	if parsed.Scheme != "https" {
		return "", fmt.Errorf("only https addresses are downloaded, because what arrives is run as a program")
	}

	if parsed.Host == "" {
		return "", fmt.Errorf("that address names no server")
	}

	return safeAppImageName(path.Base(parsed.Path)), nil
}

// maxAppImage is the largest download this will accept, in bytes.
//
// Emulators are the reason this feature exists and they are not small, so this
// is generous rather than tight. What it is for is the address that turns out to
// point at something that is not an AppImage at all: without a limit, a mistyped
// address can fill a seat's disk before the check that would have rejected it
// ever runs.
const maxAppImage = 6 << 30

// appImageProbe reports whether a downloaded file really is an AppImage.
//
// The magic is the two bytes AI followed by the type at offset 8 of an ELF
// header, which is what the format puts there and what every AppImage this was
// tested against has. Checked because the daemon is about to execute the file to
// read its name and icon, and because a file that is not an AppImage would
// otherwise sit in ~/Applications as an entry in Moonlight that does nothing.
const appImageProbe = `
import sys

with open(sys.argv[1], "rb") as fh:
    head = fh.read(11)

ok = head[:4] == b"\x7fELF" and head[8:11] in (b"AI\x01", b"AI\x02")
sys.exit(0 if ok else 1)
`

// appImageScan is the whole of the seat side, run once a minute.
//
// It does three things that belong together because they share the same
// knowledge of where files are: it adopts what was downloaded, it reads the name
// and icon out of each AppImage, and it prints the result. Splitting them would
// mean three execs into the container per minute instead of one.
//
// Reading name and icon means running the file, which is why the magic is
// checked first: --appimage-extract is handled by the AppImage runtime and never
// reaches the payload, but only if there is a runtime there at all. An ordinary
// executable named .AppImage would simply be run, and it is not this scan's job
// to be the thing that starts it.
const appImageScan = `
import glob, json, os, re, shutil, subprocess, tempfile, time

home = os.environ.get("POLYSEAT_HOME", "/home/player")
apps = os.path.join(home, "Applications")
inbox = os.path.join(home, "Downloads")
cache = os.path.join(home, ".cache/polyseat/appimage")

# The same rule as safeAppImageName in the daemon, and a test compares them.
UNSAFE = re.compile(r"[^A-Za-z0-9._+-]")


def safe_name(name):
    out = UNSAFE.sub("-", name).lstrip(".-")
    if not out:
        out = "download"
    if not out.lower().endswith(".appimage"):
        out += ".AppImage"
    if len(out) > 120:
        out = out[:120 - len(".AppImage")] + out[-len(".AppImage"):]
    return out


def is_appimage(path):
    """ELF, with the AppImage type marker where the format puts it."""
    try:
        with open(path, "rb") as fh:
            head = fh.read(11)
    except OSError:
        return False

    return head[:4] == b"\x7fELF" and head[8:11] in (b"AI\x01", b"AI\x02")


def adopt():
    """Move what was downloaded into ~/Applications.

    Replacing a file of the same name rather than keeping both, because that is
    what downloading a newer build of the same emulator is: an update. Two files
    differing by a suffix would be two entries in Moonlight, one of them stale.
    """
    for found in sorted(glob.glob(os.path.join(inbox, "*"))):
        if not os.path.isfile(found) or os.path.islink(found):
            continue

        if not found.lower().endswith(".appimage") or not is_appimage(found):
            continue

        # A file still being written is a file whose name is about to change or
        # whose magic is about to be complete. One round of waiting costs a
        # minute and saves adopting half a download.
        try:
            if time.time() - os.stat(found).st_mtime < 10:
                continue
        except OSError:
            continue

        dest = os.path.join(apps, safe_name(os.path.basename(found)))

        # move rather than rename, because a seat already has mount points under
        # this home: the shared library is mounted inside it, so "both are in
        # /home/player" does not mean both are on one filesystem.
        try:
            shutil.move(found, dest)
            os.chmod(dest, 0o755)
        except (OSError, shutil.Error):
            continue


def extract(path, into):
    """Pull the metadata files out of an AppImage, without running its payload."""
    for pattern in ("*.desktop", ".DirIcon",
                    "usr/share/icons/hicolor/*/apps/*.png"):
        try:
            subprocess.run([path, "--appimage-extract", pattern], cwd=into,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                           timeout=120, check=False)
        except (OSError, subprocess.SubprocessError):
            return False

    return os.path.isdir(os.path.join(into, "squashfs-root"))


def png(path):
    try:
        with open(path, "rb") as fh:
            return fh.read(8) == b"\x89PNG\r\n\x1a\n"
    except OSError:
        return False


def read_meta(path, key):
    """Name and icon, from inside the file."""
    meta = {"name": "", "icon": ""}
    tmp = tempfile.mkdtemp(dir=cache)

    try:
        if not extract(path, tmp):
            return meta

        root = os.path.join(tmp, "squashfs-root")

        for entry in sorted(glob.glob(os.path.join(root, "*.desktop"))):
            try:
                text = open(entry, encoding="utf-8", errors="replace").read()
            except OSError:
                continue

            m = re.search(r"^Name\s*=\s*(.+)$", text, re.MULTILINE)
            if m and m.group(1).strip():
                meta["name"] = m.group(1).strip()[:80]
                break

        # The largest picture wins: this is scaled up into a card on a
        # television rather than drawn in a menu at sixteen pixels. .DirIcon is
        # tried as well as the theme directories because an AppImage may carry
        # only one of the two, and it can be a dangling symlink into a part of
        # the image that was not extracted, which isfile answers for.
        icons = sorted(
            glob.glob(os.path.join(root, "usr/share/icons/hicolor/*/apps/*.png")),
            key=lambda p: os.path.getsize(p) if os.path.isfile(p) else 0,
            reverse=True,
        )
        icons.append(os.path.join(root, ".DirIcon"))

        for icon in icons:
            if not os.path.isfile(icon) or not png(icon):
                continue

            dest = os.path.join(cache, key + ".png")
            try:
                shutil.copyfile(icon, dest)
                meta["icon"] = dest
            except OSError:
                meta["icon"] = ""
            break
    finally:
        shutil.rmtree(tmp, ignore_errors=True)

    return meta


def meta_of(path, key, stat):
    """The metadata, from the cache when the file has not changed.

    Extracting means starting a program and unpacking part of a squashfs, once a
    minute, per AppImage. The stamp is what makes that happen once per file
    instead: a new build of the same emulator has a different size or a different
    time, and gets read again.
    """
    stamp = "%d:%d" % (stat.st_size, int(stat.st_mtime))
    record = os.path.join(cache, key + ".json")

    try:
        with open(record, encoding="utf-8") as fh:
            got = json.load(fh)

        if got.get("stamp") == stamp and (
                not got.get("icon") or os.path.exists(got["icon"])):
            return got
    except (OSError, ValueError):
        pass

    got = read_meta(path, key)
    got["stamp"] = stamp

    try:
        with open(record, "w", encoding="utf-8") as fh:
            json.dump(got, fh)
    except OSError:
        pass

    return got


def main():
    for d in (apps, cache):
        try:
            os.makedirs(d, exist_ok=True)
        except OSError:
            pass

    adopt()

    found = []
    keys = set()

    for path in sorted(glob.glob(os.path.join(apps, "*"))):
        if not os.path.isfile(path) or os.path.islink(path):
            continue

        if not is_appimage(path):
            continue

        key = os.path.basename(path)
        keys.add(key)

        try:
            stat = os.stat(path)

            if not stat.st_mode & 0o100:
                os.chmod(path, 0o755)
        except OSError:
            continue

        got = meta_of(path, key, stat)

        found.append({
            "file": key,
            "path": path,
            "name": got.get("name") or re.sub(r"(?i)\.appimage$", "", key),
            "icon": got.get("icon", ""),
            "bytes": stat.st_size,
        })

    # What a removed AppImage left behind. Cheap, and without it a seat's cache
    # keeps every icon it has ever seen.
    for stale in glob.glob(os.path.join(cache, "*.json")):
        key = os.path.basename(stale)[:-len(".json")]
        if key in keys:
            continue

        for gone in (stale, os.path.join(cache, key + ".png")):
            try:
                os.remove(gone)
            except OSError:
                pass

    print(json.dumps(found))


# Under a guard so that a test can load this and call one function of it rather
# than only checking the whole for the words it contains. The daemon runs it as
# python3 -c, where this is true.
if __name__ == "__main__":
    main()
`

// scanAppImages asks a seat what it has in ~/Applications.
//
// Takes the client rather than hanging off either the manager or the
// provisioner, because both need the same answer: the provisioner to build the
// app list, the manager to draw the software panel.
func scanAppImages(ctx context.Context, client *incusx.Client, seat string) ([]AppImage, error) {
	out, code, err := client.Try(ctx, seat, "sudo", "-u", Player, "env",
		"HOME=/home/"+Player, "python3", "-c", appImageScan)
	if err != nil {
		return nil, err
	}

	if code != 0 {
		return nil, fmt.Errorf("the seat could not be asked about AppImages: %s", lastLines(out, 2))
	}

	var found []struct {
		File  string `json:"file"`
		Path  string `json:"path"`
		Name  string `json:"name"`
		Icon  string `json:"icon"`
		Bytes int64  `json:"bytes"`
	}

	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &found); err != nil {
		return nil, fmt.Errorf("the seat's answer about AppImages could not be read: %w", err)
	}

	images := make([]AppImage, 0, len(found))

	for _, f := range found {
		// A file the daemon would not accept back from the interface is a row
		// with a Remove button that cannot work. The scan applies the same rule,
		// so this only fires if the two ever drift apart.
		if ValidateAppImageFile(f.File) != nil {
			continue
		}

		images = append(images, AppImage{
			File: f.File,
			Name: f.Name,
			Path: f.Path,
			Icon: f.Icon,
			Size: humanBytes(f.Bytes),
		})
	}

	return images, nil
}

// appImageRuntime reports what would stop an AppImage from starting in a seat,
// or nothing when there is nothing to say.
//
// Worth one exec, because the failure it catches is silent in the worst way: a
// seat built before Polyseat installed fuse2 lists AppImages, offers them in
// Moonlight, generates launcher entries for them and then does nothing at all
// when one is picked. The AppImage runtime dlopens libfuse.so.2 by name, so
// fuse3 being there does not help, and the message it prints goes to a terminal
// nobody is looking at.
//
// Asked of ldconfig rather than of pacman, because what matters is whether the
// library can be found, not whether a package claims to have installed it.
func (m *Manager) appImageRuntime(ctx context.Context, name string) string {
	_, code, err := m.client.Try(ctx, name, "sh", "-c",
		"ldconfig -p | grep -q libfuse.so.2")
	if err != nil || code == 0 {
		return ""
	}

	return "AppImages can be kept here but will not start: this seat has no " +
		"libfuse.so.2. Provision it again to add it."
}

// humanBytes is the size as flatpak would have printed it, so the two lists in
// the panel do not each have their own idea of what a megabyte looks like.
func humanBytes(n int64) string {
	switch {
	case n <= 0:
		return ""
	case n < 1e6:
		return fmt.Sprintf("%.1f kB", float64(n)/1e3)
	case n < 1e9:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	default:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	}
}

// appImageGames turns what is in ~/Applications into entries for both menus.
//
// The whole point of the feature: an emulator that ships only as an AppImage is
// otherwise a file somebody has to find with a file manager, using a gamepad, on
// a television. As a game it is a card in Moonlight and an icon in the seat's
// launcher, which is where every other installed thing already is.
func (p *Provisioner) appImageGames(ctx context.Context) []Game {
	images, err := scanAppImages(ctx, p.Client, p.name())
	if err != nil {
		p.Log("! the seat could not be asked about AppImages: %v", err)

		return nil
	}

	games := make([]Game, 0, len(images))

	for _, img := range images {
		games = append(games, Game{
			Name:   img.Name,
			Launch: img.Path,
			Image:  img.Icon,
			Source: "appimage",
		})
	}

	return games
}

// InstallAppImage downloads one into a seat.
//
// Into the player's own directory as the player, for the same reason flatpak is
// installed with --user: what the daemon puts there and what the player puts
// there are the same files with the same owner, so either of them can remove
// what the other installed.
func (m *Manager) InstallAppImage(name, rawURL string) error {
	file, err := ValidateAppImageURL(rawURL)
	if err != nil {
		return err
	}

	if _, err := m.store.Get(name); err != nil {
		return err
	}

	return m.operate(name, "installing "+file, func(ctx context.Context) error {
		part := AppImageDir + "/.polyseat-download.part"
		dest := AppImageDir + "/" + file

		if out, code, err := m.client.Try(ctx, name, m.playerEnv(
			"install", "-d", "-m", "0755", AppImageDir)...); err != nil {
			return err
		} else if code != 0 {
			return fmt.Errorf("%s could not be created: %s", AppImageDir, lastLines(out, 2))
		}

		watch := &downloadWatch{m: m, seat: name}

		// -# rather than the default meter, because the default one prints a
		// table of columns that says nothing about how far along it is, and this
		// is a download of several hundred megabytes from somebody else's
		// server. Measured in a seat: curl draws the bar on standard error even
		// with no terminal at all, so unlike flatpak this needs no pseudo
		// terminal to report progress.
		//
		// Both proto flags: the first restricts what may be asked for, the
		// second what a redirect may lead to. Without the second, an https
		// address is a redirect away from being a plain http download.
		code, err := m.client.Exec(ctx, name, m.playerEnv(
			"curl", "-fL", "-#",
			"--proto", "=https", "--proto-redir", "=https",
			"--max-filesize", fmt.Sprint(maxAppImage),
			"--max-time", "7200",
			"-o", part, "--", rawURL), nil, watch, watch)
		if err != nil {
			return err
		}

		if code != 0 {
			_, _, _ = m.client.Try(ctx, name, "rm", "-f", part)

			return fmt.Errorf("%s could not be downloaded: %s", rawURL, watch.tail())
		}

		if _, code, err := m.client.Try(ctx, name, m.playerEnv(
			"python3", "-c", appImageProbe, part)...); err != nil {
			return err
		} else if code != 0 {
			_, _, _ = m.client.Try(ctx, name, "rm", "-f", part)

			return fmt.Errorf("what arrived is not an AppImage, so it was not kept")
		}

		if out, code, err := m.client.Try(ctx, name, m.playerEnv(
			"sh", "-c", "mv -f "+part+" "+dest+" && chmod 0755 "+dest)...); err != nil {
			return err
		} else if code != 0 {
			return fmt.Errorf("%s could not be put in place: %s", file, lastLines(out, 2))
		}

		m.logf(name, "%s is in %s", file, AppImageDir)

		// So it appears in Moonlight and in the seat's launcher without waiting
		// for the next sweep.
		m.refreshAppsWhenNobodyIsStreaming(ctx, name)

		return nil
	})
}

// RemoveAppImage deletes one from a seat.
func (m *Manager) RemoveAppImage(name, file string) error {
	if err := ValidateAppImageFile(file); err != nil {
		return err
	}

	if _, err := m.store.Get(name); err != nil {
		return err
	}

	return m.operate(name, "removing "+file, func(ctx context.Context) error {
		// The cached name and icon go with it. They are rebuilt from the file,
		// so leaving them would leave a card for something that is gone until
		// the next scan tidied up after itself.
		cache := "/home/" + Player + "/.cache/polyseat/appimage/" + file

		out, code, err := m.client.Try(ctx, name, m.playerEnv("rm", "-f",
			AppImageDir+"/"+file, cache+".json", cache+".png")...)
		if err != nil {
			return err
		}

		if code != 0 {
			return fmt.Errorf("%s could not be removed: %s", file, lastLines(out, 2))
		}

		m.logf(name, "%s is gone", file)

		m.refreshAppsWhenNobodyIsStreaming(ctx, name)

		return nil
	})
}

// percent matches the figure curl draws in its progress bar.
//
// "######   17.5%", redrawn over one line with a carriage return, so this reads
// a stream of overwrites rather than lines. One figure and no steps, unlike
// flatpak: a download is one thing being fetched from one place.
var percent = regexp.MustCompile(`([0-9]{1,3})(?:\.[0-9])?%`)

// downloadWatch reads a download as it happens.
type downloadWatch struct {
	m    *Manager
	seat string

	mu   sync.Mutex
	last []byte
	edge []byte
	seen int
}

func (w *downloadWatch) Write(p []byte) (int, error) {
	w.mu.Lock()

	chunk := append(append([]byte{}, w.edge...), p...)

	if len(chunk) > 256 {
		w.edge = append([]byte{}, chunk[len(chunk)-256:]...)
	} else {
		w.edge = append([]byte{}, chunk...)
	}

	w.last = append(w.last, p...)
	if len(w.last) > 8192 {
		w.last = w.last[len(w.last)-8192:]
	}

	value, ok := lastPercent(string(chunk))

	changed := ok && value != w.seen
	if changed {
		w.seen = value
	}

	w.mu.Unlock()

	if changed && w.m != nil {
		w.m.setProgress(w.seat, value)
	}

	return len(p), nil
}

// lastPercent is how far the newest figure in a chunk says the download has got.
func lastPercent(out string) (int, bool) {
	found := percent.FindAllStringSubmatch(out, -1)
	if len(found) == 0 {
		return 0, false
	}

	last := found[len(found)-1]

	value := 0
	for _, r := range last[1] {
		value = value*10 + int(r-'0')
	}

	if value > 100 {
		return 0, false
	}

	return value, true
}

func (w *downloadWatch) tail() string {
	w.mu.Lock()
	defer w.mu.Unlock()

	text := strings.ReplaceAll(string(w.last), "\r", "\n")

	return lastLines(text, 3)
}
