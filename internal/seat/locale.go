package seat

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// A seat is built from a plain Arch image, and a plain Arch image has neither
// a locale nor a keyboard layout. systemd falls back to C.UTF-8 and xkb falls
// back to us, so everything in the seat speaks English and every key is in the
// wrong place. On a German machine that is wrong several times over: Steam and
// the launcher come up in English, so does every Windows game in the seat,
// because wine reads the Unix locale to decide what GetUserDefaultUILanguage
// answers and a title that ships fifteen translations picks by that alone, and
// signing in to a store means hunting for y and z.
//
// The clock is the third of them and was missed for a long time. A plain Arch
// image has no /etc/localtime at all, so the seat runs on UTC while the machine
// it is attached to runs on wall clock time. Two hours apart here, and it shows:
// the clock in the corner of Big Picture is simply wrong, and so is every
// timestamp a game or a log in the seat writes. It also cost an evening once,
// reading a seat's log against the host's clock.
//
// The seat has none of the three of its own to be right about. It is a screen
// attached to this machine, so it takes the machine's.

// localePattern is what a locale name may look like before it is allowed into
// a shell fragment. Nothing here comes from a seat or from the network, but it
// does come from a file on the host, and the fragment below is the one place
// in provisioning where a value read off disk reaches a shell.
var localePattern = regexp.MustCompile(`^[A-Za-z0-9_]+(\.[A-Za-z0-9-]+)?(@[A-Za-z0-9]+)?$`)

// hostLocale reports the locale the host is set to, or "" when it has none
// worth copying.
//
// Read from /etc/locale.conf rather than from the daemon's own environment,
// because polyseatd is a system service: its LANG is whatever systemd started
// it with, not what the person sitting at this machine chose. localectl would
// answer the same thing and needs a bus connection to do it.
//
// C and POSIX are not locales anybody picked, they are the absence of one, and
// copying them into the seat would be work that changes nothing.
func hostLocale() string {
	text, err := os.ReadFile("/etc/locale.conf")
	if err != nil {
		return ""
	}

	return parseLocaleConf(string(text))
}

// parseLocaleConf reads the LANG line out of a locale.conf.
//
// Separate from the file so that what it decides can be checked without one,
// which is the whole of the value here: the rejections matter more than the
// happy path.
func parseLocaleConf(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)

		value, ok := strings.CutPrefix(line, "LANG=")
		if !ok {
			continue
		}

		value = unquote(strings.TrimSpace(value))

		switch {
		case value == "", value == "C", value == "POSIX":
			return ""
		case strings.HasPrefix(value, "C."):
			return ""
		case !localePattern.MatchString(value):
			return ""
		}

		return value
	}

	return ""
}

// unquote removes a matched pair of quotes, and only a matched pair.
//
// Trimming quote characters wherever they appear would turn a value with one
// stray quote in it into a valid looking locale, which is exactly the shape
// the pattern below exists to catch.
func unquote(value string) string {
	if len(value) < 2 {
		return value
	}

	first, last := value[0], value[len(value)-1]

	if first == last && (first == '"' || first == '\'') {
		return value[1 : len(value)-1]
	}

	return value
}

// localeCharmap is the encoding half of a locale.gen line.
//
// /etc/locale.gen wants "de_DE.UTF-8 UTF-8": the locale and then the character
// set, separately. For anything without a suffix the charmap is not derivable
// and UTF-8 is the only answer worth guessing on a system built this decade.
func localeCharmap(locale string) string {
	if _, charmap, ok := strings.Cut(locale, "."); ok {
		return charmap
	}

	return "UTF-8"
}

// zonePattern is what a timezone name may look like before it is allowed into
// a shell fragment. Same reasoning as localePattern: the value comes off the
// host's disk and reaches a shell.
var zonePattern = regexp.MustCompile(`^[A-Za-z0-9+_-]+(/[A-Za-z0-9+_-]+){0,2}$`)

