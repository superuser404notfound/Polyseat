package seat

import (
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
