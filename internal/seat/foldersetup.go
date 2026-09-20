package seat

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/superuser404notfound/Polyseat/internal/library"
)

// folderSetup is the script a shared folder may carry to make itself usable
// where it has just arrived.
//
// The pool moves directories and nothing else, which is the whole reason the
// launcher agnostic side works at all: no manifest, no format, no launcher the
// daemon has to understand. That stops one step short of a game somebody can
// start. Steam gets over the same step for free, because an appmanifest travels
// inside the library folder and Steam reads the library itself; Lutris has no
// equivalent, its registry is a row in that machine's pga.db and a YAML beside
// it, and neither can be copied between machines - a host install names a
// runner under ~/.local/share/lutris/runners that no seat has.
//
// So the folder registers itself, in whatever it has arrived in, and this runs
// it. That is not a manifest: the daemon reads nothing out of the script and
// has no opinion about what is in it. It is the same bargain the rest of this
// side makes, that a directory is the unit and what is inside it is its own
// business.
//
// Named for the daemon rather than for a seat because the host receives folders
// too now, and a file called setup-seat.sh running on the machine the seats are
// hosted on invites exactly one wrong assumption about where it is.
const folderSetup = "polyseat-setup.sh"

// folderSetupLimit bounds one run.
//
// Generous because the honest work behind one of these is slow: the first run
// of the folder this convention came from builds a wine prefix, which is
// wineboot plus a registry pass. Bounded anyway, because it runs unattended and
// a script that waits for an answer nobody is there to give would otherwise
// hold a delivery open forever.
const folderSetupLimit = 10 * time.Minute

// settleFolders runs the setup script of every folder just delivered.
//
// Deliberately after the pool's lock has been let go. These runs are minutes
// long, and holding the sync lock across them would block the interface's own
// buttons behind somebody else's wine prefix.
//
// Errors are logged where that member's other library news goes and never
// returned. A folder whose setup fails is still a folder that arrived, the
// files are there, and failing the whole pass over it would also stop every
// other delivery in the same report.
func (m *Manager) settleFolders(ctx context.Context, members []library.Member, report library.Report) {
	if m.pool == nil || len(report.Delivered) == 0 {
		return
	}

	known := make(map[string]library.Member, len(members))
	for _, member := range members {
		known[member.Name] = member
	}

	for _, move := range report.Delivered {
		name, ok := library.FolderName(move.App)
		if !ok {
			// A Steam title. Its manifest travels with it and Steam reads the
			// library itself, so there is nothing to run.
			continue
		}

		member, ok := known[move.Seat]
		if !ok {
			continue
		}

		dir := m.pool.FoldersOf(member)
		if dir == "" {
			continue
		}

		// Looked for on the host in both cases, because a seat's folders live
		// under the pool root and are only bind mounted into the container.
		// Asking the container would mean starting a process to find out that
		// there is no process to start.
		if !runnable(filepath.Join(dir, name, folderSetup)) {
			continue
		}

		m.runFolderSetup(ctx, member, name)
	}
}

// runnable reports whether a path is a file somebody could execute.
//
// The executable bit is part of the question rather than left to exec. A folder
// arriving from a filesystem that does not carry the bit, or unpacked from a
// zip, has a setup script that cannot run, and "permission denied" out of the
// daemon's log is a worse way to learn that than not trying.
func runnable(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// runFolderSetup runs one folder's setup where that folder now lives.
func (m *Manager) runFolderSetup(ctx context.Context, member library.Member, name string) {
	ctx, cancel := context.WithTimeout(ctx, folderSetupLimit)
	defer cancel()

	m.moved(member.Name, "%s arrived, running its %s", name, folderSetup)

	var (
		out string
		err error
	)

	if member.Name == hostMember {
		out, err = runSetupOnHost(ctx, m.pool.FoldersOf(member), name, member.Owner)
	} else {
		out, err = m.runSetupInSeat(ctx, member.Name, name)
	}

	if err != nil {
		m.moved(member.Name, "! %s could not set itself up: %v", name, err)

		if line := lastLine(out); line != "" {
			m.moved(member.Name, "! it said: %s", line)
		}

		return
	}

	if line := lastLine(out); line != "" {
		m.moved(member.Name, "%s: %s", name, line)
	}
}

// runSetupInSeat runs the script through the container, as the player.
//
// The path is the one inside the container rather than the one on the host: the
// folders are bind mounted, so the same files have two names and only one of
// them exists for the process that runs them.
func (m *Manager) runSetupInSeat(ctx context.Context, seat, name string) (string, error) {
	script := LibraryMount + "/shared/" + name + "/" + folderSetup

	argv := append(playerPrefix(m.runtimeOf(seat).uid),
		"HOME=/home/"+Player, script)

	out, code, err := m.client.Try(ctx, seat, argv...)
	if err != nil {
		return out, err
	}

	if code != 0 {
		return out, fmt.Errorf("it exited %d", code)
	}

	return out, nil
}

// runSetupOnHost runs the script directly, as whoever owns the library.
//
// Never as root, which is the whole point of taking the credentials off the
// directory in hostMembers: this is a script out of the pool, the pool is fed
// by the members, and a member is a seat whose player has no sudo by design.
// Running it as the owner gives it exactly what that person already has, and a
// seat cannot reach further into this machine by putting a file in a folder.
func runSetupOnHost(ctx context.Context, dir, name string, owner library.Owner) (string, error) {
	who := ownerOf(owner.UID)
	if who == nil || who.HomeDir == "" {
		return "", fmt.Errorf("uid %d has no passwd entry to take a home from", owner.UID)
	}

	work := filepath.Join(dir, name)

	cmd := exec.CommandContext(ctx, filepath.Join(work, folderSetup))
	cmd.Dir = work

	// A fresh environment rather than the daemon's. systemd gives a service a
	// sparse one anyway, and what is left of it - root's HOME above all - is
	// exactly what would send a setup script writing into the wrong place.
	cmd.Env = []string{
		"HOME=" + who.HomeDir,
		"USER=" + who.Username,
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(owner.UID),
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid: uint32(owner.UID),
			Gid: uint32(owner.GID),
		},
	}

	out, err := cmd.CombinedOutput()

	return string(out), err
}
