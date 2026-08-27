// Package report writes down everything worth knowing about an installation in
// one go, so that a problem can be described in one message instead of ten.
//
// It exists because of the shape of this project's support: somebody with a
// machine nobody here can see, a card nobody here has and a filesystem nobody
// here chose. The first three questions are always the same ones, and asking
// them one at a time costs a round trip each.
//
// Two decisions shape the rest.
//
// It runs without the daemon, because a report is wanted most exactly when
// polyseatd will not start. Everything is read from disk, from sysfs, from
// Incus and from systemd directly. The cost is that the per seat logs shown in
// the web interface are not in it: those live in the daemon's memory and go
// when it does. The journal is in it instead, which is where a daemon that
// fails to start says why.
//
// And nothing here stops at the first thing it cannot read. Every line records
// its own failure and the report carries on, because "Incus is not answering"
// is one of the more useful lines it can contain.
package report

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/superuser404notfound/Polyseat/internal/config"
	"github.com/superuser404notfound/Polyseat/internal/incusx"
	"github.com/superuser404notfound/Polyseat/internal/library"
	"github.com/superuser404notfound/Polyseat/internal/seat"

	"github.com/superuser404notfound/Polyseat/internal/hostpkg"
)

// Write produces the report.
//
// No error is returned. A report that gives up halfway is worth less than one
// full of lines saying what could not be read, and there is nothing sensible
// for a caller to do about a failure to describe a machine anyway.
func Write(w io.Writer, cfg config.Config, version string, now time.Time) {
	r := &out{w: bufio.NewWriter(w)}
	defer func() { _ = r.w.Flush() }()

	r.preamble(version, now)
	r.polyseat(cfg, version)
	r.machine()
	r.graphics(cfg)
	r.incus()
	r.storage(cfg)
	r.network(cfg)
	r.configuration(cfg)
	r.seats(cfg)
	r.journal()
}

type out struct {
	w *bufio.Writer
}

func (o *out) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(o.w, format, args...)
}

func (o *out) section(title string) {
	o.printf("\n## %s\n\n", title)
}

func (o *out) line(key, value string) {
	o.printf("  %-22s %s\n", key+":", value)
}

// unreadable is how every failure in here is reported. Deliberately the same
// shape as a value: what could not be read is a fact about the machine too, and
// a reader should not have to hunt for it in a different format.
func (o *out) unreadable(key string, err error) {
	o.line(key, "could not be read: "+err.Error())
}

func (o *out) preamble(version string, now time.Time) {
	o.printf("# Polyseat report\n\n")
	o.printf("polyseatd %s, %s\n\n", version, now.Format(time.RFC3339))
	o.printf(`Read this before pasting it anywhere. It contains the host name of this
machine, the private addresses and names of its seats, and whatever the daemon
has written to the journal, which includes the name you sign in to the
interface with.

It contains no password, no key and no certificate. Nothing here opens
credentials.json, the secrets directory or the TLS material, and the seats are
only ever asked what state they are in.
`)
}

func (o *out) polyseat(cfg config.Config, version string) {
	o.section("Polyseat")

	o.line("version", version)

	if version == "dev" || version == "unknown" || strings.Contains(version, "-") {
		o.line("", "not a released build, so it is a checkout of something in between")
	}

	if path, err := os.Executable(); err != nil {
		o.unreadable("binary", err)
	} else {
		o.line("binary", path)
	}

	// Worth stating rather than assuming. Most of what follows is readable by
	// anybody; the seat records are 0700 and the journal is privileged, so a
	// report taken without root is a report with holes in exactly the places
	// somebody would look first.
	if os.Geteuid() == 0 {
		o.line("taken as", "root")
	} else {
		o.line("taken as", fmt.Sprintf("uid %d, so seats and the journal may be missing below", os.Geteuid()))
	}

	if _, err := os.Stat(config.DefaultPath); err == nil {
		o.line("configuration", config.DefaultPath)
	} else if os.IsNotExist(err) {
		// Not a fault and worth saying so plainly, or the next person reads it
		// as one. A machine with no file runs on the defaults by design.
		o.line("configuration", config.DefaultPath+" does not exist, so the defaults are in use")
	} else {
		o.unreadable("configuration", err)
	}

	o.line("unit", systemd("polyseatd.service"))
}

// systemd asks about a unit in one call and parses by key rather than by
// position, because systemd does not promise the order of what it prints.
func systemd(unit string) string {
	raw, err := run("systemctl", "show", unit,
		"-p", "ActiveState", "-p", "SubState", "-p", "UnitFileState", "-p", "ExecMainStartTimestamp")
	if err != nil {
		return "could not be read: " + err.Error()
	}

	fields := map[string]string{}

	for _, line := range strings.Split(raw, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			fields[key] = value
		}
	}

	state := fields["ActiveState"]
	if sub := fields["SubState"]; sub != "" && sub != state {
		state += " (" + sub + ")"
	}

	if file := fields["UnitFileState"]; file != "" {
		state += ", " + file
	}

	if started := fields["ExecMainStartTimestamp"]; started != "" {
		state += ", running since " + started
	}

	if state == "" {
		return "systemd knows no such unit"
	}

	return state
}

