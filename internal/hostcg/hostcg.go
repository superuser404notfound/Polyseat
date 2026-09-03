// Package hostcg answers one question about the machine: is there a cgroup v1
// hierarchy mounted on it, and is anything actually using it.
//
// It exists because of a failure that costs an afternoon to find. LXC 7 refuses
// to start a container on a host whose cgroup layout it calls "hybrid", and a
// single v1 mount anywhere under /sys/fs/cgroup is enough to earn that name
// even when everything else is unified. What Incus reports when that happens is
//
//	Failed to run: /usr/lib/incus/incusd forklxc <seat> ... exit status 1
//
// which names no cause at all. The cause is only in /var/log/incus/<seat>/lxc.log,
// which is root-only, and it reads "Unsupported cgroup layout (hybrid)". Nothing
// in the chain from the button on the page to that line mentions cgroups, so the
// obvious suspects get investigated first and all of them are innocent.
//
// The v1 mount is not usually the host owner's doing. mullvad-daemon mounts
// net_cls for its split tunneling on every start and offers no flag to turn that
// off, so on a machine with Mullvad installed this arrives by itself, survives
// nothing, and comes back after every reboot.
package hostcg

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// mountsFile is where the mount table is read from. A variable so the tests can
// point it at a fixture and describe a machine this one is not, which is the
// same reason prepare.idmapFiles is one.
var mountsFile = "/proc/self/mountinfo"

// unmount is the command Clear runs. A variable for the same reason: a test
// that really unmounted something would need root and would change the machine
// running it.
var unmount = func(args ...string) error {
	out, err := exec.Command("umount", args...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%s: %w", msg, err)
		}

		return err
	}

	return nil
}

// Root is the directory a legacy mount has to be under to count.
//
// Narrow on purpose. A v1 hierarchy somewhere else entirely is somebody's
// deliberate arrangement and not this program's business; one under the
// unified tree is the thing LXC trips over.
var Root = "/sys/fs/cgroup"

// procRoot is where process names are read from. A variable so the tests can
// name processes that are not running, for the same reason mountsFile is one.
var procRoot = "/proc"

// Legacy is one cgroup v1 hierarchy mounted under Root.
type Legacy struct {
	// Point is where it is mounted, e.g. /sys/fs/cgroup/net_cls.
	Point string

	// Controllers is what it carries, e.g. "net_cls".
	Controllers string

	// Occupants are the cgroups below the hierarchy's root that hold processes
	// and are doing something with them. See occupants for what the second half
	// of that rules out.
	Occupants []Occupant

	// Users is how many processes the Occupants hold between them.
	//
	// Below the root, and that distinction is the whole value of this field. In
	// a v1 hierarchy every process on the machine is a member of the root
	// cgroup by default, so the root's own cgroup.procs holds several hundred
	// entries on an idle host. Counting those would say "in use" about a
	// hierarchy nobody has ever put anything in, which is precisely the case
	// this package needs to recognise.
	Users int
}

// Occupant is one cgroup below a hierarchy's root with processes in it.
type Occupant struct {
	// Path is where it is, relative to the hierarchy's root, e.g.
	// "mullvad-exclusions".
	Path string

	// Comm is what the first of its processes is called, empty if the process
	// went away between being counted and being asked. It is in here so that a
	// message about a hierarchy that cannot be cleared can name what is holding
	// it rather than leave somebody guessing.
	Comm string

	// Procs is how many processes are in it.
	Procs int
}

// Describe says what the occupant is, for a line somebody has to act on.
func (o Occupant) Describe() string {
	count := fmt.Sprintf("%d processes", o.Procs)
	if o.Procs == 1 {
		count = "1 process"
	}

	if o.Comm == "" {
		return fmt.Sprintf("%s in %s", count, o.Path)
	}

	return fmt.Sprintf("%s in %s (%s)", count, o.Path, o.Comm)
}

// Idle says nothing is using the hierarchy, only that it is mounted.
func (l Legacy) Idle() bool { return l.Users == 0 }

