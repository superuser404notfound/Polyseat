package seat

import (
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
func (p *Provisioner) WriteApps(ctx context.Context) ([]string, error) {
	if p.uid == 0 {
		if err := p.readUID(ctx); err != nil {
			return nil, err
		}
	}

	found, err := p.installedLaunchers(ctx)
	if err != nil {
		return nil, err
	}

	ours := []app{
		{
			Name:      "Desktop",
			ImagePath: "desktop.png",
		},
		{
			Name:     "Steam Big Picture",
			Detached: []string{"setsid steam steam://open/bigpicture"},
			// Nothing to do beforehand: Steam is already installed and the
			// resolution is handled globally. The undo side closes Big Picture
			// again so the seat does not sit in it until somebody notices.
			PrepCmd:   []prep{{Do: "", Undo: "setsid steam steam://close/bigpicture"}},
			ImagePath: "steam.png",
		},
	}

	names := []string{"Desktop", "Steam Big Picture"}

	for _, l := range found {
		ours = append(ours, app{
			Name:     l.Name,
			Detached: []string{"setsid " + l.command},
		})

		names = append(names, l.Name)
	}

	// A seat that has never been started has no file yet. Not an error: the
	// merge simply has nothing to preserve.
	existing, _ := p.Client.ReadFile(p.name(), AppsPath)

	list, kept, err := mergeApps(ours, existing)
	if err != nil {
		return nil, err
	}

	if kept > 0 {
		p.Log("kept %d app entry/entries that were added by hand", kept)
	}

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return nil, err
	}

	err = p.Client.PushFile(p.name(), AppsPath, append(data, '\n'), 0o644, p.uid, p.uid)

	return names, err
}

// mergeApps puts Polyseat's entries first and keeps anything else that the
// existing file already had, reporting how many of those survived.
//
// Takes the old file as bytes rather than reading it, so that what happens to
// somebody's hand made app entry can be tested without a container to put one
// in. That is the part worth being sure about: the failure mode is silent and
// only shows up as work quietly gone after a restart.
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

	kept := 0

	for _, raw := range old.Apps {
		var head struct {
			Name string `json:"name"`
		}

		if err := json.Unmarshal(raw, &head); err != nil {
			continue
		}

		if head.Name == "" || replaced[head.Name] {
			continue
		}

		list.Apps = append(list.Apps, raw)
		kept++
	}

	return list, kept, nil
}

// installed is a launcher that is really there, with the command that starts it.
type installed struct {
	Name    string
	command string
}

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
		}
	}

	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })

	return found, nil
}
