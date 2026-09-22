package seat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The ordinary case, and the one the whole step exists for: a German host
// hands its language to the seat.
func TestParseLocaleConfTakesTheLangLine(t *testing.T) {
	conf := "#  This file was written by systemd-firstboot(1).\nLANG=de_DE.UTF-8\n"

	if got := parseLocaleConf(conf); got != "de_DE.UTF-8" {
		t.Errorf("read %q, want de_DE.UTF-8", got)
	}
}

// systemd writes the value quoted often enough that taking the quotes along
// would set the seat to a locale that cannot exist.
func TestParseLocaleConfStripsQuotes(t *testing.T) {
	if got := parseLocaleConf(`LANG="de_DE.UTF-8"`); got != "de_DE.UTF-8" {
		t.Errorf("read %q, want de_DE.UTF-8", got)
	}
}

// LC_* say what a locale means, not which one. Only LANG names it, and a file
// that sets the others around it must not be read as setting the language.
func TestParseLocaleConfIgnoresOtherVariables(t *testing.T) {
	conf := "LC_TIME=en_GB.UTF-8\nLC_MEASUREMENT=de_DE.UTF-8\n"

	if got := parseLocaleConf(conf); got != "" {
		t.Errorf("read %q, want nothing: no LANG is set here", got)
	}
}

// C and its spellings are the absence of a locale rather than a choice, and
// generating one in the seat would be several seconds spent changing nothing.
func TestParseLocaleConfRejectsTheNonLocales(t *testing.T) {
	for _, value := range []string{"C", "POSIX", "C.UTF-8", ""} {
		if got := parseLocaleConf("LANG=" + value); got != "" {
			t.Errorf("LANG=%q read as %q, want nothing", value, got)
		}
	}
}

// This is the one value in provisioning that comes off the host's disk and
// reaches a shell fragment. A locale name has no room in it for a quote or a
// semicolon, so anything carrying one is not a locale and is not passed on.
func TestParseLocaleConfRejectsWhatIsNotALocaleName(t *testing.T) {
	for _, value := range []string{
		"de_DE.UTF-8; rm -rf /",
		"de_DE.UTF-8'",
		"../../etc/passwd",
		"de DE",
		"$(id)",
	} {
		if got := parseLocaleConf("LANG=" + value); got != "" {
			t.Errorf("LANG=%q read as %q, want nothing", value, got)
		}
	}
}

// locale.gen wants the locale and its character set as two words, and the
// charset is only knowable from the suffix.
func TestLocaleCharmapComesFromTheSuffix(t *testing.T) {
	cases := map[string]string{
		"de_DE.UTF-8":      "UTF-8",
		"en_GB.ISO-8859-1": "ISO-8859-1",
		"de_DE":            "UTF-8",
	}

	for locale, want := range cases {
		if got := localeCharmap(locale); got != want {
			t.Errorf("%s: charmap %q, want %q", locale, got, want)
		}
	}
}

// The step has to be in the recipe to run at all, and it has to run before
// the steps that fill the seat, or the seat is built in English and only told
// afterwards.
func TestLocaleIsProvisionedBeforeTheSeatIsFilled(t *testing.T) {
	var locale, packages int

	steps := Steps()

	for i, step := range steps {
		switch step.Name {
		case "locale":
			locale = i
		case "packages":
			packages = i
		}
	}

	if locale == 0 {
		t.Fatal("no locale step in the recipe")
	}

	if locale > packages {
		t.Errorf("locale runs at %d, after packages at %d", locale, packages)
	}
}

// The ordinary case: a German host hands its layout to the seat.
func TestParseVConsoleTakesTheXkbSettings(t *testing.T) {
	conf := "KEYMAP=de\nXKBLAYOUT=de\nXKBMODEL=pc105\n"

	got := parseVConsole(conf)

	if got.Layout != "de" || got.Model != "pc105" {
		t.Errorf("read %+v, want layout de and model pc105", got)
	}

	// KEYMAP is the console keymap and there is no console in a container.
	if got.Variant != "" || got.Options != "" {
		t.Errorf("read %+v, want no variant and no options", got)
	}
}