// hostTimezone reports the zone the host is set to, or "" when it cannot be
// read.
//
// From the /etc/localtime symlink, which is where both timedatectl and every
// distribution's installer put the answer. /etc/timezone is Debian's habit and
// is not written on Arch, so the symlink is the one thing that is there
// everywhere. Reading it rather than asking timedatectl for the same reason
// hostLocale reads a file: polyseatd is a system service and has no bus of its
// own to ask on.
func hostTimezone() string {
	target, err := os.Readlink("/etc/localtime")
	if err != nil {
		return ""
	}

	return parseLocaltime(target)
}

// parseLocaltime pulls the zone out of what /etc/localtime points at.
//
// Separate from the readlink so that the rejections can be checked without a
// host to read: a relative link, a link into somewhere that is not the zone
// database, and UTC, which is what a seat already has and therefore not work
// worth doing.
func parseLocaltime(target string) string {
	const dir = "/usr/share/zoneinfo/"

	// A relative link is what a distribution's installer sometimes writes,
	// with any number of ../ in front of it. What matters is the part after
	// the database directory.
	i := strings.Index(target, dir)
	if i < 0 {
		return ""
	}

	zone := target[i+len(dir):]

	// posix/ and right/ are the same zones under different leap second rules,
	// and a seat has no business with either.
	for _, prefix := range []string{"posix/", "right/"} {
		zone = strings.TrimPrefix(zone, prefix)
	}

	if zone == "" || zone == "UTC" || zone == "Etc/UTC" || !zonePattern.MatchString(zone) {
		return ""
	}

	return zone
}

// timezoneScript is what the seat is asked to run.
//
// The symlink rather than timedatectl, because timedatectl in a container
// answers "Failed to set time zone: Access denied" on a read only /etc/adjtime
// and because the symlink is what it would have written anyway.
//
// Nothing is restarted here. glibc reads the zone once per process, so what is
// already running keeps the old one until it starts again; the session is
// restarted at the end of provisioning, which is what the clock in Big Picture
// hangs off.
func timezoneScript(zone string) string {
	return fmt.Sprintf(`
set -e

ln -sfn /usr/share/zoneinfo/%[1]s /etc/localtime
printf '%%s
' '%[1]s' > /etc/timezone
`, zone)
}

// stepLocale gives the seat the host's language.
//
// Idempotent, and cheap on the runs where there is nothing to do: locale-gen
// takes a few seconds and is only reached when the entry was not already
// enabled. Writing /etc/locale.conf every time is free and repairs a seat
// whose file was replaced by an image update.
//
// On a seat being built there is nothing more to it: the user manager starts
// later and inherits the file. On a seat that is already running there is,
// and it is the whole reason the last line exists. See localeScript.
func (p *Provisioner) stepLocale(ctx context.Context) error {
	locale := hostLocale()
	if locale == "" {
		p.Log("the host has no locale set, leaving the seat on the default")

		return nil
	}

	p.Log("setting the seat's language to %s", locale)

	if _, err := p.sh(ctx, localeScript(locale)); err != nil {
		return err
	}

	return p.setTimezone(ctx)
}

// setTimezone gives the seat the host's clock.
//
// Its own function and not a line in the step above, because a host without a
// zone worth copying is the ordinary case on a machine that runs on UTC, and
// that is a sentence in the log rather than an error.
func (p *Provisioner) setTimezone(ctx context.Context) error {
	zone := hostTimezone()
	if zone == "" {
		p.Log("the host has no timezone worth copying, leaving the seat on UTC")

		return nil
	}

	p.Log("setting the seat's timezone to %s", zone)

	_, err := p.sh(ctx, timezoneScript(zone))

	return err
}