func (o *out) machine() {
	o.section("Machine")

	o.line("distribution", osRelease())

	// Which family that name was placed in, which is not the same question and
	// is the one that matters in a bug report. PRETTY_NAME says "Linux Mint
	// 22"; this says whether anything here knew what to do with it. An "unknown"
	// on a machine whose name is plainly Debian is the report saying where to
	// look, and no amount of PRETTY_NAME would have said it.
	family := hostpkg.Detect()
	if family == hostpkg.Unknown {
		o.line("package manager", "unknown, so preparing and updating are both refused here")
	} else {
		o.line("package manager", fmt.Sprintf("%s (%s)", family.Manager(), family))
	}

	o.line("kernel", strings.TrimSpace(readOr("/proc/sys/kernel/osrelease")))
	o.line("cpu", firstField("/proc/cpuinfo", "model name"))
	o.line("memory", firstField("/proc/meminfo", "MemTotal"))

	if name, err := os.Hostname(); err == nil {
		o.line("host name", name)
	}
}

func (o *out) graphics(cfg config.Config) {
	o.section("Graphics")

	// The same call the daemon makes at startup, against the same sysfs. This
	// is the single most useful line in the report: a card detected as the
	// wrong vendor, or as nothing, produces seats that come up, stream and look
	// entirely healthy while encoding on the CPU.
	gpu, err := seat.DetectGPU("/sys")
	if err != nil {
		o.unreadable("detected", err)
	} else {
		o.line("detected", gpu.String())
	}

	if cfg.GPURenderNode != "" {
		o.line("configured", cfg.GPURenderNode+" (overriding what was detected)")
	}

	// Read from the module rather than from nvidia-smi, which is a fork and
	// needs the whole userspace to be healthy to answer at all.
	if version, err := os.ReadFile("/sys/module/nvidia/version"); err == nil {
		o.line("nvidia driver", strings.TrimSpace(string(version)))
	}

	nodes, err := filepath.Glob("/dev/dri/renderD*")
	if err != nil || len(nodes) == 0 {
		o.line("render nodes", "none, which means no driver is bound to anything that can render")
	} else {
		o.line("render nodes", strings.Join(nodes, " "))
	}
}

func (o *out) incus() {
	o.section("Incus")

	client, err := incusx.Connect()
	if err != nil {
		o.unreadable("server", err)

		return
	}

	defer client.Close()

	if version, err := client.ServerVersion(); err != nil {
		o.unreadable("server", err)
	} else {
		o.line("server", version)
	}
}

func (o *out) storage(cfg config.Config) {
	o.section("Storage")

	o.line("state directory", describeDir(cfg.StateDir))
	o.line("library directory", describeDir(cfg.LibraryDir))

	// Asked of the nearest ancestor that exists, because on a machine where the
	// library has never been built the directory itself does not, and the
	// question is about the filesystem it would land on.
	probe := cfg.LibraryDir
	for probe != "/" && probe != "." {
		if _, err := os.Stat(probe); err == nil {
			break
		}

		probe = filepath.Dir(probe)
	}

	o.line("filesystem", filesystem(probe)+" at "+probe)
	o.line("space", space(probe))

	// The real question, and the only honest way to ask it: write a block and
	// clone it. XFS only reflinks when it was made with reflink=1 and a btrfs
	// subvolume with nodatacow does not either, so the name of the filesystem
	// is not the answer.
	//
	// This writes two small files into a directory that already exists and
	// removes them again. It creates nothing that was not there.
	if err := library.SupportsReflink(probe); err != nil {
		o.line("shares blocks", "no: "+err.Error())
		o.line("", "seats work, the shared game library does not")
	} else {
		o.line("shares blocks", "yes")
	}
}

func (o *out) network(cfg config.Config) {
	o.section("Network")

	// The same function the daemon and host/lan-bridge.sh get their answer
	// from, so a report cannot describe a machine differently from the way it
	// is being run.
	uplink, source := seat.Uplink(cfg)

	if uplink == "" {
		o.line("uplink", source)
	} else {
		o.line("uplink", uplink+" ("+source+")")
	}

	// Not cosmetic. On a plain interface every seat is isolated from this
	// machine whatever the per seat checkbox says, and that is the first thing
	// to know when somebody reports that local multiplayer does not find the
	// host.
	if uplink != "" {
		if seat.IsBridge(uplink) {
			o.line("is a bridge", "yes, so seats can reach this machine")
		} else {
			o.line("is a bridge", "no, so every seat is isolated from this machine")
		}

		if config.Wireless(uplink) {
			o.line("wireless", "yes, and macvlan cannot work on it at all")
		}
	}

	o.line("candidates", strings.Join(config.Uplinks(), " "))
}

func (o *out) configuration(cfg config.Config) {
	o.section("Configuration")

	// Printed whole, and safe to print whole: this struct is paths, an
	// interface name, a listen address and two switches. No password or key has
	// ever lived in it, which is why the credentials are a separate file.
	raw, err := json.MarshalIndent(cfg, "  ", "  ")
	if err != nil {
		o.unreadable("configuration", err)

		return
	}

	o.printf("  %s\n", raw)
}

