package seat

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pictureHelper runs one of the seat's picture helpers the way the daemon does,
// as a program with a home of its own, and returns what it printed.
//
// seed prepares that home and returns the one item in the list that has
// something to find; pad is how many items with nothing in them go around it,
// which is how a list is made longer than a command line may be.
func pictureHelper(t *testing.T, asset_ string, seed func(home string) map[string]string,
	pad int, onArgv bool) map[string]string {
	t.Helper()

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("SKIPPED: no python3 to run the helper with, so its behaviour is unverified here")
	}

	if err := exec.Command(python, "-c", "import PIL").Run(); err != nil {
		t.Skip("SKIPPED: no Pillow here, which the helpers draw with, so they are unverified")
	}

	home := t.TempDir()

	script := filepath.Join(home, "helper.py")
	if err := os.WriteFile(script, asset(asset_), 0o755); err != nil {
		t.Fatal(err)
	}

	// Items with no key are skipped by both helpers before anything is looked
	// at, so they cost nothing but length: every one is a kilobyte.
	items := []map[string]string{seed(home)}
	filler := strings.Repeat("x", 1000)

	for i := 0; i < pad; i++ {
		items = append(items, map[string]string{"label": filler})
	}

	query, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(python, script)
	cmd.Env = append(os.Environ(), "HOME="+home)

	if onArgv {
		cmd.Args = append(cmd.Args, string(query))
	} else {
		cmd.Stdin = bytes.NewReader(query)
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the helper failed with a list of %d KiB: %v\n%s", len(query)>>10, err, stderr.String())
	}

	found := map[string]string{}
	if err := json.Unmarshal(out, &found); err != nil {
		t.Fatalf("the helper printed %q: %v", out, err)
	}

	return found
}

// An icon the helper can find without the network: Lutris puts one in the
// theme, named after the game's slug.
func seedLutrisIcon(t *testing.T) func(string) map[string]string {
	return func(home string) map[string]string {
		dir := filepath.Join(home, ".local/share/icons/hicolor/128x128/apps")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}

		png := filepath.Join(dir, "lutris_otter.png")
		if err := os.WriteFile(png, tinyPNG(t), 0o644); err != nil {
			t.Fatal(err)
		}

		return map[string]string{"key": "Otter", "lutris": "otter"}
	}
}

// A cover on disk the helper only has to convert, so no font and no network
// are involved.
func seedCover(t *testing.T) func(string) map[string]string {
	return func(home string) map[string]string {
		cover := filepath.Join(home, "cover.png")

		python, _ := exec.LookPath("python3")
		script := "from PIL import Image; Image.new('RGB', (600, 900), (20, 80, 160)).save('" + cover + "')"

		if out, err := exec.Command(python, "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("could not draw a cover: %v\n%s", err, out)
		}

		return map[string]string{"key": "Otter", "source": cover, "label": "Otter"}
	}
}

// tinyPNG is a one pixel PNG, which is all a theme lookup needs to exist.
func tinyPNG(t *testing.T) []byte {
	t.Helper()

	python, _ := exec.LookPath("python3")

	out, err := exec.Command(python, "-c",
		"import io, sys; from PIL import Image; b = io.BytesIO(); "+
			"Image.new('RGBA', (1, 1)).save(b, 'PNG'); sys.stdout.buffer.write(b.getvalue())").Output()
	if err != nil {
		t.Fatal(err)
	}

	return out
}

// Longer than the kernel lets one argument be, which is what a large library
// produced: the daemon's exec of the helper failed outright and every game lost
// its picture. On standard input the length does not matter.
const pastTheArgumentCeiling = 200

func TestIconsTakeAListLongerThanACommandLine(t *testing.T) {
	found := pictureHelper(t, "assets/icons.py", seedLutrisIcon(t), pastTheArgumentCeiling, false)

	if !strings.HasSuffix(found["Otter"], "lutris_otter.png") {
		t.Errorf("found %v, want the Lutris icon for Otter", found)
	}
}

func TestBoxartTakesAListLongerThanACommandLine(t *testing.T) {
	found := pictureHelper(t, "assets/boxart.py", seedCover(t), pastTheArgumentCeiling, false)

	if !strings.HasSuffix(found["Otter"], ".png") {
		t.Errorf("found %v, want a card for Otter", found)
	}
}

// A daemon from before the change still puts the list on the command line, and
// a seat rebuilt ahead of it has to keep its pictures.
func TestPictureHelpersStillReadTheCommandLine(t *testing.T) {
	if found := pictureHelper(t, "assets/icons.py", seedLutrisIcon(t), 0, true); found["Otter"] == "" {
		t.Errorf("polyseat-icons found %v from the command line", found)
	}

	if found := pictureHelper(t, "assets/boxart.py", seedCover(t), 0, true); found["Otter"] == "" {
		t.Errorf("polyseat-boxart found %v from the command line", found)
	}
}

// The other half of the same change: the daemon's side. A long list goes on
// standard input only, because on the command line it stops the helper from
// starting at all. A short one goes on both, for a seat whose helper still only
// reads the command line.
func TestSeatHelperListLeavesTheCommandLineWhenItIsLong(t *testing.T) {
	short := seatHelperArgv("polyseat-icons", []byte(`[{"key":"a"}]`))
	if short[len(short)-1] != `[{"key":"a"}]` {
		t.Errorf("a short list was not also put on the command line: %q", short)
	}

	long := seatHelperArgv("polyseat-icons", bytes.Repeat([]byte("x"), 128<<10))
	if last := long[len(long)-1]; last != "/usr/local/bin/polyseat-icons" {
		t.Errorf("a list of 128 KiB went on the command line, where the kernel refuses it: argv ends %.40q", last)
	}
}
