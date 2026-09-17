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
		got, err := pickTool(tag, isa, ".tar.xz", assets)
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

	if _, err := pickTool(tag, "riscv64", ".tar.xz", assets); err == nil {
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

	if _, err := pickTool(tag, "x86_64", ".tar.xz", without); err == nil {
		t.Error("an archive with no published checksum was accepted")
	}
}

// The order in the script is the whole of its safety. Checked as text because
// the alternative is running a third of a gigabyte through it to find out.
func TestProtonScriptVerifiesBeforeItReplaces(t *testing.T) {
	script := cachyOS.script("https://example.invalid/proton.tar.xz", strings.Repeat("a", 128), "tag-1")

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

	if at("mv \".polyseat-new-proton-cachyos\"") < at("tar -xJf") {
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

// geAssets reads a real GE release listing, for the same reason protonAssets
// reads one: the trap is two architectures whose names differ by a suffix.
func geAssets(t *testing.T) (string, []struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
},
) {
	t.Helper()

	body, err := os.ReadFile("testdata/ge-release.json")
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

// GE publishes an x86_64 build and an aarch64 one, and the seat has to be given
// the first. The extension is the other half: this project ships gzip where the
// other ships xz, and asking for the wrong one finds no asset at all rather
// than the wrong one, which is the better of the two ways to be wrong.
func TestGEPicksTheX86Build(t *testing.T) {
	tag, assets := geAssets(t)

	got, err := pickTool(tag, "x86_64", ".tar.gz", assets)
	if err != nil {
		t.Fatalf("no build was picked out of a real GE release: %v", err)
	}

	if !strings.HasSuffix(got.url, "-x86_64.tar.gz") {
		t.Errorf("picked %q, which is not the x86_64 archive", got.url)
	}

	if !strings.HasSuffix(got.sum, "-x86_64.sha512sum") {
		t.Errorf("the checksum is %q, which does not belong to the archive", got.sum)
	}

	if _, err := pickTool(tag, "x86_64", ".tar.xz", assets); err == nil {
		t.Error("an xz archive was found in a release that publishes gzip, so the " +
			"extension is not being looked at")
	}
}

// The two tools are unpacked by the same script, and the flag that says how is
// the one difference between them that no test could catch afterwards: a wrong
// one is half a gigabyte fetched and then "tar: unrecognized archive format".
func TestEachToolIsUnpackedTheWayItIsPublished(t *testing.T) {
	for _, tc := range []struct {
		tool tool
		want string
	}{
		{cachyOS, "tar -xJf"},
		{geProton, "tar -xzf"},
	} {
		script := tc.tool.script("https://example.invalid/archive", strings.Repeat("a", 128), "tag-1")

		if !strings.Contains(script, tc.want) {
			t.Errorf("%s is unpacked without %q:\n%s", tc.tool.name, tc.want, script)
		}
	}

	// And they must not unpack into each other's working directory, which is
	// what a seat updating both at once would do with one shared name.
	cachy := cachyOS.script("https://example.invalid/a", strings.Repeat("a", 128), "tag-1")
	ge := geProton.script("https://example.invalid/b", strings.Repeat("b", 128), "tag-2")

	if strings.Contains(cachy, geProton.name+".tar") || strings.Contains(ge, cachyOS.name+".tar") {
		t.Error("the two tools share a working file, so updating both at once has them " +
			"writing over each other")
	}
}

// Steam records the chosen tool by the identity in this file, so the identity
// has to survive an update while the name in the menu says which build it is.
// GE names its releases weekly; a seat that took the identity from the tag
// would list a year of them and lose every per game setting on each update.
func TestEachToolKeepsItsIdentityAcrossReleases(t *testing.T) {
	for _, tc := range []struct {
		tool  tool
		tag   string
		shown string
	}{
		{cachyOS, "cachyos-11.0-20260703-slr", "Proton CachyOS 11.0-20260703"},
		{geProton, "GE-Proton11-7", "GE-Proton11-7"},
	} {
		manifest := tc.tool.manifest(tc.tag)

		if !strings.Contains(manifest, `"`+tc.tool.name+`"`) {
			t.Errorf("the manifest does not identify the tool as %q:\n%s", tc.tool.name, manifest)
		}

		if strings.Contains(manifest, `"install_path" "`+tc.tag) {
			t.Errorf("the tag leaked into the identity:\n%s", manifest)
		}

		if !strings.Contains(manifest, `"display_name" "`+tc.shown+`"`) {
			t.Errorf("the menu would not say which build this is, want %q:\n%s", tc.shown, manifest)
		}
	}
}

// A seat is given GE only when it asks, and the step is what enforces that.
// Ordering matters as much: it runs after the Proton step, which is after the
// user exists, because both write into the same directory and the second one
// would otherwise be deciding things about a seat that has no player yet.
func TestGEProtonIsProvisionedAfterProton(t *testing.T) {
	var proton, ge int

	names := []string{}

	for i, step := range Steps() {
		names = append(names, step.Name)

		switch step.Name {
		case "proton":
			proton = i
		case "ge-proton":
			ge = i
		}
	}

	if ge == 0 {
		t.Fatalf("there is no GE-Proton step at all: %v", names)
	}

	if ge < proton {
		t.Errorf("GE-Proton is installed before Proton CachyOS: %v", names)
	}
}
