package seat

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"reflect"
	"strings"
	"testing"
)

// The names a browser really sends. A file picked one at a time arrives as its
// own name; a folder arrives as one part per file, each carrying the path
// underneath the folder that was dropped, which is what a save or a mod is.
func TestADropKeepsTheFolderItCameIn(t *testing.T) {
	for _, want := range []string{
		"Eden-Linux-v0.2.1-amd64-clang-pgo.AppImage",
		"0100F2C0115B6000/01/00000001",
		"load/0100000000010000/Better Textures/romfs/actor.bfres",
		"Zelda (Europe) [rev 1].zip",
		"Pokémon.save",
	} {
		got, err := ValidateDropPath(want)
		if err != nil {
			t.Errorf("%q was refused: %v", want, err)

			continue
		}

		if got != want {
			t.Errorf("%q was changed to %q, and a file somebody cannot find again is worse than one that was refused", want, got)
		}
	}
}

// Everything this refuses, and the reason each one is here rather than left to
// the container to complain about.
func TestWhatADropMayNotBeCalled(t *testing.T) {
	cases := map[string]string{
		"":                     "a file with no name has nowhere to go",
		"/etc/passwd":          "an absolute path is a place, not a name, though the empty component rule catches this one too",
		"../../etc/passwd":     "the classic way out of a directory",
		"saves/../../../root":  "and the same thing further in, where it is easier to miss",
		"..":                   "on its own",
		".":                    "and this, which would be a write to the directory itself",
		"saves//deep":          "an empty component, which path.Dir would swallow",
		"saves/":               "a name that is only a folder",
		".ssh/authorized_keys": "hidden at the top, where the player would never see it",
		"bad\x00name":          "a NUL, which is where a path ends for the kernel and not for Go",
		"bad\nname":            "a newline, which would break the log line it appears in",
	}

	for name, why := range cases {
		if got, err := ValidateDropPath(name); err == nil {
			t.Errorf("%q was accepted as %q: %s", name, got, why)
		}
	}
}

// Length and depth are refused here so that the reason reaches the person, not
// as an errno from inside a container in the middle of three hundred files.
func TestADropIsRefusedBeforeTheFilesystemWould(t *testing.T) {
	long := strings.Repeat("a", maxDropSegment+1)
	if _, err := ValidateDropPath(long); err == nil {
		t.Errorf("a component of %d bytes was accepted, and the filesystem takes %d", len(long), maxDropSegment)
	}

	if _, err := ValidateDropPath(strings.Repeat("a", maxDropSegment)); err != nil {
		t.Errorf("a component of exactly %d bytes was refused: %v", maxDropSegment, err)
	}

	deep := strings.TrimSuffix(strings.Repeat("d/", maxDropDepth+1), "/")
	if _, err := ValidateDropPath(deep); err == nil {
		t.Error("a path deeper than the limit was accepted")
	}

	// Neither deep nor long in any one component, and still longer as a whole
	// than a path may be. Without its own limit this is the shape that reaches
	// the container and comes back as an errno.
	wide := strings.TrimSuffix(strings.Repeat(strings.Repeat("w", 200)+"/", 25), "/")
	if len(wide) <= maxDropPath {
		t.Fatalf("this test needs a path over %d bytes, and built one of %d", maxDropPath, len(wide))
	}

	if _, err := ValidateDropPath(wide); err == nil {
		t.Errorf("a path of %d bytes was accepted, and a path may be %d", len(wide), maxDropPath)
	}
}

// The property the whole check exists for. Whatever comes out of it, joined to
// the drop directory, is still inside the drop directory: a name that got past
// this is a write as root into somebody's container.
func TestNothingAcceptedLeadsOutOfTheDropDirectory(t *testing.T) {
	// Two of these are accepted and the rest are not. The test does not care
	// which: it says that an accepted one cannot reach outside, whatever the
	// answer for a particular name turns out to be.
	for _, name := range []string{
		"save.zip",
		"a/b/c.bin",
		"../escape",
		"a/../../escape",
		"a/./b",
		"/absolute",
		"....//....//escape",
		"a/b/../../../escape",
		strings.Repeat("../", 40) + "etc/shadow",
	} {
		rel, err := ValidateDropPath(name)
		if err != nil {
			continue
		}

		full := path.Clean(DropDir + "/" + rel)
		if !strings.HasPrefix(full, DropDir+"/") {
			t.Errorf("%q was accepted as %q and lands at %s, outside %s", name, rel, full, DropDir)
		}
	}
}