// localeScript is what the seat is asked to run.
//
// Idempotent, and cheap on the runs where there is nothing to do: locale-gen
// takes a few seconds and is only reached when the entry was not already
// enabled. Writing /etc/locale.conf every time is free and repairs a seat
// whose file was replaced by an image update.
//
// The last line is the one that is easy to leave out and leaves the job half
// done. /etc/locale.conf decides what a process started from now on inherits,
// but the player's systemd keeps the environment it was started with, and on
// a seat that has been up since before this ran that environment is the old
// one. The session is restarted at the end of provisioning and would come
// back in the old language anyway, which reads as the setting not working.
// Writing it into the user manager first means the restart picks it up.
//
// It fails on a seat being built for the first time, because there is no
// player logged in yet to have a manager, and that is not a failure: such a
// seat has nothing running to be in the wrong language, and its manager will
// read the file when it starts. Hence the guard rather than a bare call.
func localeScript(locale string) string {
	return fmt.Sprintf(`
set -e

if ! grep -q '^%[1]s %[2]s$' /etc/locale.gen; then
    if grep -q '^#%[1]s %[2]s$' /etc/locale.gen; then
        sed -i 's/^#%[1]s %[2]s$/%[1]s %[2]s/' /etc/locale.gen
    else
        printf '%%s %%s\n' '%[1]s' '%[2]s' >> /etc/locale.gen
    fi

    locale-gen
fi

printf 'LANG=%%s\n' '%[1]s' > /etc/locale.conf

systemctl --user --machine=%[3]s@ set-environment LANG='%[1]s' || true
`, locale, localeCharmap(locale), Player)
}

// ------------------------------------------------------------- the keyboard

// xkbPattern is what an xkb value may look like. Layouts and variants are
// comma separated lists of short names, options are colon separated pairs.
//
// The point of the check is not the character set: these values are written
// into the seat's sway config, and sway's config is line oriented, so a value
// carrying a newline or a brace would not be a bad layout but a sway directive
// somebody else wrote.
var xkbPattern = regexp.MustCompile(`^[A-Za-z0-9_,.:+-]+$`)

// Keyboard is the host's xkb configuration.
type Keyboard struct {
	Layout  string
	Variant string
	Model   string
	Options string
}

// hostKeyboard reports the keyboard the host is set to.
//
// From /etc/vconsole.conf, which is where systemd-localed keeps the xkb
// settings and what localectl prints back. The X11 snippet under
// /etc/X11/xorg.conf.d carries the same values, but it is a config file for a
// display server this host may not even run, and it is the copy rather than
// the original.
func hostKeyboard() Keyboard {
	text, err := os.ReadFile("/etc/vconsole.conf")
	if err != nil {
		return Keyboard{}
	}

	return parseVConsole(string(text))
}

// parseVConsole reads the xkb settings out of a vconsole.conf.
//
// KEYMAP is deliberately ignored. That one is the console keymap, and a
// container has no console to apply it to.
func parseVConsole(text string) Keyboard {
	var keyboard Keyboard

	for _, line := range strings.Split(text, "\n") {
		name, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}

		value = unquote(strings.TrimSpace(value))

		// An empty setting is how localed writes "no variant", and it is not
		// a value worth carrying or checking.
		if value == "" || !xkbPattern.MatchString(value) {
			continue
		}

		switch strings.TrimSpace(name) {
		case "XKBLAYOUT":
			keyboard.Layout = value
		case "XKBVARIANT":
			keyboard.Variant = value
		case "XKBMODEL":
			keyboard.Model = value
		case "XKBOPTIONS":
			keyboard.Options = value
		}
	}

	// A variant, a model or an option without a layout to apply them to is
	// not a keyboard, and half of one is worse than none: it would replace
	// sway's default with something incomplete.
	if keyboard.Layout == "" {
		return Keyboard{}
	}

	return keyboard
}

// swayInput renders the input block for the seat's sway config, or nothing at
// all when the host has no layout to pass on.
//
// Nothing rather than an explicit "us", so that a host which never set a
// keyboard leaves sway on its own default instead of being told a guess.
func (k Keyboard) swayInput() string {
	if k.Layout == "" {
		return ""
	}

	lines := []string{
		"# The keyboard this machine is set to. A seat is a screen attached to",
		"# it and the person typing is at its keyboard, so a seat left on the",
		"# xkb default puts a US layout under every key.",
		"input * {",
		"    xkb_layout " + k.Layout,
	}

	for _, setting := range []struct{ name, value string }{
		{"xkb_variant", k.Variant},
		{"xkb_model", k.Model},
		{"xkb_options", k.Options},
	} {
		if setting.value != "" {
			lines = append(lines, "    "+setting.name+" "+setting.value)
		}
	}

	return strings.Join(append(lines, "}"), "\n") + "\n"
}