// localed writes the settings it has no value for as empty strings, and an
// empty xkb_variant line is not the same as no line at all.
func TestParseVConsoleSkipsEmptySettings(t *testing.T) {
	got := parseVConsole("XKBLAYOUT=de\nXKBVARIANT=\nXKBOPTIONS=\"\"\n")

	if got.Layout != "de" {
		t.Fatalf("read %+v, want layout de", got)
	}

	if block := got.swayInput(); strings.Contains(block, "xkb_variant") {
		t.Errorf("the block carries an empty variant:\n%s", block)
	}
}

// Half a keyboard would replace sway's default with something incomplete,
// which is worse than leaving it alone.
func TestParseVConsoleWantsALayoutOrNothing(t *testing.T) {
	got := parseVConsole("KEYMAP=de\nXKBMODEL=pc105\nXKBOPTIONS=ctrl:nocaps\n")

	if got != (Keyboard{}) {
		t.Errorf("read %+v, want nothing without a layout", got)
	}
}

// These values are written into sway's config, which is line oriented. A
// value carrying a newline is not a bad layout, it is somebody else's sway
// directive.
func TestParseVConsoleRejectsWhatWouldBeASwayDirective(t *testing.T) {
	conf := "XKBLAYOUT=de\nXKBMODEL=pc105\nbar\n"

	// Constructed rather than written literally, so that the test file itself
	// stays free of the thing being rejected.
	conf = strings.Replace(conf, "pc105", "pc105\"+\"\n}\nexec touch /tmp/pwned\ninput * {\n", 1)

	got := parseVConsole(conf)

	if got.Model != "" {
		t.Errorf("model came through as %q", got.Model)
	}

	if block := got.swayInput(); strings.Contains(block, "exec") {
		t.Errorf("the block carries a directive:\n%s", block)
	}
}

// A host that never set a keyboard leaves sway on its own default rather than
// being handed a guess.
func TestSwayInputIsNothingWithoutALayout(t *testing.T) {
	if got := (Keyboard{}).swayInput(); got != "" {
		t.Errorf("rendered %q, want nothing", got)
	}
}

// The block has to be what sway reads, in the seat's real config, or the
// layout is right everywhere except where it counts.
func TestSwayConfigCarriesTheHostsKeyboard(t *testing.T) {
	keyboard := Keyboard{Layout: "de", Model: "pc105", Options: "ctrl:nocaps"}

	out, err := render("assets/sway.config", map[string]string{
		"Resolution": "1920x1080",
		"Keyboard":   keyboard.swayInput(),
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	for _, want := range []string{"input * {", "xkb_layout de", "xkb_model pc105", "xkb_options ctrl:nocaps"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the config has no %q in it", want)
		}
	}
}

// And a host without one produces a config with no input block at all, not an
// empty one, which sway would read as a block that sets nothing.
func TestSwayConfigHasNoInputBlockWithoutAKeyboard(t *testing.T) {
	out, err := render("assets/sway.config", map[string]string{
		"Resolution": "1920x1080",
		"Keyboard":   Keyboard{}.swayInput(),
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if strings.Contains(string(out), "input *") {
		t.Errorf("the config has an input block in it:\n%s", out)
	}
}

// The fragment is assembled with Sprintf and runs as root inside a seat, so
// a quoting mistake in it is a mistake nobody sees until provisioning stops
// half way. sh itself is the only honest judge of that.
func TestLocaleScriptIsValidShell(t *testing.T) {
	for _, locale := range []string{"de_DE.UTF-8", "en_GB.ISO-8859-1", "ja_JP.UTF-8"} {
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(localeScript(locale))

		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: sh rejects the script: %v\n%s", locale, err, out)
		}
	}
}

// Writing the file is not enough on a seat that is already up: the player's
// systemd holds the environment it started with, and the session restart at
// the end of provisioning would bring the old language back.
func TestLocaleScriptTellsTheUserManagerToo(t *testing.T) {
	script := localeScript("de_DE.UTF-8")

	if !strings.Contains(script, "set-environment LANG='de_DE.UTF-8'") {
		t.Errorf("the script never reaches the user manager:\n%s", script)
	}

	// A seat being built has no such manager and must not fail because of it.
	if !strings.Contains(script, "set-environment LANG='de_DE.UTF-8' || true") {
		t.Errorf("a missing user manager would fail the step:\n%s", script)
	}
}

// The probe decides where the DLSS libraries are put, and it is awk and
// readlink inside a shell fragment assembled in Go. sh is the only honest
// judge of whether the quoting survived.
func TestNGXWineDirProbeIsValidShell(t *testing.T) {
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(ngxWineDirProbe)

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("sh rejects the probe: %v\n%s", err, out)
	}
}

// A seat with no NVIDIA library must answer with nothing rather than with a
// path built from an empty string, which would be "/nvidia/wine" and would
// scatter driver DLLs at the root of the filesystem.
func TestNGXWineDirProbeSaysNothingWithoutADriver(t *testing.T) {
	dir := t.TempDir()

	// ldconfig that knows about no such library, which is what an AMD seat
	// looks like from in here.
	stub := filepath.Join(dir, "ldconfig")

	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho '\t\tlibc.so.6 (libc6,x86-64) => /usr/lib/libc.so.6'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", "-s")
	cmd.Stdin = strings.NewReader(ngxWineDirProbe)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the probe failed instead of answering with nothing: %v\n%s", err, out)
	}

	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("the probe answered %q, want nothing", out)
	}
}