// A name that is not text at all. Go strings carry arbitrary bytes, and the
// path reaches Incus as a query parameter and the log as a line.
func TestADropHasToBeText(t *testing.T) {
	if _, err := ValidateDropPath("save\xff\xfe.bin"); err == nil {
		t.Error("a name that is not valid UTF-8 was accepted")
	}
}

// The log line is the only place the size of a drop is ever said, so it has to
// say the right thing for both shapes of one.
func TestTheLogLineNamesOneFileAndCountsSeveral(t *testing.T) {
	one := describeDrop("save.zip", Received{Files: 1, Bytes: 2_100_000})
	if !strings.Contains(one, "save.zip") || !strings.Contains(one, "2.1 MB") {
		t.Errorf("a single file reads as %q", one)
	}

	many := describeDrop("save.zip", Received{Files: 47, Bytes: 3_000_000_000})
	if !strings.Contains(many, "47 files") || !strings.Contains(many, "3.0 GB") {
		t.Errorf("a folder reads as %q", many)
	}
}

// pushed is what one fake write into a container recorded.
type pushed struct {
	path string
	uid  int64
	gid  int64
	mode int
	body string
}

// fakeFiler is a container that only remembers what was written to it, so that
// the loop in Receive can be measured without an Incus to run it against.
type fakeFiler struct {
	status string

	dirs  []string
	files []pushed

	// failOn makes the write of one path fail, which is the case that has to
	// stop the whole drop rather than skip a file.
	failOn string
}

func (f *fakeFiler) Status(string) (string, error) { return f.status, nil }

func (f *fakeFiler) MakeDir(_, dir string, _ int, _, _ int64) error {
	f.dirs = append(f.dirs, dir)

	return nil
}

func (f *fakeFiler) PushStream(_, dest string, content io.Reader, mode int, uid, gid int64) error {
	body, err := io.ReadAll(content)
	if err != nil {
		return err
	}

	if dest == f.failOn {
		return errors.New("the disk is full")
	}

	f.files = append(f.files, pushed{path: dest, uid: uid, gid: gid, mode: mode, body: string(body)})

	return nil
}

// receiver builds a manager whose seat exists and whose container is a fake.
func receiver(t *testing.T, uid int64, files *fakeFiler) *Manager {
	t.Helper()

	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Put(Seat{Name: "living-room", PlayerUID: uid}); err != nil {
		t.Fatal(err)
	}

	return &Manager{
		store: store,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		rt:    map[string]*runtime{},
		files: files,
	}
}

// handing over is one call per file, in order, and never a whole list: the
// parts of a multipart body can only be read once and in the order they came.
type list struct {
	files [][2]string
	at    int
}

func (l *list) Next() (Upload, error) {
	if l.at >= len(l.files) {
		return Upload{}, io.EOF
	}

	file := l.files[l.at]
	l.at++

	return Upload{Path: file[0], Body: strings.NewReader(file[1])}, nil
}

