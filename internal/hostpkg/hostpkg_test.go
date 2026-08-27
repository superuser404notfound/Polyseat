// The table is checked against files rather than against the machine the test
// runs on, which is the only way the Debian and Fedora rows are checked at all:
// this is developed on Arch and CI runs on Ubuntu, so two of the three rows
// would otherwise be read by nobody until somebody installed Polyseat on them.
package hostpkg

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// withOSRelease points the detector at a file holding what a distribution says
// about itself, and stops it from falling back to the binaries on this machine.
//
// Both are needed together. Without the second, a test asserting that an
// unrecognised os-release comes out Unknown would pass on CI and fail here,
// where pacman is on PATH and the fallback finds it.
func withOSRelease(t *testing.T, content string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "os-release")

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	oldPath, oldLook := osReleasePath, lookPath
	osReleasePath = path
	lookPath = func(string) (string, error) { return "", errors.New("no package manager on this test's PATH") }

	t.Cleanup(func() { osReleasePath, lookPath = oldPath, oldLook })
}

func TestDetectReadsRealOSReleaseLines(t *testing.T) {
	// Taken from the real files rather than written to suit the parser. The
	// quoting differs between distributions and that is exactly what the parser
	// has to survive: Debian quotes ID_LIKE, Arch omits it entirely, and Fedora
	// quotes some fields and not others.
	for _, c := range []struct {
		name    string
		content string
		want    Family
	}{
		{
			"Debian 13",
			"PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nNAME=\"Debian GNU/Linux\"\nID=debian\nVERSION_ID=\"13\"\n",
			Debian,
		},
		{
			// The one that has to come out Debian through ID_LIKE, because its
			// own ID says nothing about apt.
			"Ubuntu 24.04",
			"NAME=\"Ubuntu\"\nID=ubuntu\nID_LIKE=debian\nVERSION_ID=\"24.04\"\n",
			Debian,
		},
		{
			"Fedora 42",
			"NAME=\"Fedora Linux\"\nID=fedora\nVERSION_ID=42\n",
			Fedora,
		},
		{
			"Arch",
			"NAME=\"Arch Linux\"\nID=arch\nBUILD_ID=rolling\n",
			Arch,
		},
		{
			// The machine this is developed on, and the reason ID_LIKE is read
			// at all: it says cachyos and nothing else would place it.
			"CachyOS",
			"NAME=\"CachyOS Linux\"\nID=cachyos\nID_LIKE=\"arch\"\n",
			Arch,
		},
		{
			"EndeavourOS",
			"NAME=\"EndeavourOS\"\nID=endeavouros\nID_LIKE=\"arch\"\n",
			Arch,
		},
		{
			"Linux Mint",
			"NAME=\"Linux Mint\"\nID=linuxmint\nID_LIKE=\"ubuntu debian\"\n",
			Debian,
		},
		{
			"Rocky Linux",
			"NAME=\"Rocky Linux\"\nID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\n",
			Fedora,
		},
		{
			// Not a refusal to support openSUSE so much as a statement that
			// nobody has written the zypper row. Unknown is the honest answer
			// and every caller treats it as "cannot install a release here".
			"openSUSE Tumbleweed",
			"NAME=\"openSUSE Tumbleweed\"\nID=\"opensuse-tumbleweed\"\nID_LIKE=\"opensuse suse\"\n",
			Unknown,
		},
		{
			"an empty file",
			"",
			Unknown,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			withOSRelease(t, c.content)

			if got := Detect(); got != c.want {
				t.Errorf("%s came out %q, wanted %q", c.name, got, c.want)
			}
		})
	}
}

func TestDetectFallsBackToWhatIsOnPath(t *testing.T) {
	// A machine with no os-release worth reading is still a machine with a
	// package manager, and refusing it over a missing file would be pedantry.
	oldPath, oldLook := osReleasePath, lookPath
	t.Cleanup(func() { osReleasePath, lookPath = oldPath, oldLook })

	osReleasePath = filepath.Join(t.TempDir(), "there-is-no-such-file")

	for _, c := range []struct {
		bin  string
		want Family
	}{
		{"pacman", Arch},
		{"apt-get", Debian},
		{"dnf", Fedora},
	} {
		t.Run(c.bin, func(t *testing.T) {
			lookPath = func(name string) (string, error) {
				if name == c.bin {
					return "/usr/bin/" + name, nil
				}

				return "", errors.New("not here")
			}

			if got := Detect(); got != c.want {
				t.Errorf("a machine with only %s came out %q, wanted %q", c.bin, got, c.want)
			}
		})
	}
}

func TestEveryKnownFamilyHasAnAssetAndUnknownHasNone(t *testing.T) {
	seen := map[string]Family{}

	for _, f := range []Family{Arch, Debian, Fedora} {
		name := f.Asset()

		if name == "" {
			t.Errorf("%q publishes no asset, so a host of that family could never update itself", f)

			continue
		}

		if other, ok := seen[name]; ok {
			t.Errorf("%q and %q both publish %q, so one would install the other's package", f, other, name)
		}

		seen[name] = f
	}

	// The property the whole of the Unknown path rests on. An asset name here
	// would match some release file and hand it to a package manager that does
	// not exist.
	if got := Unknown.Asset(); got != "" {
		t.Errorf("an unknown host publishes %q, and it must publish nothing", got)
	}
}

func TestAssetNamesCarryNoVersion(t *testing.T) {
	// releases/latest/download/<name> is a permanent link only while <name> is
	// permanent, which is why none of these carries a version and why the
	// documented curl commands never have to be edited. Every one of the three
	// package managers reads the real version out of the file instead.
	//
	// A bare digit is not the test: x86_64 and amd64 both have one and neither
	// is a version. Three numbers with dots between them is what a version
	// looks like, and that is what would have to appear here for the permanent
	// link to stop being permanent.
	version := regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+`)

	for _, f := range []Family{Arch, Debian, Fedora} {
		if version.MatchString(f.Asset()) {
			t.Errorf("%q publishes %q, which carries a version, so releases/latest/download would break on the next release", f, f.Asset())
		}
	}
}

func TestManagerAlwaysNamesSomething(t *testing.T) {
	// Used in error messages that a person reads. An empty one would produce
	// "so  has nothing to replace", which is a sentence with a hole in it.
	for _, f := range []Family{Arch, Debian, Fedora, Unknown} {
		if f.Manager() == "" {
			t.Errorf("%q has no name to use in a sentence", f)
		}
	}
}
