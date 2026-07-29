package library

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// reflinkDir returns a scratch directory on a filesystem that can clone.
//
// Not simply t.TempDir(): on this machine and on most others /tmp is tmpfs,
// which has no reflinks, so every test in this file would skip and the package
// would report itself green while testing nothing. The candidates are tried in
// turn and the skip at the end is deliberately loud.
func reflinkDir(t *testing.T) string {
	t.Helper()

	candidates := []string{os.TempDir(), "/var/tmp"}
	if dir := os.Getenv("POLYSEAT_TEST_DIR"); dir != "" {
		candidates = append([]string{dir}, candidates...)
	}

	for _, base := range candidates {
		dir, err := os.MkdirTemp(base, "polyseat-library-")
		if err != nil {
			continue
		}

		if err := SupportsReflink(dir); err != nil {
			os.RemoveAll(dir)

			continue
		}

		t.Cleanup(func() { os.RemoveAll(dir) })

		return dir
	}

	t.Skipf("none of %v is on a filesystem that supports reflinks, so this test "+
		"cannot check the one thing the library package exists to do. Set "+
		"POLYSEAT_TEST_DIR to a directory on btrfs or on XFS with reflink=1.", candidates)

	return ""
}

func TestSupportsReflink(t *testing.T) {
	if err := SupportsReflink(reflinkDir(t)); err != nil {
		t.Errorf("the scratch directory does not support reflinks: %v", err)
	}

	// The negative case matters more than the positive one. If this probe
	// answered yes everywhere, the daemon would happily build a pool on a
	// filesystem that duplicates every byte and only say so when the disk
	// filled up.
	if _, err := os.Stat("/dev/shm"); err != nil {
		t.Skip("no /dev/shm to use as a filesystem without reflinks")
	}

	dir, err := os.MkdirTemp("/dev/shm", "polyseat-noreflink-")
	if err != nil {
		t.Skipf("cannot write to /dev/shm: %v", err)
	}

	defer os.RemoveAll(dir)

	if err := SupportsReflink(dir); err == nil {
		t.Error("tmpfs was reported as supporting reflinks")
	}
}

// TestCloneSharesBlocks is the claim the whole milestone rests on.
//
// The assertion is that no file fell back to a full copy, which is a sound
// proof rather than a proxy: FICLONE either shares extents or returns an
// error, and there is no third outcome. Sabotaging the ioctl makes this test
// fail, which is how it was checked.
//
// There is deliberately no second, independent measurement here, and the
// reason is worth writing down. Two were tried and both were worthless. Free
// space via statfs does not move on this filesystem when a 64 MiB file is
// written, verified outside the test harness, so a full copy passed the check.
// A FIEMAP reader written for the purpose reported identical physical extents
// for a reflink and for a copy that filefrag showed landing in different
// places, so it was measuring nothing.
//
// Block sharing itself was confirmed by hand with filefrag, which is a tool
// nobody in this project wrote: an original and its reflink both report
// physical offset 25830012 with the shared flag set, while cp --reflink=never
// of the same file lands at 25845904 with no flag. Note that du reports the
// full size for both, so it cannot be used to see this.
func TestCloneSharesBlocks(t *testing.T) {
	dir := reflinkDir(t)

	const size = 64 << 20 // large enough that a fallback to copying would be obvious

	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}

	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(src, "game.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Clone(src, filepath.Join(dir, "dst"), Keep)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}

	if result.Copied != 0 {
		t.Fatalf("%d files fell back to a full copy, so nothing was shared", result.Copied)
	}

	if result.Files != 1 || result.Bytes != size {
		t.Errorf("cloned %d files and %d bytes, want 1 and %d", result.Files, result.Bytes, size)
	}

	same, err := os.ReadFile(filepath.Join(dir, "dst", "game.bin"))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(same, payload) {
		t.Fatal("the clone does not have the same contents as the original")
	}
}