// The ordinary case, and the two things about it that matter: the files land
// under Downloads with the folders they came in, and they belong to the player
// rather than to root. A tree owned by root inside somebody's home is one this
// project has already put there once.
func TestAnUploadLandsInDownloadsOwnedByThePlayer(t *testing.T) {
	files := &fakeFiler{status: "Running"}
	m := receiver(t, 1000, files)

	got, err := m.Receive("living-room", &list{files: [][2]string{
		{"save.zip", "the save"},
		{"load/0100/Textures/a.bfres", "the mod"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	if got.Files != 2 || got.Bytes != int64(len("the save")+len("the mod")) {
		t.Errorf("the drop was reported as %d files and %d bytes", got.Files, got.Bytes)
	}

	want := []pushed{
		{path: DropDir + "/save.zip", uid: 1000, gid: 1000, mode: 0o644, body: "the save"},
		{path: DropDir + "/load/0100/Textures/a.bfres", uid: 1000, gid: 1000, mode: 0o644, body: "the mod"},
	}

	if !reflect.DeepEqual(files.files, want) {
		t.Errorf("what reached the container was\n%+v\nwant\n%+v", files.files, want)
	}
}

// A folder of several hundred files is several hundred files in a handful of
// directories, and asking Incus to create each of those once per file is a
// round trip for every answer nobody reads.
func TestAnUploadAsksForEachFolderOnce(t *testing.T) {
	files := &fakeFiler{status: "Running"}
	m := receiver(t, 1000, files)

	var sent [][2]string
	for i := range 40 {
		sent = append(sent, [2]string{fmt.Sprintf("save/user/%02d.dat", i), "x"})
	}

	if _, err := m.Receive("living-room", &list{files: sent}); err != nil {
		t.Fatal(err)
	}

	if len(files.dirs) != 1 {
		t.Errorf("%d files in one folder asked for it %d times: %v", len(sent), len(files.dirs), files.dirs)
	}
}

// One name the seat refuses does not cost the other four hundred. Finding out
// otherwise means uploading the whole folder again to learn whether there was a
// second one.
func TestAnUploadSkipsABadNameAndKeepsGoing(t *testing.T) {
	files := &fakeFiler{status: "Running"}
	m := receiver(t, 1000, files)

	got, err := m.Receive("living-room", &list{files: [][2]string{
		{"first.bin", "one"},
		{"../escape", "no"},
		{"second.bin", "two"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	if got.Files != 2 {
		t.Errorf("%d files were kept, want the two that were fine", got.Files)
	}

	if len(got.Skipped) != 1 || got.Skipped[0].Path != "../escape" {
		t.Fatalf("what was skipped came back as %+v", got.Skipped)
	}

	if got.Skipped[0].Reason == "" {
		t.Error("the skipped file carries no reason, and the interface prints that reason")
	}

	for _, file := range files.files {
		if strings.Contains(file.path, "escape") {
			t.Errorf("%q was written after being refused", file.path)
		}
	}
}

// A write that fails is not a name the seat can do anything about: it is the
// container, the connection or the disk, and the next file will meet the same
// one. Carrying on would turn one error into four hundred.
func TestAnUploadStopsWhenTheContainerRefusesAWrite(t *testing.T) {
	files := &fakeFiler{status: "Running", failOn: DropDir + "/second.bin"}
	m := receiver(t, 1000, files)

	got, err := m.Receive("living-room", &list{files: [][2]string{
		{"first.bin", "one"},
		{"second.bin", "two"},
		{"third.bin", "three"},
	}})

	if err == nil {
		t.Fatal("a container that refused a write was reported as a drop that worked")
	}

	if !strings.Contains(err.Error(), "second.bin") {
		t.Errorf("the error does not say which file it was about: %v", err)
	}

	if got.Files != 1 {
		t.Errorf("%d files were reported as arrived, and one had", got.Files)
	}
}

// Without a recorded uid there is nothing to own the files with. The same
// condition the shared library has, and the same answer: provision it once.
func TestAnUploadNeedsToKnowWhoThePlayerIs(t *testing.T) {
	files := &fakeFiler{status: "Running"}
	m := receiver(t, 0, files)

	if _, err := m.Receive("living-room", &list{files: [][2]string{{"save.zip", "x"}}}); err == nil {
		t.Fatal("a seat with no recorded uid took an upload, and root would own it")
	}

	if len(files.files) != 0 {
		t.Errorf("%d files were written anyway", len(files.files))
	}
}

// A seat that has never been built has nowhere to put anything, and the error
// has to say that rather than come back from Incus as a name it does not know.
func TestAnUploadIntoASeatWithNoContainer(t *testing.T) {
	files := &fakeFiler{status: ""}
	m := receiver(t, 1000, files)

	if _, err := m.Receive("living-room", &list{files: [][2]string{{"save.zip", "x"}}}); err == nil {
		t.Fatal("a seat with no container took an upload")
	}
}

// A seat that is switched off is deliberately allowed: Incus mounts the volume
// for the file API whether or not the container runs, and loading a seat up
// before turning it on is a reasonable thing to want.
func TestAnUploadIntoASeatThatIsOff(t *testing.T) {
	files := &fakeFiler{status: "Stopped"}
	m := receiver(t, 1000, files)

	if _, err := m.Receive("living-room", &list{files: [][2]string{{"save.zip", "x"}}}); err != nil {
		t.Fatalf("a stopped seat refused an upload: %v", err)
	}

	if len(files.files) != 1 {
		t.Errorf("%d files reached a stopped seat", len(files.files))
	}
}