// Mounts returns the legacy hierarchies under Root, nil on a unified host.
//
// Cheap enough to ask whenever a start has failed: one read of a file the
// kernel generates, and a directory walk per hierarchy found, of which a real
// machine has none or one.
func Mounts() []Legacy {
	data, err := os.ReadFile(mountsFile)
	if err != nil {
		// A machine whose mount table cannot be read is not one to make claims
		// about. Saying "no legacy mounts" here is the safe direction: it
		// leaves whatever error the caller already had untouched.
		return nil
	}

	var found []Legacy

	for _, line := range strings.Split(string(data), "\n") {
		// mountinfo puts a variable number of optional fields before a " - "
		// separator, so the two halves have to be split off it rather than
		// counted from the start.
		left, right, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}

		lf := strings.Fields(left)
		rf := strings.Fields(right)

		if len(lf) < 5 || len(rf) < 3 {
			continue
		}

		// "cgroup" is v1. "cgroup2" is the unified hierarchy and is what a
		// healthy host has, so it must not match here.
		if rf[0] != "cgroup" {
			continue
		}

		point := unescape(lf[4])
		if point != Root && !strings.HasPrefix(point, Root+"/") {
			continue
		}

		occupied := occupants(point)

		found = append(found, Legacy{
			Point:       point,
			Controllers: controllers(rf[2]),
			Occupants:   occupied,
			Users:       total(occupied),
		})
	}

	return found
}