func TestCloneTree(t *testing.T) {
	dir := reflinkDir(t)
	src := filepath.Join(dir, "src")

	mkdirs(t, filepath.Join(src, "bin"), filepath.Join(src, "data", "maps"))
	write(t, filepath.Join(src, "bin", "game"), "executable", 0o755)
	write(t, filepath.Join(src, "data", "maps", "one.pak"), "map data", 0o644)
	write(t, filepath.Join(src, "readme.txt"), "hello", 0o644)

	if err := os.Symlink("../bin/game", filepath.Join(src, "data", "link")); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "dst")

	result, err := Clone(src, dst, Keep)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}

	if result.Files != 3 || result.Dirs != 3 || result.Symlinks != 1 {
		t.Errorf("counted %d files, %d dirs, %d symlinks, want 3, 3, 1",
			result.Files, result.Dirs, result.Symlinks)
	}

	if got := read(t, filepath.Join(dst, "data", "maps", "one.pak")); got != "map data" {
		t.Errorf("nested file reads %q", got)
	}

	// The executable bit is not cosmetic. A game whose binary arrives without
	// it is installed, listed and unplayable, which is a confusing failure.
	info, err := os.Stat(filepath.Join(dst, "bin", "game"))
	if err != nil {
		t.Fatal(err)
	}

	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("the executable came out as %o, want 755", perm)
	}

	// The symlink has to survive as a symlink. Following it during the clone
	// would turn every one into a full copy, and games ship trees full of them.
	target, err := os.Readlink(filepath.Join(dst, "data", "link"))
	if err != nil {
		t.Fatalf("the symlink did not survive as one: %v", err)
	}

	if target != "../bin/game" {
		t.Errorf("the symlink points at %q, want ../bin/game", target)
	}
}

func TestCloneReplacesWhatWasThere(t *testing.T) {
	dir := reflinkDir(t)

	src := filepath.Join(dir, "src")
	mkdirs(t, src)
	write(t, filepath.Join(src, "new.txt"), "new", 0o644)

	dst := filepath.Join(dir, "dst")
	mkdirs(t, dst)
	write(t, filepath.Join(dst, "old.txt"), "old", 0o644)

	if _, err := Clone(src, dst, Keep); err != nil {
		t.Fatalf("Clone: %v", err)
	}

	if got := read(t, filepath.Join(dst, "new.txt")); got != "new" {
		t.Errorf("the new file reads %q", got)
	}

	// A clone that merged into the destination would leave this behind, and a
	// game directory that is a mixture of two builds is worse than either.
	if _, err := os.Stat(filepath.Join(dst, "old.txt")); err == nil {
		t.Error("a file from the previous contents survived the clone")
	}

	// Nothing may be left lying around under a temporary name.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if name := e.Name(); name != "src" && name != "dst" {
			t.Errorf("the clone left %q behind", name)
		}
	}
}

func TestCloneRefusesSpecialFiles(t *testing.T) {
	dir := reflinkDir(t)
	src := filepath.Join(dir, "src")

	mkdirs(t, src)

	if err := unix.Mkfifo(filepath.Join(src, "pipe"), 0o644); err != nil {
		t.Skipf("cannot create a fifo here: %v", err)
	}

	if _, err := Clone(src, filepath.Join(dir, "dst"), Keep); err == nil {
		t.Error("a fifo was cloned rather than refused")
	}

	// The refusal must not leave a partial tree behind.
	if _, err := os.Stat(filepath.Join(dir, "dst")); err == nil {
		t.Error("the failed clone left a destination directory behind")
	}
}

func TestTreeSize(t *testing.T) {
	dir := reflinkDir(t)

	mkdirs(t, filepath.Join(dir, "tree", "sub"))
	write(t, filepath.Join(dir, "tree", "a"), "12345", 0o644)
	write(t, filepath.Join(dir, "tree", "sub", "b"), "678", 0o644)

	size, err := TreeSize(filepath.Join(dir, "tree"))
	if err != nil {
		t.Fatal(err)
	}

	if size != 8 {
		t.Errorf("TreeSize = %d, want 8", size)
	}

	// A missing tree is zero rather than an error, because the caller asks
	// about seats that may not have been built yet.
	size, err = TreeSize(filepath.Join(dir, "nothing-here"))
	if err != nil || size != 0 {
		t.Errorf("TreeSize on a missing tree = %d, %v, want 0, nil", size, err)
	}
}

