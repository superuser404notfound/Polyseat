package hostcg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unified is what /proc/self/mountinfo says on a host LXC is happy with.
const unified = `23 28 0:22 / /proc rw,nosuid,nodev,noexec,relatime shared:14 - proc proc rw
28 1 0:31 /@ / rw,relatime shared:1 - btrfs /dev/nvme0n1p5 rw,ssd,subvol=/@
43 23 0:23 / /sys rw,nosuid,nodev,noexec,relatime shared:6 - sysfs sysfs rw
45 43 0:29 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime shared:7 - cgroup2 cgroup2 rw,nsdelegate
`

// hybrid is the same host after mullvad-daemon has started, which is the whole
// reason this package exists. Taken from the machine the bug was found on.
const hybrid = unified +
	`631 45 0:202 / /sys/fs/cgroup/net_cls rw,relatime shared:1521 - cgroup net_cls rw,net_cls
`

// mounts points the reader at a fixture and returns what it makes of it.
func mounts(t *testing.T, table string) []Legacy {
	t.Helper()

	path := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(path, []byte(table), 0o644); err != nil {
		t.Fatal(err)
	}

	old := mountsFile
	mountsFile = path

	t.Cleanup(func() { mountsFile = old })

	return Mounts()
}

// hierarchy builds a directory that looks like a mounted v1 hierarchy, with the
// given number of processes in a child cgroup and always a crowd in the root.
//
// Always a crowd in the root because that is what the kernel does and what the
// count has to ignore: on a real host every process is a member of the root of
// every mounted v1 hierarchy, so a fixture without that would let a wrong
// implementation pass.
func hierarchy(t *testing.T, child int) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "net_cls")
	sub := filepath.Join(dir, "mullvad-exclusions")

	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	root := make([]string, 0, 615)
	for i := range 615 {
		root = append(root, fmt.Sprint(1000+i))
	}

	write := func(path string, pids []string) {
		body := strings.Join(pids, "\n")
		if body != "" {
			body += "\n"
		}

		if err := os.WriteFile(filepath.Join(path, "cgroup.procs"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(dir, root)

	pids := make([]string, 0, child)
	for i := range child {
		pids = append(pids, fmt.Sprint(9000+i))
	}

	write(sub, pids)

	return dir
}

func TestUnifiedHostHasNoLegacyMounts(t *testing.T) {
	if got := mounts(t, unified); len(got) != 0 {
		t.Fatalf("a unified host reported %d legacy mounts: %+v", len(got), got)
	}
}

func TestHybridHostNamesTheMount(t *testing.T) {
	got := mounts(t, hybrid)

	if len(got) != 1 {
		t.Fatalf("want 1 legacy mount, got %d: %+v", len(got), got)
	}

	if got[0].Point != "/sys/fs/cgroup/net_cls" {
		t.Errorf("point = %q", got[0].Point)
	}

	// "rw" is a mount flag and not a controller, so it must not survive into
	// a message that claims to name what the hierarchy carries.
	if got[0].Controllers != "net_cls" {
		t.Errorf("controllers = %q, want net_cls", got[0].Controllers)
	}
}

// A v1 hierarchy somewhere other than under the unified tree is somebody's own
// arrangement, and unmounting it would be this daemon reaching well outside
// what it was asked to do.
func TestLegacyMountOutsideTheUnifiedTreeIsIgnored(t *testing.T) {
	elsewhere := unified +
		"700 28 0:99 / /opt/something/cgroup rw,relatime shared:9 - cgroup none rw,freezer\n"

	if got := mounts(t, elsewhere); len(got) != 0 {
		t.Fatalf("a mount outside %s was reported: %+v", Root, got)
	}
}

// The root of a v1 hierarchy holds every process on the machine. Counting those
// would report an untouched hierarchy as busy, which would turn the one case
// this package can fix into the one case it refuses to.
func TestRootMembershipDoesNotCountAsUse(t *testing.T) {
	dir := hierarchy(t, 0)

	if n := users(dir); n != 0 {
		t.Fatalf("users = %d, want 0: the %d processes in the root were counted", n, 615)
	}
}

func TestProcessesInAChildCgroupCount(t *testing.T) {
	dir := hierarchy(t, 3)

	if n := users(dir); n != 3 {
		t.Fatalf("users = %d, want 3", n)
	}
}

// The count is taken just after a container start has failed, which is exactly
// when LXC's own pivot cgroup still holds the processes of the attempt that
// failed. Counting those made the failure vouch for its own irreparability: on
// the machine this was found on the hierarchy reported two processes and was
// left alone, and was empty a second later.
func TestTheRuntimesOwnCgroupDoesNotCountAsUse(t *testing.T) {
	dir := hierarchy(t, 0)

	pivot := filepath.Join(dir, "lxc.pivot")
	if err := os.MkdirAll(pivot, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(pivot, "cgroup.procs"), []byte("4242\n4243\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if n := users(dir); n != 0 {
		t.Fatalf("users = %d, want 0: a failed start left processes in lxc.pivot and they were counted", n)
	}
}

// Somebody else's cgroup in the same hierarchy still counts, whatever it is
// called. The exception above is for the runtime, not for everything awkward.
func TestAnUnknownChildCgroupStillCountsAsUse(t *testing.T) {
	dir := hierarchy(t, 0)

	other := filepath.Join(dir, "something-else")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(other, "cgroup.procs"), []byte("77\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if n := users(dir); n != 1 {
		t.Fatalf("users = %d, want 1", n)
	}
}

func TestRecoverClearsAnIdleHierarchy(t *testing.T) {
	dir := hierarchy(t, 0)

	var called []string

	old := unmount
	unmount = func(args ...string) error {
		called = append(called, strings.Join(args, " "))

		return nil
	}

	t.Cleanup(func() { unmount = old })

	var log []string

	if !RecoverFor([]Legacy{{Point: dir, Controllers: "net_cls", Users: users(dir)}}, func(f string, a ...any) {
		log = append(log, fmt.Sprintf(f, a...))
	}) {
		t.Fatal("Recover reported no change for an idle hierarchy")
	}

	if len(called) != 1 || called[0] != dir {
		t.Fatalf("unmount calls = %v, want one plain unmount of %s", called, dir)
	}

	if !strings.Contains(strings.Join(log, "\n"), "split tunneling") {
		t.Errorf("the log never mentions what was switched off:\n%s", strings.Join(log, "\n"))
	}
}

func TestRecoverLeavesAHierarchyInUseAlone(t *testing.T) {
	dir := hierarchy(t, 2)

	old := unmount
	unmount = func(args ...string) error {
		t.Fatalf("unmounted a hierarchy with processes in it: %v", args)

		return nil
	}

	t.Cleanup(func() { unmount = old })

	var log []string

	if RecoverFor([]Legacy{{Point: dir, Controllers: "net_cls", Users: users(dir)}}, func(f string, a ...any) {
		log = append(log, fmt.Sprintf(f, a...))
	}) {
		t.Fatal("Recover claimed to have changed something")
	}

	if !strings.Contains(strings.Join(log, "\n"), "leaving it alone") {
		t.Errorf("the log does not say why nothing was done:\n%s", strings.Join(log, "\n"))
	}
}

// The plain unmount fails with EBUSY while the daemon that made the mount holds
// a descriptor in it, which on a machine with Mullvad running is always.
func TestClearFallsBackToALazyUnmount(t *testing.T) {
	var called []string

	old := unmount
	unmount = func(args ...string) error {
		joined := strings.Join(args, " ")
		called = append(called, joined)

		if !strings.HasPrefix(joined, "-l ") {
			return fmt.Errorf("target is busy")
		}

		return nil
	}

	t.Cleanup(func() { unmount = old })

	if err := Clear(Legacy{Point: "/sys/fs/cgroup/net_cls"}); err != nil {
		t.Fatalf("Clear = %v", err)
	}

	want := []string{"/sys/fs/cgroup/net_cls", "-l /sys/fs/cgroup/net_cls"}
	if strings.Join(called, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v, want %v", called, want)
	}
}

// umount says "not mounted" and exits 32 when the hierarchy has gone between a
// caller looking at the host and this running, which a failed container start
// can cause by itself: the mount is shared, so an unmount inside the namespace
// LXC builds propagates back to the host. The desired state has arrived, and an
// earlier version of this reported a failure while standing in front of it,
// which cost the retry that would have brought the seat up.
func TestClearAcceptsAMountThatHasAlreadyGone(t *testing.T) {
	// A table with no legacy mount in it, so the check after the attempt finds
	// the hierarchy absent.
	path := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(path, []byte(unified), 0o644); err != nil {
		t.Fatal(err)
	}

	oldFile := mountsFile
	mountsFile = path

	t.Cleanup(func() { mountsFile = oldFile })

	old := unmount
	unmount = func(_ ...string) error {
		return fmt.Errorf("umount: /sys/fs/cgroup/net_cls: not mounted: exit status 32")
	}

	t.Cleanup(func() { unmount = old })

	if err := Clear(Legacy{Point: "/sys/fs/cgroup/net_cls"}); err != nil {
		t.Fatalf("Clear = %v, want nil: the mount was gone, which is what Clear is for", err)
	}
}

// The opposite: both calls refused and the mount is still there, which is a
// real failure and has to be reported as one.
func TestClearReportsAMountItCouldNotRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(path, []byte(hybrid), 0o644); err != nil {
		t.Fatal(err)
	}

	oldFile := mountsFile
	mountsFile = path

	t.Cleanup(func() { mountsFile = oldFile })

	old := unmount
	unmount = func(_ ...string) error {
		return fmt.Errorf("target is busy")
	}

	t.Cleanup(func() { unmount = old })

	if err := Clear(Legacy{Point: "/sys/fs/cgroup/net_cls"}); err == nil {
		t.Fatal("Clear = nil for a mount that is still in the table")
	}
}

func TestDescribeNamesTheMountAndTheWayOut(t *testing.T) {
	got := Describe([]Legacy{{Point: "/sys/fs/cgroup/net_cls", Controllers: "net_cls"}})

	for _, want := range []string{"/sys/fs/cgroup/net_cls", "umount -l", "mullvad-daemon", "hybrid"} {
		if !strings.Contains(got, want) {
			t.Errorf("the message never mentions %q:\n%s", want, got)
		}
	}
}

func TestDescribeIsEmptyOnAUnifiedHost(t *testing.T) {
	if got := Describe(nil); got != "" {
		t.Fatalf("Describe(nil) = %q", got)
	}
}
