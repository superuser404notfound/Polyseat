package seat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// AppsPath is the list Moonlight shows when it connects to a seat.
const AppsPath = "/home/" + Player + "/.config/sunshine/apps.json"

// launcher is something Polyseat knows how to offer in that list.
//
// A seat is not a desktop somebody sits at, so the app list is the menu: it is
// what a gamepad can navigate before a stream even starts, and for a client
// without a keyboard it is the only menu there is. A launcher that is
// installed and missing from here is effectively not installed.
//
// Both a native binary and a Flathub id per entry, because either can be true
// in the same seat. Steam is installed as an Arch package during provisioning;
// everything else is normally a flatpak the player installed themselves, which
// is exactly the case that has to keep working without the daemon being told.
type launcher struct {
	Name    string
	Binary  string
	Flatpak string
}

// launchers is the set Polyseat looks for. Deliberately a fixed list rather
// than a scan of .desktop files: an app list assembled from everything
// installed fills up with uninstallers and configuration tools, and picking
// the wrong entry in Moonlight means reconnecting to find out.
var launchers = []launcher{
	{Name: "Heroic", Binary: "heroic", Flatpak: "com.heroicgameslauncher.hgl"},
	{Name: "Lutris", Binary: "lutris", Flatpak: "net.lutris.Lutris"},
	{Name: "Bottles", Flatpak: "com.usebottles.bottles"},
	{Name: "itch", Flatpak: "io.itch.itch"},
	{Name: "Prism Launcher", Binary: "prismlauncher", Flatpak: "org.prismlauncher.PrismLauncher"},
	{Name: "RetroArch", Binary: "retroarch", Flatpak: "org.libretro.RetroArch"},
}

// stockApps are entries Sunshine ships that have no meaning in a seat.
//
// "Low Res Desktop" runs `xrandr --output HDMI-1 --mode 1920x1080`. There is no
// HDMI-1 in a headless container and no X server owning the output, so the
// entry cannot do anything except fail, and per-client resolution has made it
// pointless anyway. Named here so that seats built before Polyseat generated
// this file lose it on their next start rather than keeping it forever.
var stockApps = []string{"Low Res Desktop"}

// app is one entry. Only the fields Polyseat sets; everything Sunshine
// understands and Polyseat does not is preserved by keeping foreign entries as
// raw JSON rather than by round tripping them through this struct.
type app struct {
	Name      string   `json:"name"`
	Detached  []string `json:"detached,omitempty"`
	PrepCmd   []prep   `json:"prep-cmd,omitempty"`
	ImagePath string   `json:"image-path,omitempty"`

	// Polyseat marks the entries this file's generator owns.
	//
	// Without it there is no way to tell an entry Polyseat wrote last time from
	// one somebody added by hand, and the two need opposite treatment: ours
	// must disappear when whatever produced it is gone, theirs must survive.
	// Keeping unknown entries and generating from what is installed are
	// otherwise in direct conflict, which is how an uninstalled game stayed in
	// Moonlight's list forever. It stopped being generated, so it stopped being
	// recognised, so it was preserved as somebody's handiwork.
	//
	// Sunshine ignores keys it does not know.
	Polyseat bool `json:"polyseat"`
}

type prep struct {
	Do   string `json:"do"`
	Undo string `json:"undo,omitempty"`
}

type appList struct {
	Env  map[string]string `json:"env"`
	Apps []json.RawMessage `json:"apps"`
}