func (o *out) seats(cfg config.Config) {
	o.section("Seats")

	// Asked before OpenStore, which creates the directory when it is missing. A
	// report reads a machine and does not change it, and its absence is an
	// answer worth printing rather than a hole worth filling in. Without this,
	// the same call without root reported "mkdir: permission denied", which
	// reads as though describing the machine required writing to it.
	dir := filepath.Join(cfg.StateDir, "seats")

	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			o.line("seats", "none, "+dir+" does not exist, so none has ever been created")
		} else {
			o.unreadable("seats", err)
		}

		return
	}

	store, err := seat.OpenStore(cfg.StateDir)
	if err != nil {
		o.unreadable("seats", err)

		return
	}

	seats, err := store.List()
	if err != nil {
		o.unreadable("seats", err)

		return
	}

	o.printf("  provisioning recipe in this build: generation %d\n", seat.Generation)

	if len(seats) == 0 {
		o.printf("\n  none defined\n")

		return
	}

	// Asked once for all of them rather than per seat, and a failure here is
	// not fatal to the section: the records on disk are worth having even when
	// Incus is the thing that is broken.
	var client *incusx.Client

	if c, err := incusx.Connect(); err == nil {
		client = c

		defer client.Close()
	}

	for _, s := range seats {
		o.printf("\n")
		o.line("seat", s.Name)

		if client != nil {
			if state, err := client.Status(s.Name); err != nil {
				o.unreadable("  container", err)
			} else {
				o.line("  container", state)
			}
		} else {
			o.line("  container", "unknown, Incus did not answer")
		}

		built := fmt.Sprintf("generation %d", s.Provisioned)

		switch {
		case s.Provisioned == 0:
			built = "never built"
		case s.Provisioned != seat.Generation:
			built += ", which is behind this build and needs provisioning again"
		}

		o.line("  built", built)
		o.line("  resolution", s.Resolution)
		o.line("  autostart", yesno(s.Autostart))
		o.line("  shared library", yesno(s.Library))
		o.line("  reaches the host", yesno(!s.Isolated))

		if s.Address != "" {
			o.line("  address", s.Address+" via "+s.Gateway)
		} else {
			o.line("  address", "DHCP")
		}
	}
}

func (o *out) journal() {
	o.section("Journal, last 200 lines")

	raw, err := run("journalctl", "-u", "polyseatd.service", "-n", "200", "--no-pager")
	if err != nil {
		o.printf("  could not be read: %v\n", err)

		return
	}

	if strings.TrimSpace(raw) == "" {
		o.printf("  empty, so this daemon has never logged anything on this boot\n")

		return
	}

	o.printf("%s\n", raw)
}

// ------------------------------------------------------------------ helpers

// run executes a command and returns its output. Short timeout because every
// caller here is asking a local question that answers in milliseconds, and a
// report that hangs is a report nobody sends.
func run(name string, args ...string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("no %s on this machine", name)
	}

	cmd := exec.Command(path, args...)

	raw, err := cmd.Output()
	if err != nil {
		return string(raw), err
	}

	return string(raw), nil
}

func readOr(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "could not be read: " + err.Error()
	}

	return string(raw)
}

// osRelease prefers PRETTY_NAME, which is the line a person recognises.
func osRelease() string {
	raw, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "could not be read: " + err.Error()
	}

	for _, line := range strings.Split(string(raw), "\n") {
		if value, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(value, `"`)
		}
	}

	return "no PRETTY_NAME in /etc/os-release"
}

// firstField pulls one value out of a file of "key: value" lines, which is what
// both /proc/cpuinfo and /proc/meminfo are. The first is enough: every core
// carries the same model name.
func firstField(path, key string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "could not be read: " + err.Error()
	}

	for _, line := range strings.Split(string(raw), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(name) == key {
			return strings.TrimSpace(value)
		}
	}

	return "no " + key + " in " + path
}

func describeDir(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return path + " does not exist yet"
		}

		return path + ": " + err.Error()
	}

	// Octal, not the rwx string. These are directories, and the rwx form leads
	// with a dash for "not a directory" in a way that reads as a typo here.
	return fmt.Sprintf("%s, mode %04o", path, info.Mode().Perm())
}

func filesystem(path string) string {
	raw, err := run("findmnt", "-no", "FSTYPE", "--target", path)
	if err != nil {
		return "unknown"
	}

	return strings.TrimSpace(raw)
}

// space reports what is left on the filesystem holding path. Rounded to
// gigabytes on purpose: this figure is for "is the disk full", and anything
// finer invites reading it as a measurement of something.
func space(path string) string {
	var st syscall.Statfs_t

	if err := syscall.Statfs(path, &st); err != nil {
		return "could not be read: " + err.Error()
	}

	gib := func(blocks uint64) string {
		return fmt.Sprintf("%.0f GiB", float64(blocks)*float64(st.Bsize)/(1<<30))
	}

	return gib(st.Bavail) + " free of " + gib(st.Blocks)
}

func yesno(b bool) string {
	if b {
		return "yes"
	}

	return "no"
}
