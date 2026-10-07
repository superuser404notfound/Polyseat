package seat

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The package a seat installs is named in three places that nothing but this
// ties together: the pin and the build number here, the recipe that builds it,
// and the checksum of what the recipe produced. Moving one and not the others
// does not fail at build time. It fails in a seat, as a download that 404s or
// a package that is refused, in the middle of provisioning.
func TestTheSunshinePackageIsTheOneTheRecipeBuilds(t *testing.T) {
	recipe, err := os.ReadFile("../../packaging/sunshine/PKGBUILD")
	if err != nil {
		t.Fatalf("the recipe is not where the source says it is: %v", err)
	}

	field := func(name string) string {
		m := regexp.MustCompile(`(?m)^` + name + `=(\S+)`).FindSubmatch(recipe)
		if m == nil {
			t.Fatalf("the recipe sets no %s", name)
		}

		return string(m[1])
	}

	if got := field("pkgver"); got != SunshinePin {
		t.Errorf("the recipe builds %q and seats are pinned to %q", got, SunshinePin)
	}

	if got := field("pkgrel"); got != SunshineBuild {
		t.Errorf("the recipe builds pkgrel %q and seats ask for %q", got, SunshineBuild)
	}

	// The patch is the whole reason this package exists.
	if !strings.Contains(string(recipe), "wlgrab-event-driven.patch") {
		t.Error("the recipe does not apply the capture patch")
	}

	if _, err := os.Stat("../../packaging/sunshine/wlgrab-event-driven.patch"); err != nil {
		t.Errorf("the patch the recipe names is missing: %v", err)
	}
}

func TestTheSunshinePackageHasAnAddressAndAChecksum(t *testing.T) {
	want := "https://github.com/superuser404notfound/Polyseat/releases/download/sunshine-" +
		SunshinePin + "-" + SunshineBuild + "/sunshine-" + SunshinePin + "-" + SunshineBuild + "-x86_64.pkg.tar.zst"

	if got := sunshinePackageURL(); got != want {
		t.Errorf("the package is fetched from %q, want %q", got, want)
	}

	// Lower case hex, which is what the comparison in stepSunshine produces. A
	// checksum pasted in upper case would refuse every download.
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(sunshinePackageSHA256) {
		t.Errorf("%q is not a sha256 the way stepSunshine writes one", sunshinePackageSHA256)
	}
}

// A seat built before 0.37.0 carries LizardByte's package of the same release.
// Compared on the release alone it looks current, and it is the one package
// that has to go.
func TestASeatOnTheUpstreamPackageIsNotCurrent(t *testing.T) {
	ours := SunshinePin + "-" + SunshineBuild

	for out, current := range map[string]bool{
		"sunshine " + ours:                        true,
		"sunshine " + SunshinePin + "-1":          false,
		"sunshine " + SunshinePin + "-1.10":       false,
		"error: package 'sunshine' was not found": false,
	} {
		if got := sunshineIsCurrent(out); got != current {
			t.Errorf("%q read as current=%v", out, got)
		}
	}

	// And the freshness report still reads our build as the pinned release.
	if got := installedSunshine("sunshine " + ours); got != SunshinePin {
		t.Errorf("a seat on our build reads back as %q rather than %q", got, SunshinePin)
	}
}