// WriteApps generates the seat's Sunshine app list.
//
// Called on every start rather than only while provisioning, because the whole
// point is that a launcher the player installed an hour ago shows up. Nothing
// in the seat tells the daemon that happened, so the list is rebuilt from what
// is actually installed each time the seat comes up.
//
// Entries Polyseat did not put there are kept exactly as they are. Sunshine's
// own web interface can add apps and somebody who used it should not find their
// work gone after a restart; the ones this function owns are replaced, and the
// stock entries that cannot work in a seat are dropped.
//
// Reports whether the file actually changed, which is what lets this be called
// on a timer without filling the seat's log with the same line every ten
// seconds. Sunshine rereads apps.json on every request, so writing it is the
// whole of the update: no restart, and nothing interrupted for anybody who is
// streaming at the time. Measured against a running seat, by taking an entry
// out of the file and asking Sunshine what it served afterwards.
func (p *Provisioner) WriteApps(ctx context.Context) ([]string, bool, error) {
	if p.uid == 0 {
		if err := p.readUID(ctx); err != nil {
			return nil, false, err
		}
	}

	found, err := p.installedLaunchers(ctx)
	if err != nil {
		return nil, false, err
	}

	ours, names := polyseatApps(found, p.installedGames(ctx))

	// A seat that has never been started has no file yet. Not an error: the
	// merge simply has nothing to preserve.
	existing, _ := p.Client.ReadFile(p.name(), AppsPath)

	list, kept, err := mergeApps(ours, existing)
	if err != nil {
		return nil, false, err
	}

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return nil, false, err
	}

	data = append(data, '\n')

	// Nothing to do, which on a timer is almost every time.
	//
	// The comparison is against the file rather than against something the
	// daemon remembers, so it also holds after a restart and after Sunshine
	// rewrote the file from its own interface. Byte comparison is safe here
	// because MarshalIndent reindents the whole document uniformly, including
	// the entries kept verbatim from the old file.
	if bytes.Equal(data, existing) {
		return names, false, nil
	}

	if kept > 0 {
		p.Log("kept %d app entry/entries that were added by hand", kept)
	}

	err = p.Client.PushFile(p.name(), AppsPath, data, 0o644, p.uid, p.uid)

	return names, err == nil, err
}

// Picking Desktop should land on something to choose from. Anything else
// should not have a launcher sitting on top of it.
const (
	showLauncher = "/usr/local/bin/polyseat-launcher show"
	hideLauncher = "/usr/local/bin/polyseat-launcher hide"
)

// polyseatApps builds every entry Polyseat owns, and the names in order.
//
// A function of its arguments rather than part of the writer, because the one
// thing that has to hold for all of them is that they carry the marker, and
// that is only checkable if they can be built without a container. Setting it
// on the struct definition and forgetting it here is exactly what happened
// once: every entry went out with "polyseat": false, so the file looked to the
// next merge like one nobody had generated.
func polyseatApps(launchers []installed, games []Game) ([]app, []string) {
	ours := []app{
		{
			Name:      "Desktop",
			PrepCmd:   []prep{{Do: showLauncher}},
			ImagePath: "desktop.png",
			Polyseat:  true,
		},
		{
			Name:     "Steam Big Picture",
			Detached: []string{"setsid steam steam://open/bigpicture"},
			// The undo side closes Big Picture again so the seat does not sit
			// in it until somebody notices.
			PrepCmd:   []prep{{Do: hideLauncher, Undo: "setsid steam steam://close/bigpicture"}},
			ImagePath: "steam.png",
			Polyseat:  true,
		},
	}

	names := []string{"Desktop", "Steam Big Picture"}
	taken := map[string]bool{"desktop": true, "steam big picture": true}

	add := func(name, launch, image string) {
		// A game called Desktop, or one whose name matches a launcher, would
		// otherwise put two entries with the same name in Moonlight and make
		// which of them runs a matter of luck.
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" || taken[key] {
			return
		}

		taken[key] = true

		ours = append(ours, app{
			Name:      name,
			Detached:  []string{"setsid " + launch},
			PrepCmd:   []prep{{Do: hideLauncher}},
			ImagePath: image,
			Polyseat:  true,
		})

		names = append(names, name)
	}

	for _, l := range launchers {
		add(l.Name, l.command, l.image)
	}

	// The games themselves, so that a client with a controller can start one
	// without first starting a launcher and steering through it.
	for _, g := range games {
		add(g.Name, g.Launch, g.Image)
	}

	return ours, names
}

