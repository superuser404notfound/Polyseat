package seat

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// The broker is a helper the daemon starts, not a package, so the test reaches
// for the file the same way the update tests reach for the release workflow.
// It is here because internal/seat is what starts it: see startBroker.
const brokerPath = "../../spike/m2-input-broker/broker.py"

// The driver imports the helper and asks it one question. The alternative is a
// Go transcription of a binary format, which would only prove that the
// transcription agrees with itself.
const brokerDriver = `
import importlib.util, json, os, sys

path = sys.argv[1]
sys.path.insert(0, os.path.dirname(os.path.abspath(path)))

spec = importlib.util.spec_from_file_location("broker", path)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

print(json.dumps(module.acl_names_somebody(sys.argv[2])))
`

// namesSomebody asks the broker whether this file is open to anybody by name.
func namesSomebody(t *testing.T, path string) bool {
	t.Helper()

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("SKIPPED: no python3, so the broker's behaviour is unverified here")
	}

	if _, err := os.Stat(brokerPath); err != nil {
		t.Skip("SKIPPED: the broker is not beside this checkout")
	}

	out, err := exec.Command(python, "-c", brokerDriver, brokerPath, path).Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && len(exit.Stderr) > 0 {
			t.Skipf("SKIPPED: the broker could not be loaded here: %s", exit.Stderr)
		}

		t.Fatal(err)
	}

	var answer bool
	if err := json.Unmarshal(out, &answer); err != nil {
		t.Fatalf("the driver printed something unreadable: %s", out)
	}

	return answer
}

// acl puts an access list on a file, or skips the test where that cannot be
// done: a filesystem without them cannot carry the case being checked.
func acl(t *testing.T, path, spec string) {
	t.Helper()

	if _, err := exec.LookPath("setfacl"); err != nil {
		t.Skip("SKIPPED: no setfacl to build an access list with")
	}

	if out, err := exec.Command("setfacl", "-m", spec, path).CombinedOutput(); err != nil {
		t.Skipf("SKIPPED: an access list could not be written here: %s", out)
	}
}

func aFile(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "event99")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// The answer in the case that happens all the time: a sealed node, asked about
// twice a second for as long as a seat runs. It has no access list at all, and
// finding that out has to cost nothing.
func TestASealedNodeIsOpenToNobody(t *testing.T) {
	if namesSomebody(t, aFile(t)) {
		t.Error("a file with no access list was reported as open to somebody, so every device would be resealed on every pass")
	}
}

// The case the seal exists for. logind hands the desktop user exactly this
// through the uaccess tag, and it survives a chmod, which is how a seat's
// gamepad was readable by the host's Steam.
func TestANamedUserIsFound(t *testing.T) {
	path := aFile(t)
	acl(t, path, "u:"+strconv.Itoa(os.Getuid())+":rw")

	if !namesSomebody(t, path) {
		t.Error("a node handed to a named user looked sealed, so it would be left open")
	}
}

// A named group opens the node exactly as far, and the getfacl this replaced
// only ever looked at user lines.
func TestANamedGroupIsFoundToo(t *testing.T) {
	path := aFile(t)
	acl(t, path, "g:"+strconv.Itoa(os.Getgid())+":r")

	if !namesSomebody(t, path) {
		t.Error("a node handed to a named group looked sealed")
	}
}

// And an access list that names nobody is not somebody. This is the entry that
// tells the two apart: a mask makes the file carry a list, with only the owner,
// the owning group, the mask and everybody else in it, all of which the mode
// already covers.
func TestAListThatNamesNobodyIsNotSomebody(t *testing.T) {
	path := aFile(t)
	acl(t, path, "m::rw")

	if namesSomebody(t, path) {
		t.Error("an access list naming nobody was read as naming somebody, which reseals a sealed node twice a second forever")
	}
}

// Clearing it puts the node back to sealed, which is what the broker does when
// it finds one open. Without this the broker would report the same device as
// taken off the host on every pass.
func TestClearingTheListSettlesAgain(t *testing.T) {
	path := aFile(t)
	acl(t, path, "u:"+strconv.Itoa(os.Getuid())+":rw")

	if out, err := exec.Command("setfacl", "-b", path).CombinedOutput(); err != nil {
		t.Skipf("SKIPPED: the access list could not be cleared: %s", out)
	}

	if namesSomebody(t, path) {
		t.Error("a cleared access list still read as open to somebody")
	}
}