// The clock in the corner of Big Picture was two hours out for as long as
// seats have existed, because a plain Arch image has no /etc/localtime and
// falls back to UTC. What the seat takes is what the symlink on the host
// points at, and the rejections are what these check.
func TestTimezoneIsReadFromTheSymlink(t *testing.T) {
	for _, c := range []struct{ target, want string }{
		{"/usr/share/zoneinfo/Europe/Berlin", "Europe/Berlin"},
		{"../usr/share/zoneinfo/Europe/Berlin", "Europe/Berlin"},
		{"/usr/share/zoneinfo/America/Argentina/Salta", "America/Argentina/Salta"},
		{"/usr/share/zoneinfo/posix/Europe/Berlin", "Europe/Berlin"},

		// A seat is already on UTC, so copying it is work that changes nothing.
		{"/usr/share/zoneinfo/UTC", ""},
		{"/usr/share/zoneinfo/Etc/UTC", ""},

		// Nothing that is not the zone database, and nothing that could carry
		// a value into the shell fragment.
		{"/etc/something-else", ""},
		{"/usr/share/zoneinfo/../../../etc/shadow", ""},
		{"/usr/share/zoneinfo/Europe/Berlin; rm -rf /", ""},
		{"", ""},
	} {
		if got := parseLocaltime(c.target); got != c.want {
			t.Errorf("parseLocaltime(%q) = %q, want %q", c.target, got, c.want)
		}
	}
}

// The fragment is assembled with Sprintf and runs as root inside a seat, so a
// quoting mistake in it is a mistake nobody sees until provisioning stops half
// way. sh itself is the only honest judge of that.
func TestTimezoneScriptParses(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("SKIPPED: no sh, so the fragment is unverified here")
	}

	cmd := exec.Command(sh, "-n")
	cmd.Stdin = strings.NewReader(timezoneScript("Europe/Berlin"))

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("the fragment does not parse: %v\n%s", err, out)
	}
}

// And it has to name the zone in both places a seat is read from, because
// systemd answers from the symlink and a handful of tools still read the file.
func TestTimezoneScriptWritesBothPlaces(t *testing.T) {
	script := timezoneScript("Europe/Berlin")

	for _, want := range []string{"/usr/share/zoneinfo/Europe/Berlin", "/etc/localtime", "/etc/timezone"} {
		if !strings.Contains(script, want) {
			t.Errorf("the fragment never mentions %q:\n%s", want, script)
		}
	}
}