// mergeApps puts Polyseat's entries first and keeps anything else that the
// existing file had and that Polyseat did not write, reporting how many of
// those survived.
//
// The second half of that sentence is what took two attempts. Keeping every
// entry it did not recognise looked like politeness towards somebody who had
// added an app through Sunshine's own interface, and it quietly made removal
// impossible: an uninstalled game stops being generated, so it stops being
// recognised, so it was preserved as somebody's handiwork and stayed in
// Moonlight's list forever. Ours are marked now, so the two cases can be told
// apart instead of guessed at.
//
// Takes the old file as bytes rather than reading it, so that both halves can
// be tested without a container to put an app entry in. Both failure modes are
// silent: one is work gone after a restart, the other is a menu entry that
// starts nothing.
func mergeApps(ours []app, existing []byte) (appList, int, error) {
	list := appList{
		// Flatpak puts the wrappers for exported applications in
		// .local/share/flatpak/exports/bin, which is not on PATH for a process
		// started by a user unit. Without this an app entry for a launcher the
		// player installed would be in the list and fail to start.
		Env: map[string]string{
			"PATH": "$(PATH):$(HOME)/.local/bin:$(HOME)/.local/share/flatpak/exports/bin",
		},
	}

	replaced := map[string]bool{}

	for _, a := range ours {
		raw, err := json.Marshal(a)
		if err != nil {
			return list, 0, err
		}

		list.Apps = append(list.Apps, raw)
		replaced[a.Name] = true
	}

	for _, name := range stockApps {
		replaced[name] = true
	}

	// A seat whose file is missing or corrupt should still end up with a
	// working list rather than with no app list at all. Neither is worth
	// failing a start over.
	var old appList
	if len(existing) == 0 || json.Unmarshal(existing, &old) != nil {
		return list, 0, nil
	}

	type head struct {
		Name     string `json:"name"`
		Polyseat bool   `json:"polyseat"`
	}

	heads := make([]head, 0, len(old.Apps))
	marked := false

	for _, raw := range old.Apps {
		var h head
		if err := json.Unmarshal(raw, &h); err != nil {
			h = head{}
		}

		heads = append(heads, h)

		if h.Polyseat {
			marked = true
		}
	}

	// A file written before the marker existed cannot be read entry by entry:
	// nothing in it says who put what there, and the entries Polyseat had
	// generated look exactly like somebody's own. Converging it once, by
	// keeping nothing, is the only reading that does not leave stale entries
	// behind forever. It happens at most once per seat, because everything
	// written from here on is marked.
	if !marked && len(old.Apps) > 0 {
		return list, 0, nil
	}

	kept := 0

	for i, raw := range old.Apps {
		h := heads[i]

		if h.Name == "" || h.Polyseat || replaced[h.Name] {
			continue
		}

		list.Apps = append(list.Apps, raw)
		kept++
	}

	return list, kept, nil
}

// installed is a launcher that is really there, with the command that starts it
// and the icon to show for it.
type installed struct {
	Name    string
	command string
	image   string
}

// iconScan finds the icon a desktop entry names, as a file Sunshine can read.
//
// Games arrive with artwork of their own, from Steam's cache or Lutris's, and
// a launcher had nothing, so Moonlight drew it as an unlabelled grey box next
// to titles that had covers. What a launcher does have is a desktop entry, and
// that names an icon rather than pointing at one, so the name has to be
// resolved against the icon themes: hicolor for anything installed, plus the
// flatpak export directory for anything the player installed themselves.
//
// Largest first, because this is scaled up into box art on a television rather
// than drawn at sixteen pixels in a menu. PNG only: Sunshine hands the file to
// a client that will not render SVG.
const iconScan = `
import glob, json, os, re, sys

home = "/home/player"
roots = [
    home + "/.local/share/flatpak/exports/share",
    "/var/lib/flatpak/exports/share",
    home + "/.local/share",
    "/usr/local/share",
    "/usr/share",
]
sizes = ["512x512", "384x384", "256x256", "192x192", "128x128", "96x96", "64x64"]

def icon_file(name):
    if not name:
        return ""
    if os.path.isabs(name) and os.path.exists(name):
        return name
    for root in roots:
        for size in sizes:
            p = os.path.join(root, "icons/hicolor", size, "apps", name + ".png")
            if os.path.exists(p):
                return p
        p = os.path.join(root, "pixmaps", name + ".png")
        if os.path.exists(p):
            return p
    return ""

def icon_of(*candidates):
    """The Icon= of the first desktop entry that names one, then that file."""
    for want in candidates:
        for root in roots:
            for path in glob.glob(os.path.join(root, "applications", want + ".desktop")):
                try:
                    text = open(path, encoding="utf-8", errors="replace").read()
                except OSError:
                    continue
                m = re.search(r"^Icon=(.+)$", text, re.MULTILINE)
                if m:
                    found = icon_file(m.group(1).strip())
                    if found:
                        return found
    return ""

out = {}
for want in json.loads(sys.argv[1]):
    # A flatpak's entry is named after its application id; a package's is
    # usually named after its binary, sometimes after a reverse DNS name.
    out[want["key"]] = icon_of(*want["names"])

print(json.dumps(out))
`

