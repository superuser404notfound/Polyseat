package seat

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// protonAssets reads a real release listing. Handwritten test data would only
// prove that the code agrees with what I imagined a release looks like, and
// what makes this choice worth testing is that every release carries six
// archives whose names differ by a suffix.
func protonAssets(t *testing.T) (string, []struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
},
) {
	t.Helper()

	body, err := os.ReadFile("testdata/proton-release.json")
	if err != nil {
		t.Fatal(err)
	}

	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}

	if err := json.Unmarshal(body, &release); err != nil {
		t.Fatal(err)
	}

	return release.TagName, release.Assets
}

// The v3 archive and the baseline one differ by a suffix, and matching on
// "contains x86_64" would hand a processor that cannot run the optimised build
// exactly that build. What the seat gets then is every game dying on an illegal
// instruction.
func TestProtonPicksTheBuildTheProcessorCanRun(t *testing.T) {
	tag, assets := protonAssets(t)

	for _, isa := range []string{"x86_64", "x86_64_v3"} {
		got, err := pickProton(tag, isa, assets)
		if err != nil {
			t.Fatalf("%s: %v", isa, err)
		}

		if !strings.HasSuffix(got.url, "-"+isa+".tar.xz") {
			t.Errorf("%s: picked %s", isa, got.url)
		}

		if !strings.HasSuffix(got.sum, "-"+isa+".sha512sum") {
			t.Errorf("%s: checksum is %s, which belongs to another build", isa, got.sum)
		}

		if got.tag != tag || got.size == 0 {
			t.Errorf("%s: tag %q size %d", isa, got.tag, got.size)
		}
	}
}

// An architecture this release does not carry has to be an error rather than
// the nearest thing, and an archive with no checksum has to be refused rather
// than installed unverified.
func TestProtonRefusesWhatItCannotVerify(t *testing.T) {
	tag, assets := protonAssets(t)

	if _, err := pickProton(tag, "riscv64", assets); err == nil {
		t.Error("an architecture that is not in the release was accepted")
	}

	var without []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
		URL  string `json:"browser_download_url"`
	}

	for _, asset := range assets {
		if !strings.HasSuffix(asset.Name, ".sha512sum") {
			without = append(without, asset)
		}
	}

	if _, err := pickProton(tag, "x86_64", without); err == nil {
		t.Error("an archive with no published checksum was accepted")
	}
}

// The order in the script is the whole of its safety. Checked as text because
// the alternative is running a third of a gigabyte through it to find out.
func TestProtonScriptVerifiesBeforeItReplaces(t *testing.T) {
	script := protonScript("https://example.invalid/proton.tar.xz", strings.Repeat("a", 128), "tag-1")

	at := func(needle string) int {
		i := strings.Index(script, needle)
		if i < 0 {
			t.Fatalf("the script no longer contains %q, so this test proves nothing:\n%s", needle, script)
		}

		return i
	}

	if at("sha512sum -c -") < at("curl") {
		t.Error("the checksum is compared before the download happens")
	}

	if at("tar -xJf") < at("sha512sum -c -") {
		t.Error("the archive is unpacked before its checksum is checked")
	}

	if at("mv .polyseat-new") < at("tar -xJf") {
		t.Error("the new tool is moved into place before it is unpacked")
	}

	// set -e is what makes the order mean anything: without it every command
	// runs whatever the one before it decided.
	if !strings.HasPrefix(script, "set -e\n") {
		t.Error("the script does not stop at the first failure, so a bad download would still be installed")
	}
}

// The checksum file holds a hash and a filename, and what it holds instead when
// something went wrong is usually an error page. Whatever comes out of here
// goes into a shell command.
func TestProtonChecksumIsReadStrictly(t *testing.T) {
	good := strings.Repeat("ab", 64)

	for name, tc := range map[string]struct {
		body string
		want string
	}{
		// The real published form, two spaces between the fields.
		"as published":   {good + "  proton-cachyos-11.0-slr-x86_64_v3.tar.xz\n", good},
		"nothing":        {"", ""},
		"an error page":  {"<html>404 not found</html>", ""},
		"a short hash":   {"abcdef  proton.tar.xz", ""},
		"not hex at all": {strings.Repeat("z", 128) + "  proton.tar.xz", ""},
		"a hash alone":   {good, ""},
	} {
		hash, err := parseChecksum(tc.body)

		if tc.want == "" {
			if err == nil {
				t.Errorf("%s: accepted, and %q would have gone into a shell command", name, hash)
			}

			continue
		}

		if err != nil {
			t.Errorf("%s: %v", name, err)
		}

		if hash != tc.want {
			t.Errorf("%s: read %q, want %q", name, hash, tc.want)
		}
	}
}

// From the processor this runs on, which does have the whole set, and from the
// same file with one flag of the set removed. A check for AVX2 alone would pass
// the second and hand that machine a build it cannot execute.
func TestProtonDetectsTheInstructionSetLevel(t *testing.T) {
	body, err := os.ReadFile("testdata/cpuinfo-v3.txt")
	if err != nil {
		t.Fatal(err)
	}

	cpuinfo := string(body)

	if !supportsV3(cpuinfo) {
		t.Fatalf("this processor was not recognised as x86-64-v3:\n%s", cpuinfo)
	}

	for _, flag := range []string{"avx2", "bmi2", "f16c", "fma", "movbe"} {
		crippled := strings.Replace(cpuinfo, " "+flag+" ", " ", 1)

		if crippled == cpuinfo {
			t.Fatalf("%s is not in the test data, so removing it proves nothing", flag)
		}

		if supportsV3(crippled) {
			t.Errorf("a processor without %s was still offered the optimised build", flag)
		}
	}

	if supportsV3("") || supportsV3("model name\t: something\n") {
		t.Error("a cpuinfo with no flags line was treated as capable")
	}
}

// The part of the Proton step that sets the seat's default writes into the
// player's home and needs their uid. Ordered before the step that creates that
// user, it asks a container which has no such user yet, and the whole
// provisioning run of a seat being built for the first time fails on it. An
// existing seat never shows it, because it already has the user, which is
// exactly the kind of ordering that survives every test done on a machine that
// has been running for a while.
func TestProtonIsProvisionedAfterThereIsAUser(t *testing.T) {
	user, proton := -1, -1

	for i, step := range Steps() {
		switch step.Name {
		case "user":
			user = i
		case "proton":
			proton = i
		}
	}

	if user < 0 || proton < 0 {
		t.Fatalf("the recipe no longer has both steps: user at %d, proton at %d", user, proton)
	}

	if proton < user {
		t.Errorf("proton is step %d and the user is created at step %d", proton, user)
	}
}
