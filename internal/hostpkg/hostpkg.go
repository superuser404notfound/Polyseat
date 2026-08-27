// Which package manager this host has, for the two things the daemon does with
// one: work out whether it owns the running binary, and install a release.
//
// The shell half of this is host/distro.sh, and the two are deliberately not
// one file generated from the other. They answer different questions — that one
// installs prerequisites and this one installs Polyseat — and the overlap is
// three rows of a table. A generator between them would be more machinery than
// the duplication costs.
//
// Nothing here concerns a seat. A seat is an Incus container built from
// archlinux/current on every host, so the pacman calls in internal/seat run
// inside a container and mean the same thing whatever this returns.
package hostpkg

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Family is which of the three package managers a host has.
//
// A family rather than a distribution, because that is the granularity every
// decision here is made at: CachyOS and EndeavourOS are both Arch as far as
// installing a package goes, and nothing in this package wants to know which.
type Family string

const (
	Arch   Family = "arch"
	Debian Family = "debian"
	Fedora Family = "fedora"

	// Unknown is a real answer and not a failure. A host Polyseat's scripts do
	// not know can still be running a checkout install perfectly well; what it
	// cannot do is update itself from the interface, and saying so is better
	// than guessing at a command.
	Unknown Family = ""
)

// osReleasePath is where a distribution names itself.
//
// A variable rather than a constant so that a test can hold the table against
// files it wrote instead of against the machine it runs on. Without that the
// Debian and Fedora rows would be checked by nobody, since this is developed on
// Arch and CI runs on Ubuntu.
var osReleasePath = "/etc/os-release"

// lookPath is how the fallback finds a package manager, replaced in tests for
// the same reason.
var lookPath = exec.LookPath

// Detect works out which family this host belongs to.
//
// ID first and then ID_LIKE, because a derivative names its parent there and
// that is exactly the question: CachyOS, EndeavourOS and Manjaro all say
// ID_LIKE=arch and all three have pacman. When os-release settles nothing, the
// binaries on PATH do, because a machine with pacman is a machine with pacman
// whatever its os-release says or fails to say.
func Detect() Family {
	id, like := readOSRelease()

	for _, token := range append(fields(id), fields(like)...) {
		switch token {
		case "arch", "archlinux", "cachyos":
			return Arch
		case "debian", "ubuntu":
			return Debian
		case "fedora", "rhel", "centos":
			return Fedora
		}
	}

	for _, candidate := range []struct {
		bin    string
		family Family
	}{
		{"pacman", Arch},
		{"apt-get", Debian},
		{"dnf", Fedora},
	} {
		if _, err := lookPath(candidate.bin); err == nil {
			return candidate.family
		}
	}

	return Unknown
}

// readOSRelease returns ID and ID_LIKE, both possibly empty.
func readOSRelease() (id, like string) {
	raw, err := os.ReadFile(osReleasePath)
	if err != nil {
		return "", ""
	}

	for _, line := range strings.Split(string(raw), "\n") {
		if value, ok := strings.CutPrefix(line, "ID="); ok {
			id = unquote(value)
		}

		if value, ok := strings.CutPrefix(line, "ID_LIKE="); ok {
			like = unquote(value)
		}
	}

	return id, like
}

// unquote strips the quoting os-release allows around a value. The file is
// shell syntax, and both quoted and bare forms appear in the wild.
func unquote(s string) string {
	return strings.Trim(strings.TrimSpace(s), `"'`)
}

// fields splits an ID_LIKE, which is a space separated list, and an ID, which
// is one token and splits to itself.
func fields(s string) []string {
	if s == "" {
		return nil
	}

	return strings.Fields(s)
}

// Manager is what to call this family's package manager in a sentence.
func (f Family) Manager() string {
	switch f {
	case Arch:
		return "pacman"
	case Debian:
		return "apt"
	case Fedora:
		return "dnf"
	}

	return "the package manager"
}

// Asset is the release file this family installs.
//
// Three names, and none of them carries a version, for the reason
// packaging/README.md gives at length: releases/latest/download/<name> is a
// permanent link only while <name> is permanent, so the documented command
// never has to be edited. Every one of the three package managers reads the
// real name and version out of the file rather than off it.
//
// Unknown gets the empty string, and every caller treats that as "this host
// cannot install a release", which is the honest answer.
func (f Family) Asset() string {
	switch f {
	case Arch:
		return "polyseat-x86_64.pkg.tar.zst"
	case Debian:
		return "polyseat_amd64.deb"
	case Fedora:
		return "polyseat.x86_64.rpm"
	}

	return ""
}

// Owns says whether a package on this host owns a file.
//
// What tells a packaged installation from a checkout one. Every one of the
// three reads a local database and none of them touches the network, which
// matters because this is asked at daemon startup.
func Owns(path string) bool {
	// Resolved, because a package manager knows the real path and not a symlink
	// to it.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}

	switch Detect() {
	case Arch:
		return exec.Command("pacman", "-Qo", "--", path).Run() == nil
	case Debian:
		return exec.Command("dpkg", "-S", "--", path).Run() == nil
	case Fedora:
		return exec.Command("rpm", "-qf", "--", path).Run() == nil
	}

	return false
}

// InstallFile installs a package that is already on disk, and returns whatever
// the package manager said.
//
// The file is never named by the caller of the interface: it is the release the
// daemon's own checker found, downloaded by internal/update and verified before
// this is reached. That property is what the whole of apply.go is written to
// keep, and this function is the last place it could be lost.
func InstallFile(ctx context.Context, file string) ([]byte, error) {
	f := Detect()

	// Absolute, and not only for tidiness. apt reads a bare name as a package
	// name rather than as a path, so a relative filename here would send it to
	// the network looking for a package called polyseat_amd64.deb.
	if abs, err := filepath.Abs(file); err == nil {
		file = abs
	}

	var cmd *exec.Cmd

	switch f {
	case Arch:
		// --noconfirm because there is nobody at a terminal to answer, and no
		// question here has a second sensible answer.
		cmd = exec.CommandContext(ctx, "pacman", "-U", "--noconfirm", "--", file)
	case Debian:
		// apt-get install on a path rather than dpkg -i, because install
		// resolves the package's dependencies and dpkg -i leaves them unmet and
		// the package half configured. DEBIAN_FRONTEND stops debconf from
		// opening a prompt at a terminal that is not there.
		cmd = exec.CommandContext(ctx, "apt-get", "install", "-y", file)
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	case Fedora:
		cmd = exec.CommandContext(ctx, "dnf", "install", "-y", file)
	default:
		return nil, fmt.Errorf("this host's package manager is not one Polyseat knows, so it cannot install a release from here")
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s refused the package: %w", f.Manager(), err)
	}

	return out, nil
}