// installedLaunchers asks the seat what it has.
//
// One call rather than one per launcher: this runs on every seat start, and an
// `incus exec` per candidate turns a fixed cost into a growing one for no gain.
func (p *Provisioner) installedLaunchers(ctx context.Context) ([]installed, error) {
	var binaries []string
	for _, l := range launchers {
		if l.Binary != "" {
			binaries = append(binaries, l.Binary)
		}
	}

	script := `for b in ` + strings.Join(binaries, " ") + `; do
    command -v "$b" >/dev/null 2>&1 && echo "bin:$b"
done
flatpak list --app --columns=application 2>/dev/null | while read -r id; do
    [ -n "$id" ] && echo "flatpak:$id"
done
exit 0`

	// As the player with HOME set, because a flatpak installed into the user
	// installation is invisible to root: `flatpak list` as root reports the
	// system installation, which in a seat is empty.
	out, _, err := p.Client.Try(ctx, p.name(), "sudo", "-u", Player, "env",
		"HOME=/home/"+Player, "sh", "-c", script)
	if err != nil {
		return nil, err
	}

	haveBin := map[string]bool{}
	haveFlatpak := map[string]bool{}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)

		if id, ok := strings.CutPrefix(line, "bin:"); ok {
			haveBin[id] = true
		}

		if id, ok := strings.CutPrefix(line, "flatpak:"); ok {
			haveFlatpak[id] = true
		}
	}

	var found []installed

	// Which desktop entries could name this launcher's icon. Both spellings
	// per launcher, because a package and a flatpak of the same program do not
	// agree: net.lutris.Lutris against lutris.
	type wanted struct {
		Key   string   `json:"key"`
		Names []string `json:"names"`
	}

	var want []wanted

	for _, l := range launchers {
		switch {
		// A native package wins over a flatpak of the same thing. Both work,
		// but the native one is the one whose files the seat already shares
		// through the library, and running it does not go through a sandbox
		// that would have to be given access to the library mount separately.
		case l.Binary != "" && haveBin[l.Binary]:
			found = append(found, installed{Name: l.Name, command: l.Binary})
		case l.Flatpak != "" && haveFlatpak[l.Flatpak]:
			found = append(found, installed{
				Name:    l.Name,
				command: fmt.Sprintf("flatpak run %s", l.Flatpak),
			})
		default:
			continue
		}

		names := []string{}
		if l.Flatpak != "" {
			names = append(names, l.Flatpak)
		}

		if l.Binary != "" {
			names = append(names, l.Binary)
		}

		want = append(want, wanted{Key: l.Name, Names: names})
	}

	if icons := p.launcherIcons(ctx, want); icons != nil {
		for i := range found {
			found[i].image = icons[found[i].Name]
		}
	}

	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })

	return found, nil
}

// launcherIcons resolves an icon file for each launcher, keyed by its name.
//
// Best effort throughout. A launcher with no icon is a launcher Moonlight
// draws plainly, which is how it was before and is not worth failing a seat's
// start over.
func (p *Provisioner) launcherIcons(ctx context.Context, want any) map[string]string {
	query, err := json.Marshal(want)
	if err != nil {
		return nil
	}

	out, code, err := p.Client.Try(ctx, p.name(), "sudo", "-u", Player, "env",
		"HOME=/home/"+Player, "python3", "-c", iconScan, string(query))
	if err != nil || code != 0 {
		return nil
	}

	icons := map[string]string{}
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &icons) != nil {
		return nil
	}

	return icons
}