func TestSafeName(t *testing.T) {
	for _, name := range []string{"Half-Life", "Proton - Experimental", "appmanifest_4.acf", "a b c"} {
		if err := safeName(name); err != nil {
			t.Errorf("safeName(%q) = %v, want nil", name, err)
		}
	}

	// These come out of files Steam writes, which is to say out of a game's own
	// metadata, and they get joined onto paths the daemon writes to as root.
	invalid := map[string]string{
		"..":            "the parent directory",
		".":             "the directory itself",
		"":              "empty",
		"../../etc":     "escapes upwards",
		"a/b":           "a separator",
		`a\b`:           "a backslash, which some manifests carry from Windows",
		".hidden":       "a leading dot, which would collide with the staging directories",
		"/etc/passwd":   "absolute",
		"..\\..\\hosts": "escapes upwards with backslashes",
	}

	for name, why := range invalid {
		if err := safeName(name); err == nil {
			t.Errorf("safeName(%q) accepted it (%s)", name, why)
		}
	}
}

// -------------------------------------------------------------------- helpers

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()

	for _, path := range paths {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}

	// WriteFile only applies the mode when it creates the file, and it is
	// subject to the umask either way.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

// TestCloneRootIsUsable checks the tree's own directory, not only its contents.
//
// This is the test that was missing, and its absence made the whole feature
// fail while every other test passed. Clone builds the tree under a temporary
// name from os.MkdirTemp, which creates 0700 owned by whoever is running. The
// mode and ownership were applied to everything inside the tree and never to
// the tree itself, so a game directory reached a seat as root with no
// permissions for anybody else: files present, sizes right, blocks shared, and
// the player inside the container unable to open any of it.
//
// Nothing caught it because these tests run as one user, and 0700 is fully
// accessible to its own owner. Checking the mode against the source is what
// makes the difference visible without a second uid.
func TestCloneRootIsUsable(t *testing.T) {
	dir := reflinkDir(t)
	src := filepath.Join(dir, "src")

	mkdirs(t, filepath.Join(src, "sub"))
	write(t, filepath.Join(src, "sub", "file"), "content", 0o644)

	// Explicit rather than whatever the umask produced, so the comparison below
	// is against a known value.
	if err := os.Chmod(src, 0o755); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "dst")

	if _, err := Clone(src, dst, Keep); err != nil {
		t.Fatalf("Clone: %v", err)
	}

	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}

	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("the cloned tree's own directory is %o, want 755 like the source. "+
			"A game directory with this mode is unreadable to the user inside the "+
			"seat, however correct its contents are", perm)
	}

	// The same for a directory one level down, which is where the contents walk
	// does reach and so has always been right.
	if info, err := os.Stat(filepath.Join(dst, "sub")); err != nil {
		t.Fatal(err)
	} else if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("the nested directory is %o, want 755", perm)
	}
}

// TestCloneRootOwnership is the other half of the bug above, and it needs a
// second uid to hand out, so it only runs as root.
//
// Skipped rather than dropped: the mode check above would not have caught an
// ownership that was applied to the contents and not to the root, and that was
// the half that made the directories belong to nobody inside the container.
func TestCloneRootOwnership(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to chown a tree to a uid that is not the caller's, so " +
			"the ownership half of the root fixup is unchecked here. Run the " +
			"package as root to cover it.")
	}

	dir := reflinkDir(t)
	src := filepath.Join(dir, "src")

	mkdirs(t, filepath.Join(src, "sub"))
	write(t, filepath.Join(src, "sub", "file"), "content", 0o644)

	const uid, gid = 1001000, 1001000

	if _, err := Clone(src, filepath.Join(dir, "dst"), Owner{UID: uid, GID: gid}); err != nil {
		t.Fatalf("Clone: %v", err)
	}

	for _, path := range []string{"dst", "dst/sub", "dst/sub/file"} {
		info, err := os.Stat(filepath.Join(dir, path))
		if err != nil {
			t.Fatal(err)
		}

		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatal("no stat information")
		}

		if int(stat.Uid) != uid || int(stat.Gid) != gid {
			t.Errorf("%s is owned by %d:%d, want %d:%d", path, stat.Uid, stat.Gid, uid, gid)
		}
	}
}