// unescape undoes the octal escaping mountinfo puts on the characters that
// would otherwise split a field: space, tab, newline and the backslash itself.
//
// A cgroup mount point with a space in it is not a thing anybody has, but the
// field is a path and paths are the place where the assumption that they are
// well behaved is eventually wrong.
func unescape(field string) string {
	if !strings.Contains(field, `\`) {
		return field
	}

	var b strings.Builder

	for i := 0; i < len(field); i++ {
		if field[i] != '\\' || i+3 >= len(field) {
			b.WriteByte(field[i])

			continue
		}

		n, err := strconv.ParseUint(field[i+1:i+4], 8, 8)
		if err != nil {
			b.WriteByte(field[i])

			continue
		}

		b.WriteByte(byte(n))

		i += 3
	}

	return b.String()
}

// controllers keeps the names out of a superblock option list and drops the
// ordinary mount flags, so that "rw,net_cls" reads as "net_cls".
func controllers(options string) string {
	var keep []string

	for _, opt := range strings.Split(options, ",") {
		switch opt {
		case "rw", "ro", "relatime", "noatime", "nosuid", "nodev", "noexec", "seclabel":
			continue
		}

		keep = append(keep, opt)
	}

	return strings.Join(keep, ",")
}

// ours are cgroups the container runtime makes for itself, which do not count
// as somebody using the hierarchy.
//
// lxc.pivot is LXC's own scratch cgroup. It moves processes through it while it
// sets a container up and tears one down, so a start that has just failed
// leaves processes in it for as long as they take to exit. That matters here
// more than it sounds: this count is taken right after a failed start, which is
// exactly when lxc.pivot is at its fullest, so counting it let the failure
// answer the question of whether the failure could be fixed. On the machine
// this was found on it reported two processes and refused to clear a hierarchy
// that was, a second later, empty.
//
// Leaving it out is not a special case bolted on. The question the count asks
// is whether some other program is relying on this hierarchy, and the container
// runtime that is at this moment failing to start is not another program.
var ours = map[string]bool{"lxc.pivot": true}

// occupants finds the cgroups below point that somebody is relying on, never
// point itself, never the runtime's own, and never one that classifies nothing.
// See the note on Legacy.Users for why the root is skipped and the note on ours
// for the runtime.
//
// The last exclusion is the one that took a second machine to find. A process
// below the root is not by itself evidence that anybody wants this hierarchy:
// libvirt puts every virtual machine it starts into each v1 controller it finds
// mounted, and systemd does the same for its scopes, so a running VM lands in
// net_cls purely because Mullvad mounted it. Counted as a user, it made this
// daemon refuse the one repair it knows on a host where nothing was excluded
// from the tunnel at all, and the message that came out named mullvad-daemon
// for a process that was qemu.
//
// What tells the two apart is net_cls.classid. It is the only thing the
// controller does: a cgroup with a classid of zero has no mark to put on a
// packet, so no traffic of its processes is treated differently for its being
// there, and unmounting the hierarchy takes nothing away from them. Mullvad's
// own cgroup carries a real classid, and a process in that one is somebody
// deliberately kept outside the tunnel — exactly the case Recover must not
// touch. A hierarchy for a controller with no such file is counted as before,
// because then there is nothing to read the question off.
//
// Unmounting under a classid-zero occupant is not free of consequence: the
// plain unmount fails while the VM is in there and the lazy one leaves its
// cgroup unreachable, so libvirt may complain when it tears the machine down.
// It complains about a directory. The alternative is a host that cannot start
// a seat until a virtual machine somebody else's daemon parked there is shut
// down, and that is the worse of the two.
func occupants(point string) []Occupant {
	var found []Occupant

	_ = filepath.WalkDir(point, func(path string, entry os.DirEntry, err error) error {
		if err != nil || !entry.IsDir() || path == point {
			return nil //nolint:nilerr // an unreadable corner is not a reason to abandon the count
		}

		rel, err := filepath.Rel(point, path)
		if err != nil {
			return nil //nolint:nilerr
		}

		if ours[rel] {
			return filepath.SkipDir
		}

		pids := procs(path)
		if len(pids) == 0 || inert(path) {
			return nil
		}

		found = append(found, Occupant{Path: rel, Comm: comm(pids[0]), Procs: len(pids)})

		return nil
	})

	return found
}

// total is how many processes a set of occupants holds between them.
func total(list []Occupant) int {
	n := 0
	for _, o := range list {
		n += o.Procs
	}

	return n
}

// procs reads the process ids in one cgroup.
func procs(dir string) []int {
	data, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return nil
	}

	var pids []int

	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil {
			pids = append(pids, pid)
		}
	}

	return pids
}

// inert says the cgroup is in a net_cls hierarchy and classifies nothing, which
// is what makes its processes somebody's bookkeeping rather than somebody's
// use. A missing file means this is not net_cls and the question does not
// apply; an unreadable or unparsable one is not grounds for a claim either, so
// both answer false and leave the cgroup counted.
func inert(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "net_cls.classid"))
	if err != nil {
		return false
	}

	classid, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)

	return err == nil && classid == 0
}

// comm is what a process is called, empty if it has gone or cannot be read.
func comm(pid int) string {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "comm"))
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(data))
}

// Clear takes a hierarchy out of the mount table.
//
// Plain first, lazily second. The plain one fails with EBUSY while any process
// holds a descriptor inside the hierarchy, and the daemon that mounted it
// generally does hold one, so on the machine this was written for the plain
// call rarely succeeds. A lazy unmount detaches it from the mount tree at once
// and lets the kernel free it when the last reference goes, which is enough:
// what LXC decides the layout from is the mount table, and the hierarchy is out
// of it the moment the call returns.
//
// What counts as success is the state of the table afterwards and not what
// umount said, which is the correction to a version of this that reported a
// failure while standing in front of the result it wanted. Between a caller
// looking at the host and this running, the mount can go on its own — a failed
// container start is itself capable of removing it, since the mount is shared
// and an unmount inside the namespace LXC builds propagates back here. umount
// then says "not mounted" and exits 32, which is the desired state arriving by
// another route and not a problem. Asking the table is also the only check that
// stays right whoever did it.
func Clear(l Legacy) error {
	plain := unmount(l.Point)
	if plain == nil {
		return nil
	}

	lazy := unmount("-l", l.Point)
	if lazy == nil {
		return nil
	}

	// Both refused. If the hierarchy is out of the table anyway, that is the
	// job done and the errors describe how it was already done.
	if !mounted(l.Point) {
		return nil
	}

	return lazy
}

// mounted says whether point is in the mount table right now.
func mounted(point string) bool {
	for _, l := range Mounts() {
		if l.Point == point {
			return true
		}
	}

	return false
}

// Recover unmounts the legacy hierarchies nothing is using, and says whether it
// changed anything.
//
// Only the idle ones, and that restraint is the design rather than caution. A
// hierarchy somebody is relying on — with Mullvad that means applications
// deliberately kept outside the tunnel — is not one to unmount, because a
// program that silently switches off another program's feature to get its own
// work done has done something worse than fail. An idle one is the opposite
// case: it is evidence that nobody has used the feature at all, and clearing it
// costs its owner nothing they would notice. What idle means, and why a process
// sitting in the hierarchy is not enough to make it busy, is in occupants.
//
// The log lines are written the long way round on purpose. Somebody reading a
// seat's log later should be able to see that this daemon unmounted something
// of another program's, and why, without having to already know any of it.
func Recover(log func(format string, args ...any)) bool {
	return RecoverFor(Mounts(), log)
}

// RecoverFor is Recover over hierarchies already found, for a caller that has
// looked at the host once and should not have to look again between deciding
// something is wrong and doing something about it.
func RecoverFor(ls []Legacy, log func(format string, args ...any)) bool {
	cleared := false

	for _, l := range ls {
		if !l.Idle() {
			log("%s is a cgroup v1 mount %s, which is what stops containers from starting here; leaving it alone, because something is using it",
				l.Point, describeUsers(l))

			continue
		}

		log("%s is a cgroup v1 mount and nothing on this host is using it, which is what stops containers from starting here; unmounting it",
			l.Point)

		if err := Clear(l); err != nil {
			log("could not unmount %s: %v", l.Point, err)

			continue
		}

		log("unmounted %s, so seats can start again; if it was mullvad-daemon's, split tunneling is off until that is next started",
			l.Point)

		cleared = true
	}

	return cleared
}

// Describe is what to tell somebody whose seat would not start.
//
// It names the mount, says what it does to a container start, and gives the one
// command that undoes it, because a message that explains a failure without
// saying what to do about it has only moved the problem.
func Describe(ls []Legacy) string {
	if len(ls) == 0 {
		return ""
	}

	var b strings.Builder

	b.WriteString("This host has a cgroup v1 hierarchy mounted, which makes LXC call the\n")
	b.WriteString("layout \"hybrid\" and refuse to start any container:\n\n")

	for _, l := range ls {
		fmt.Fprintf(&b, "  %s (%s), %s\n", l.Point, l.Controllers, describeUsers(l))
	}

	b.WriteString("\nOn a machine with Mullvad installed the mount is almost certainly\n")
	b.WriteString("mullvad-daemon's: it mounts net_cls for its split tunneling on every start\n")
	b.WriteString("and has no option to leave it alone.")

	// Only when something is in there. This daemon clears an idle hierarchy by
	// itself, so a message about one that is still standing has a second reason
	// behind it — the unmount was refused — and pointing at occupants it has
	// just said there are none of would send the reader looking for a process
	// that does not exist.
	if occupied(ls) {
		b.WriteString(" What is named above as being in\n")
		b.WriteString("the mount is what stopped this daemon from clearing it by itself, and is\n")
		b.WriteString("worth a look before you do.")
	}

	b.WriteString(" Unmounting lets seats start again:\n\n")

	for _, l := range ls {
		fmt.Fprintf(&b, "  sudo umount -l %s\n", l.Point)
	}

	b.WriteString("\nSplit tunneling stops working until mullvad-daemon is next started, and the\n")
	b.WriteString("mount comes back when it is.")

	return b.String()
}

// describeUsers says what is in a hierarchy, naming the cgroups when they are
// known. Somebody who has to decide whether unmounting is safe needs to know
// what is in there, and "1 process" on its own has sent that decision the wrong
// way once already.
// occupied says whether anything is using any of the hierarchies.
func occupied(ls []Legacy) bool {
	for _, l := range ls {
		if !l.Idle() {
			return true
		}
	}

	return false
}

func describeUsers(l Legacy) string {
	if l.Users == 0 {
		return "with nothing using it"
	}

	if len(l.Occupants) == 0 {
		if l.Users == 1 {
			return "with 1 process in it"
		}

		return fmt.Sprintf("with %d processes in it", l.Users)
	}

	parts := make([]string, 0, len(l.Occupants))
	for _, o := range l.Occupants {
		parts = append(parts, o.Describe())
	}

	return "with " + strings.Join(parts, ", ")
}
