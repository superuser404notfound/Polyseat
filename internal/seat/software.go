package seat

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// CatalogEntry is something the web interface offers to install into a seat.
type CatalogEntry struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

// Catalog is the short list, not a copy of Flathub.
//
// A seat is a games machine, so the useful set is small enough to name: the
// launchers Polyseat can also put in the Moonlight app list. Anything else is
// installable by typing its application id, which is why this is a starting
// point rather than a limit.
//
// What is missing from it is deliberate. Steam, Lutris and Firefox are
// installed into every seat as ordinary packages, so offering them again as
// flatpaks would be a second copy of something already there, taking hundreds
// of megabytes per seat to do the same job worse: a sandboxed launcher has to
// be told separately that it may see the shared library.
//
// The ones that stay are the ones with no package in the distribution, or none
// worth preferring, and the ones not everybody wants. Heroic alone is close to
// a gigabyte once its runtime is counted, and a seat belonging to somebody who
// only plays on Steam should not carry it.
var Catalog = []CatalogEntry{
	{"com.heroicgameslauncher.hgl", "Heroic", "Epic, GOG and Amazon games"},
	{"com.usebottles.bottles", "Bottles", "Runs Windows software in prefixes"},
	{"io.itch.itch", "itch", "The itch.io library"},
	{"org.prismlauncher.PrismLauncher", "Prism Launcher", "Minecraft"},
	{"org.libretro.RetroArch", "RetroArch", "Emulators, one interface"},
	{"net.davidotek.pupgui2", "ProtonUp-Qt", "Installs Proton-GE for Lutris and Heroic"},
	{"com.discordapp.Discord", "Discord", "Voice chat while playing"},
}

// Installed is one flatpak that is really in a seat.
type Installed struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size string `json:"size"`
}

// SoftwareStatus is what the interface needs to draw the software section.
type SoftwareStatus struct {
	// Available is whether anything can be installed right now. False for a
	// seat that is switched off or was built before Polyseat installed
	// flatpak, and Problem says which.
	Available bool   `json:"available"`
	Problem   string `json:"problem,omitempty"`

	Installed []Installed    `json:"installed"`
	Catalog   []CatalogEntry `json:"catalog"`
}

