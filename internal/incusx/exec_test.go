package incusx

import "testing"

// A command run as the player through sudo keeps the directory it was started
// in, and root's home is not one the player can enter. find ... -exec {} +
// needs to get back to it and runs nothing when it cannot, which emptied the
// scan for Steam's own desktop entries and doubled games in the launcher.
func TestCommandsStartInTheRoot(t *testing.T) {
	if cwd := execRequest([]string{"true"}).Cwd; cwd != "/" {
		t.Errorf("commands start in %q, which the player may not be able to enter", cwd)
	}
}
