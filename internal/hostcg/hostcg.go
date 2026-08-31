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

// Legacy is one cgroup v1 hierarchy mounted under Root.
type Legacy struct {
	// Point is where it is mounted, e.g. /sys/fs/cgroup/net_cls.
	Point string

	// Controllers is what it carries, e.g. "net_cls".
	Controllers string

	// Users is how many processes sit in cgroups below the hierarchy's root.
	//
	// Below the root, and that distinction is the whole value of this field. In
	// a v1 hierarchy every process on the machine is a member of the root
	// cgroup by default, so the root's own cgroup.procs holds several hundred
	// entries on an idle host. Counting those would say "in use" about a
	// hierarchy nobody has ever put anything in, which is precisely the case
	// this package needs to recognise.
	Users int
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

		found = append(found, Legacy{
			Point:       point,
			Controllers: controllers(rf[2]),
			Users:       users(point),
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

// users counts the processes in cgroups below point, never in point itself and
// never in the runtime's own. See the note on Legacy.Users for why the root is
// skipped and the note on ours for the rest.
func users(point string) int {
	total := 0

	_ = filepath.WalkDir(point, func(path string, entry os.DirEntry, err error) error {
		if err != nil || !entry.IsDir() || path == point {
			return nil //nolint:nilerr // an unreadable corner is not a reason to abandon the count
		}

		if rel, err := filepath.Rel(point, path); err == nil && ours[rel] {
			return filepath.SkipDir
		}

		data, err := os.ReadFile(filepath.Join(path, "cgroup.procs"))
		if err != nil {
			return nil //nolint:nilerr
		}

		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if _, err := strconv.Atoi(strings.TrimSpace(line)); err == nil {
				total++
			}
		}

		return nil
	})

	return total
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
// hierarchy with processes in it is one somebody is relying on — with Mullvad
// that means applications deliberately kept outside the tunnel — and a program
// that silently switches off another program's feature to get its own work done
// has done something worse than fail. An empty one is the opposite case: it is
// evidence that nobody has used the feature at all, and clearing it costs its
// owner nothing they would notice.
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
			log("%s is a cgroup v1 mount with %d processes in it, which is what stops containers from starting here; leaving it alone, because something is using it",
				l.Point, l.Users)

			continue
		}

		log("%s is a cgroup v1 mount and nothing is in it, which is what stops containers from starting here; unmounting it",
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

	b.WriteString("\nOn a machine with Mullvad installed this is almost certainly mullvad-daemon,\n")
	b.WriteString("which mounts net_cls for its split tunneling on every start and has no\n")
	b.WriteString("option to leave it alone. Unmounting it lets seats start again:\n\n")

	for _, l := range ls {
		fmt.Fprintf(&b, "  sudo umount -l %s\n", l.Point)
	}

	b.WriteString("\nSplit tunneling stops working until mullvad-daemon is next started, and the\n")
	b.WriteString("mount comes back when it is.")

	return b.String()
}

func describeUsers(l Legacy) string {
	switch l.Users {
	case 0:
		return "with nothing in it"
	case 1:
		return "with 1 process in it"
	default:
		return fmt.Sprintf("with %d processes in it", l.Users)
	}
}