// flatpakID is the reverse DNS form Flathub uses.
//
// Checked before anything is run with it, even though every call here is an
// argv list and never a shell string. The argv list is what makes a crafted id
// harmless; this is what makes a typo say so instead of producing a flatpak
// error nobody can read.
var flatpakID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*(\.[A-Za-z][A-Za-z0-9_-]*){2,}$`)

// ValidateAppID checks a proposed application id.
func ValidateAppID(id string) error {
	if !flatpakID.MatchString(id) {
		return fmt.Errorf("an application id looks like com.example.Name, with at least three parts")
	}

	if len(id) > 255 {
		return fmt.Errorf("that application id is too long")
	}

	return nil
}

// Software reports what a seat has installed and what it can be given.
func (m *Manager) Software(ctx context.Context, name string) (SoftwareStatus, error) {
	status := SoftwareStatus{
		Installed: []Installed{},
		Catalog:   Catalog,
	}

	if _, err := m.store.Get(name); err != nil {
		return status, err
	}

	state, err := m.client.Status(name)
	if err != nil {
		return status, err
	}

	if !strings.EqualFold(state, "running") {
		status.Problem = "the seat has to be running before software can be installed"

		return status, nil
	}

	out, code, err := m.client.Try(ctx, name, m.playerEnv(
		"flatpak", "list", "--user", "--app", "--columns=application,name,size")...)
	if err != nil {
		return status, err
	}

	if code != 0 {
		status.Problem = "this seat has no flatpak yet, provision it again to add it"

		return status, nil
	}

	status.Available = true

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}

		fields := strings.Split(line, "\t")
		if len(fields) < 1 || fields[0] == "" {
			continue
		}

		entry := Installed{ID: fields[0], Name: fields[0]}

		if len(fields) > 1 && fields[1] != "" {
			entry.Name = fields[1]
		}

		if len(fields) > 2 {
			entry.Size = fields[2]
		}

		status.Installed = append(status.Installed, entry)
	}

	return status, nil
}

// playerEnv wraps a command so it runs as the player with a home directory.
//
// Both halves matter. A flatpak installed with --user lives under the player's
// home, so running any of this as root reports the system installation, which
// in a seat is empty; and flatpak without HOME set does not know where that is.
func (m *Manager) playerEnv(argv ...string) []string {
	return append([]string{"sudo", "-u", Player, "env", "HOME=/home/" + Player}, argv...)
}

// InstallSoftware puts a flatpak into a seat.
//
// Installed into the player's own installation rather than system wide, so
// that what the daemon installs and what the player installs are the same
// thing in the same place. The alternative, a system installation only the
// daemon can write, would mean two lists of software with different rules, and
// the player being unable to remove something they can see.
func (m *Manager) InstallSoftware(name, id string) error {
	if err := ValidateAppID(id); err != nil {
		return err
	}

	if _, err := m.store.Get(name); err != nil {
		return err
	}

	return m.operate(name, "installing "+id, func(ctx context.Context) error {
		out, code, err := m.client.Try(ctx, name, m.playerEnv(
			"flatpak", "install", "--user", "--assumeyes", "--noninteractive",
			"flathub", id)...)
		if err != nil {
			return err
		}

		if code != 0 {
			return fmt.Errorf("%s could not be installed: %s", id, lastLines(out, 3))
		}

		m.logf(name, "%s is installed", id)

		// So it appears in Moonlight without waiting for the next restart. The
		// app list is generated from what is installed, and this is the moment
		// that changed.
		m.refreshApps(ctx, name)

		return nil
	})
}

// RemoveSoftware takes a flatpak back out of a seat.
func (m *Manager) RemoveSoftware(name, id string) error {
	if err := ValidateAppID(id); err != nil {
		return err
	}

	if _, err := m.store.Get(name); err != nil {
		return err
	}

	return m.operate(name, "removing "+id, func(ctx context.Context) error {
		out, code, err := m.client.Try(ctx, name, m.playerEnv(
			"flatpak", "uninstall", "--user", "--assumeyes", "--noninteractive", id)...)
		if err != nil {
			return err
		}

		if code != 0 {
			return fmt.Errorf("%s could not be removed: %s", id, lastLines(out, 3))
		}

		m.logf(name, "%s is gone", id)

		// Unused runtimes are what makes a seat quietly grow: removing the last
		// application that needed a two gigabyte platform leaves the platform
		// behind. Reported rather than failed, because the application really
		// is gone either way.
		if _, code, err := m.client.Try(ctx, name, m.playerEnv(
			"flatpak", "uninstall", "--user", "--unused",
			"--assumeyes", "--noninteractive")...); err != nil || code != 0 {
			m.logf(name, "! unused runtimes could not be cleaned up, they are only taking space")
		}

		m.refreshApps(ctx, name)

		return nil
	})
}

// refreshApps rewrites the Moonlight app list for a running seat.
//
// Quiet unless something actually changed, because this also runs on the
// periodic sweep and a line every ten seconds would both fill the seat's log
// and wake the interface each time.
func (m *Manager) refreshApps(ctx context.Context, name string) {
	s, err := m.store.Get(name)
	if err != nil {
		return
	}

	p := &Provisioner{
		Client: m.client,
		Seat:   s,
		Image:  m.cfg.Image,
		Log:    func(f string, a ...any) { m.logf(name, f, a...) },
		uid:    m.runtimeOf(name).uid,
	}

	apps, changed, err := p.WriteApps(ctx)
	if err != nil {
		m.logf(name, "! the app list could not be updated: %v", err)

		return
	}

	if changed {
		m.logf(name, "Moonlight will offer: %s", strings.Join(apps, ", "))
	}
}
