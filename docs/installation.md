# Installation: who does what

Three layers, and the boundary between them is the point of this document.
`host/install.sh` covers the host side; this records what it must eventually
cover as well, and what must not end up in it.

## The rule

**The installer does what a package may not do.** Everything else belongs to the
daemon, and the web interface does nothing at all except talk to the daemon.

That used to read "what a daemon cannot do for itself", and the difference is
the whole of the change in 0.4.0. A daemon running as root can write
`/etc/subuid` and call `usermod` perfectly well; what cannot do those things is
an *Arch package*, and that is a rule about packaging rather than about
privilege. So the machine half stays exactly where it was, in one script, and it
grew a third caller: the interface runs it too. What is left that genuinely
cannot be done from inside is smaller than it looks — installing the package,
and starting the unit — and it is two lines of shell, once, per machine.

The property that has to survive any change here: **the browser never says what
to run.** Preparing runs one fixed script and takes one argument out of the
request, the account that goes in the `input` group. Removing runs one fixed
script and takes two flags. Neither takes a path, a command or a package name.

## The second line, inside the installer

The host side is itself two halves, and they are separate files because only one
of them can be packaged.

**`host/prepare.sh` gets the machine ready.** Packages, the idmap range, Incus,
the driver check, the filesystem probe, the uplink, the group. An Arch package
may place files and pull in dependencies, and it may not run `incus admin init`,
write to `/etc/subuid` or add an account to a group. So this half is a script,
the package installs it as `polyseat-prepare`, and there are three ways to run
it: from a terminal, from `install.sh`, and from the web interface.

The third way is why the script now avoids two things it used to take for
granted. It reads no terminal it has not tested for, which it already did for
the driver question; and it takes `POLYSEAT_INPUT_USER` for the account that
goes in the `input` group, because `SUDO_USER` answers that question only when a
person invoked it and a daemon started by systemd at boot has nobody to ask.
`NO_COLOR` and `POLYSEAT_FROM_DAEMON` are the other two: escape codes are noise
in a browser, and the closing "now start it" is wrong when it is already
running. Everything about it is safe to run again: it checks before it changes,
and an entry that already exists is left exactly as it is, including when it is
narrower than the one it would have written.

**`host/install.sh` places the files**, which is precisely what
`packaging/aur/PKGBUILD` does instead. It runs `prepare.sh` first, with `set -e`
in force, so a machine that cannot be made ready stops the install before a
single file has been placed.

The two paths differ in exactly one thing that is not a path: the checkout build
puts the binary and helpers under `/usr/local`, where a file placed by hand
belongs, and the package puts them under `/usr`, which is the only place it is
allowed to write. The daemon is the same binary either way and looks in both,
local first, which is the order a shell uses for the binary itself.

Both are tested against a fresh virtual machine, because neither can be
exercised on a machine that already has them: `host/test-install.sh` for the
checkout, `host/test-package.sh` for the package.

**The package is not in the AUR**, because new accounts cannot be registered at
present and there is therefore no account to upload from. It is built and
attached to every GitHub release instead, so `pacman -U` on that file is a real
way in and not a promise, and it is the one most people should take. The AUR
would have distributed the recipe rather than the package anyway: installing
from it means building it yourself, which is what the checkout already does.

The split above was not waiting on any of that. It is what makes
`polyseat-prepare` a command rather than a script in a checkout, and it was
worth doing before the package existed.

## What the installer must cover

Everything here is one-time, root-only host setup. Most of it was learned the
hard way, so the reasons are kept with the steps.

### Packages

`incus`, `bpftrace`, `python`, plus `go` while the daemon is built from source,
and `nvidia-container-toolkit` on NVIDIA machines only.

The installer installs the ones that are missing rather than printing a command
and stopping. **Deliberately not with `-Sy`:** refreshing the package database
and installing from it without upgrading is the partial upgrade Arch warns
about, and an installer is a bad place to break a system in a way that surfaces
weeks later. It installs from the database that is already there, and when that
database is too old to resolve, it says to run `pacman -Syu` first.

**This binds the installer to Arch.** It queries `pacman`. Rather than claiming
a portability nobody has tested, it says so and refuses elsewhere. A second
distribution is a later decision, not a guess to encode now.

Note that this binds only the *host*. Inside a seat nothing CachyOS specific is
left: Sunshine comes from LizardByte's own Arch package with each release, so a
seat is a plain Arch container. The M1 spike bootstrapped the CachyOS keyring
into every seat out of the host's package cache, which tied the seats to a
CachyOS host and was where the mirror lag problem came from. Provisioning
removes that repository from seats that still carry it.

### Which card is in the machine

Worked out before anything else, because it decides the rest: which package the
installer needs, which driver check has to pass and what the daemon builds
seats from. An AMD machine has no use for `nvidia-container-toolkit`, which is
a shim for a driver that is not there.

Render nodes under `/sys/class/drm/renderD*` first, since a node exists only
where a driver is bound to something that can render, and the PCI bus as a
fallback, since a machine whose driver is missing has no render node at all and
is exactly the machine this script exists to help. With cards from both vendors
present NVIDIA wins, the same rule the daemon follows. The daemon's choice can
be overridden with `gpu_render_node` in `/etc/polyseat/polyseatd.json`.

The two detections are checked against machines this one is not, in
`host/test-gpu-detect.sh` and `go test ./internal/seat -run GPU`.

### The AMD driver

`amdgpu` bound to the card, and that is the whole host requirement. There is no
userspace on the host for a seat to borrow: Mesa is an ordinary package inside
each seat, so nothing is injected and no host driver update can leave a seat
behind. Refused when the card came up on `simpledrm` or `vesa` instead, which
usually means missing firmware and `linux-firmware-amdgpu`.

`vainfo` is reported rather than refused over, both here and again inside the
seat during provisioning. **The AMD path has never been run on real hardware:**
[amd.md](amd.md) says what was verified and what was not.

### The NVIDIA driver

Checked and refused rather than installed or warned about.

What NVENC needs is the driver's own userspace, `libcuda.so.1` and
`libnvidia-encode.so.1`, which `nvidia-container-toolkit` injects into every seat
from the host. Both belong to `nvidia-utils`, established with `pacman -Qo`
rather than assumed. The `cuda` package is the toolkit, `nvcc` and the CUDA
runtime, and a seat needs none of it: the machine this was developed on happens
to have `cuda` installed, which is exactly the trap that makes "it works here"
worthless as evidence.

`lib32-nvidia-utils` is a warning rather than a refusal. Everything works without
it except the 32 bit games, and Steam's own client and a great many games are 32
bit.

Not installed, because a driver userspace has to match a kernel module and which
module package is right depends on the card and the kernel. Refused rather than
warned about, because a seat built without a working driver comes up, streams in
software and looks entirely healthy; the encoder line on its card is the only
place it shows, and by then somebody has spent twenty minutes on it.

### idmap ranges

`/etc/subuid` and `/etc/subgid` need an entry for **root**:

```
root:1000000:1000000000
```

CachyOS ships only an entry for the user. Without the root entry every container
start fails with `System doesn't have a functional idmap setup`, which does not
point at subuid at all.

The installer adds it, and leaves an existing root entry alone even when it is
narrower than this one: widening somebody's idmap ranges behind their back
changes what every other container on the machine may map.

### Incus itself

`systemctl enable --now incus.socket`, then `incus admin init`. `--minimal` is
enough to start and picks btrfs automatically when the root filesystem is btrfs.
The shared library pool of M6 wants btrfs, so this is worth checking rather than
assuming.

The installer does both. The init is skipped when a storage pool already exists,
because `incus admin init` fails on a machine that has one and there is no
sensible way to rerun it.

### What the host tools are called

Three of the scripts in `host/` are not development aids and ship as commands,
because somebody who installed the package has no checkout to find them in:

| in the repository | installed as |
|---|---|
| `host/prepare.sh` | `polyseat-prepare` |
| `host/uninstall.sh` | `polyseat-uninstall` |
| `host/lan-bridge.sh` | `polyseat-lan-bridge` |
| `host/check-hardening.sh` | `polyseat-check-hardening` |

The first two are also placed by `install.sh`, into `/usr/local/bin`, which the
other two are not. That is not symmetry for its own sake: the daemon looks those
two up by name, `/usr/local` first and `/usr` second, the same order it uses for
the input helpers, and a daemon built from a checkout has no way to find the
checkout it came from. Without them the two buttons in the interface would have
nothing to run. Nothing looks the other two up, and whoever has a checkout has
them under `host/` already.

The rest stay in the repository, because they are about the repository:
`install.sh` and `update.sh` are the checkout's own way in and out,
`reset-machine.sh` and the `test-*.sh` scripts exist to be run against a machine
somebody is willing to break.

Elsewhere in these documents the scripts are named by their path, because that
is where they are written and read. Where a document gives a command to type,
it uses the installed name.

### The uhid module

`modprobe uhid`, and `uhid` written into `/etc/modules-load.d/polyseat.conf` so
that the next boot does it too.

The daemon's observer attaches a kprobe to `uhid_dev_create2` to record which
container created each gamepad, which is what makes ownership a fact rather than
a guess. A kprobe attaches to a symbol the running kernel has, and where uhid is
a module, `CONFIG_UHID=m`, its symbols do not exist until it is loaded.

Nothing loads it early enough on its own. `/dev/uhid` is a static node, declared
in `modules.devname` and created at boot by systemd-tmpfiles, and the module is
autoloaded only when something first opens that node, which is the first seat
that runs a pad. Without this step the daemon starts seconds into boot, finds no
symbol, and the observer gives up until something happens to open the node.

**The node existing is not evidence that the module is loaded**, which is what
made this expensive to find: everything looks correctly installed, and the
warning in the interface that says to load the module tests for `/dev/uhid`,
which is there either way. That warning is right about what it actually asks,
which is whether a seat can have a gamepad at all, and it is left as it is.

The ordering at the next boot needs nothing added to the unit:
`systemd-modules-load.service` runs `Before=sysinit.target`, and `polyseatd`
hangs off `multi-user.target`, which is ordered after it.

Nothing is lost while the module is missing. Gamepads still work, and the broker
falls back to attributing them by name, which is the documented fallback and a
heuristic rather than a fact.

**Making it survive a reboot is not this script's doing.** The file that does
that ships with Polyseat: `/usr/lib/modules-load.d/polyseat.conf` from the
package and `/usr/local/lib/modules-load.d/polyseat.conf` from a checkout, both
of which systemd reads. Whichever installed it removes it, which is the whole
reason it is a shipped file rather than one written by hand.

It was written by hand, into `/etc`, in 0.3.2 to 0.3.4, and that is exactly the
problem it had: `pacman -Rns` could not remove a file the package did not own,
so removing Polyseat left a machine loading a module at every boot for something
that was gone. `polyseat-prepare` now takes that older copy out on the way past.

The module itself is left loaded. Unloading it would reach past this
installation, since uhid is also what bluez uses for HID over GATT.

### Group membership

**`incus-admin` is not needed.** The spike scripts needed the invoking user in
it, and with it the trap that `usermod -aG` does not affect the running session,
so the scripts had to re-exec themselves under `sg incus-admin`. None of that
applies any more: `polyseatd` runs as root from a systemd unit and opens the
Incus socket directly. Adding yourself to `incus-admin` is now purely a
convenience for running `incus` by hand.

**`input` is added by the installer**, for the account that invoked it. Worth
being precise about, because a step that grants an account read access to every
input device on the machine should not be there out of habit:

* The daemon does not need it. It is root and opens device nodes directly.
* The host-side tooling in this repository does. `uhidgen.py` opens `/dev/uhid`
  to make a synthetic gamepad for the M2 checks, and `/dev/uhid` and
  `/dev/uinput` are `root:input` mode 0660. Sunshine running on the host itself
  wants the same two nodes.
* On a desktop with an active local session logind grants that account an ACL on
  both anyway, so there the group changes nothing. Measured on the development
  machine: `getfacl /dev/uinput` shows `user:rooky:rw-` without any group
  membership being involved.

So it covers the cases where there is no such session to grant an ACL: a
headless host, a second administrator, a session that is not the active one.
`--uninstall` does not take it away again, because it is a property of the
account rather than of this installation.

## What is not required

Worth stating, because the machine this was developed on has both and it would
be easy to bake the assumption in without noticing.

**Passwordless sudo is not required.** Nothing in the daemon calls `sudo` on the
host; it is already root. The two `sudo -u player` calls run *inside* a
container, invoked by that container's root, and sudo does not authenticate a
caller whose uid is 0. The whole installation costs two password prompts, from a
checkout:

```
sudo ./host/install.sh
sudo systemctl enable --now polyseatd
```

and two from the package, which is the shorter of the two paths now that the
machine half moved into the interface:

```
sudo pacman -U polyseat-x86_64.pkg.tar.zst
sudo systemctl enable --now polyseatd
```

After that everything happens in the web interface, which needs no privileges of
its own because the daemon already has them. `host/check-hardening.sh` runs
unprivileged and only asks for root with `--fix`.

**A CachyOS host is not required either**, beyond the pacman check. Nothing
CachyOS specific is left inside a seat.

### One thing that is required, for the shared library only

The directory named by `library_dir`, `/srv/polyseat/library` unless changed,
has to be on a filesystem that can share blocks between files. **btrfs** can, and
so does **XFS** if it was created with `reflink=1`, which has been the mkfs
default since 2018. **ext4 cannot**, and neither can tmpfs or any network
filesystem.

The daemon does not take the filesystem's word for it. At startup it writes a
block and clones it, because reflink support is a property of the mount and the
kernel rather than of the label: XFS without `reflink=1` and btrfs with
`nodatacow` both look like filesystems that should work and do not.

Where the probe fails, the daemon starts normally and the library is off. The
web interface says which filesystem and why. That is deliberate, in both
directions: a missing games feature is no reason to take every seat down, and a
pool that quietly made full copies would fill the disk and only announce itself
when there was no space left to fix it in.

Nothing has to be set up for the games already on the machine, either. The daemon
looks for a Steam library on the host on every pass and takes it into the pool
when there is exactly one and it can share blocks with the pool, so this is one
more thing the installer has no business doing: the library may well be installed
after Polyseat is, and a one-time step at install time would miss it.

Everything else in Polyseat works on any filesystem.

### Host-side pieces

The udev rule under `/etc/udev/rules.d/`, the input helpers under
`/usr/local/lib/polyseat/`, the daemon at `/usr/local/bin/polyseatd` and its
systemd unit.

One unit, not three. The per-seat broker template and the observer unit are
gone; `polyseatd` supervises both itself so that the seat lifecycle has exactly
one owner. An installer that finds the old units removes them.

### Two things it reports rather than changes

Both are conditions the operator has to decide about, and both used to surface
only much later, after a seat had been built and something did not work.

**Whether the library filesystem can share blocks.** The installer asks the
filesystem instead of asking its name, by cloning a file the way the daemon does
at startup, and says plainly when the answer is no. Nothing fails on a no: every
other part of Polyseat works on any filesystem and the library simply stays off.

**Whether there is an uplink for the seats.** Each seat becomes a host of its own
on the LAN, through a macvlan on the uplink or, where the uplink is a bridge,
through a port on that bridge. Two things make that impossible and both are
quiet: no default route to take the interface from, and a wireless one, where
macvlan cannot work at all because 802.11 does not carry more than one MAC
address per association, and where bridging a station interface does not work
either for the same reason.

### Optional hardening

`polyseat-check-hardening --fix` pins `kernel.sysrq`. Everything else it finds is
reported rather than changed, because the remaining measures cost the machine
its text consoles. That judgement belongs to the operator, not to an installer.

## Whether the machine is ready, and where the panel sits

The daemon answers that from one fact, and deliberately not from a marker file
it writes itself: **whether root has an idmap range** in `/etc/subuid` and
`/etc/subgid`. That is asking the machine rather than asking a note somebody
left about it, which is the same choice the library probe makes when it clones a
file instead of reading a filesystem label. A marker would be wrong in both
directions, surviving a machine that was reset and missing on one prepared
before the marker existed.

It is the right single fact for three reasons. Nothing else on an Arch machine
writes that entry, since CachyOS ships one for the user and none for root.
Without it every container start fails with `System doesn't have a functional
idmap setup`, which names neither subuid nor Polyseat, so no seat can be built
and the message does not say why. And it is two small file reads, cheap enough
to answer on every state request. Everything else preparing does is either
already reported on its own — the uhid observer, the udev rule, the library
filesystem, the uplink — or cannot be asked from here at all, like whose account
is in the `input` group.

**Where the panel lives follows from that.** On a machine that is not ready it
is on the page itself, above the seats that cannot be built yet. On a machine
that is ready but has no seats it is still there, in ordinary colours rather
than a warning, because that is somebody's first look at this page and "is this
machine ready" should not need a dialog to answer. Once a seat exists it moves
behind the *Host* button, where it belongs among the things that are done
rarely and on purpose.

## Before the machine is ready

The daemon used to exit when it could not reach Incus, and on a machine that has
just installed the package that is every time: Incus is one of the things
`prepare.sh` installs, so there is no socket to connect to. systemd brought it
back five seconds later, and the interface that exists to explain exactly this
was the one thing that never came up.

It serves a smaller interface instead, from the same address with the same
certificate and the same password, and that password is the one that is there
afterwards. What is registered in that mode is what needs no seat: claiming the
machine, signing in, preparing it, restarting, and removing Polyseat.
Everything about seats is left unregistered rather than guarded, because the
manager those handlers talk to does not exist there. The handful that serve both
modes ask for the configuration through an accessor rather than through the
manager, which is the whole of what they had to learn.

The daemon reaches Incus at startup or not at all — the manager, the store and
every seat hang off that connection — so it does not grow the rest of itself
afterwards. It restarts into it. A goroutine looks for Incus every fifteen
seconds and schedules the restart through the same transient unit an update
uses, which is what makes preparing from the page finish by itself: the last
thing `prepare.sh` does is bring Incus up, and nobody has to be told what to
press next.

Two things that watcher deliberately does not do:

* **It does not restart while a prepare is running.** Incus comes up several
  steps before that script is finished, and `KillMode=mixed` means a restart
  takes the daemon's whole control group with it, pacman included. A pacman
  killed halfway leaves a lock and a partly applied transaction, which is a
  worse machine than the one this started with. The restart button and the
  update button refuse for the same reason.
* **It does not restart what systemd did not start.** systemd sets
  `INVOCATION_ID` for everything it runs; without it, telling systemd to restart
  `polyseatd.service` would restart a different process than the one running.

## What the daemon does instead

Everything per-seat and everything at runtime. This was a list of shell scripts
under `spike/m1-seat/` until M5; it is the daemon's own work now, and the scripts
are kept as the record of how each step was arrived at:

* create the container, attach its LAN interface, configure DHCP
* install packages inside the seat, including Steam in the base image
* repair the NVIDIA userspace that `nvidia.runtime` does not bring, or on AMD
  install Mesa and Vulkan as packages and repair nothing
* generate the Sunshine configuration, `XDG_SEAT` and the CSRF origins
* install the session units inside the seat
* start and stop the per-seat broker

The daemon also owns the seat lifecycle, which the broker does not: it must know
when a container is stopping and hold still, because polling into a stop once
drove Incus into a hung "Stopping instance".

## What the web interface does

Nothing on its own. It is a client of the daemon's API. Configuration is owned
by the daemon and everything else is a generated artifact, as set out in
[`architecture.md`](architecture.md).

## Removing it again

`host/uninstall.sh`, installed as `polyseat-uninstall`, and it is one file for
the same reason `prepare.sh` is: three ways in and one procedure.
`install.sh --uninstall` and `--purge` hand over to it, the package ships it so
that a machine which never had a checkout has a way out, and the daemon runs it
in a transient systemd unit when somebody presses the button in the interface.

Three things about it are not obvious and all three were paid for:

**It runs from a copy of itself.** `pacman -R polyseat` deletes
`/usr/bin/polyseat-uninstall` while bash is reading it, and bash reads a script
in chunks and comes back for the next one at a command boundary. So the first
thing the file does is copy itself into `/tmp` and hand over.

**It removes the package with `-R` and not `-Rs`.** The package depends on
incus, bpftrace and python. On a machine where somebody installed Polyseat first
and let pacman pull Incus in as a dependency, `-s` takes their container manager
away with it. The readme said `-Rns` before this file existed, which was that
same trap written down.

**The daemon stops first, before anything it owns is touched**, which is why
this is a command rather than a paragraph in a document.

`--seats` takes the seats as well, and exists for the order it does things in
rather than for the list of things it removes. The daemon supervises every seat
and reads inside each running one every ten seconds; deleting a container while
that is going on lands an exec in a shutdown, and Incus answers with a "Stopping
instance" task that never finishes. On this machine that took a restart of the
Incus daemon and killing the container's cgroup by hand to get out of, twice, so
both the order and the way out are written into the script:

* stop `polyseatd` first, before touching anything it owns
* stop each seat, wait a minute, and if Incus has accepted the stop and left the
  container running anyway, kill its cgroup and restart Incus
* only then delete the containers and the daemon's state
* and only then take the files and the package away

The interface offers exactly the same three choices, because it runs exactly the
same file: the daemon only, the seats with it, and the library with those. What
it adds is that the password is asked for every time, whatever
`update_needs_password` says, and that deleting seats needs the word typed out.
`"web_uninstall": false` turns the button off and leaves the command.

The seat names come from the seat records and from nowhere else. Matching
container names against a pattern would put an unrelated container one typo away
from being deleted.

The shared game library is kept unless `--library` is given, because it is the
expensive thing: the seats' copies come back from it by reflink in a second,
where downloading them again does not. Packages and Incus are left alone in both
cases. They are not Polyseat's to remove, and a script that uninstalls somebody's
container manager because it once installed it is a script nobody should run.

## Resetting the machine to test the installer on it

`host/reset-machine.sh` puts this machine back to before Polyseat, so that the
one path a developed-on machine can never test by accident can be tested
deliberately: what a new user does first.

It is not part of uninstalling Polyseat. `--uninstall` leaves the seats and
`--purge` leaves the packages and Incus, on purpose. This removes them anyway,
and keeps the shared game library unless `--library` is given, because the seats'
copies come back from it by reflink in a second.

Everything in it was learned by doing it by hand three times:

* Stop Incus before removing its package, or the running daemon outlives it. The
  first run left an `incusd`, two `dnsmasq` children and two bridges behind, all
  belonging to a package that was no longer installed.
* Delete the btrfs subvolumes before removing `/var/lib/incus`. `rm` cannot
  delete a subvolume, and the first attempt left two thirds of the directory
  there while reporting nothing.
* Unmount the three tmpfs mounts under it as well. `rm` says "device or resource
  busy" for those and carries on, which reads as success.
* Remove the leftover `incusbr` bridges. They outlive both the daemon and the
  package, and a stale one is the sort of thing that makes the next install look
  haunted.
* Do not pipe a long step through `sed` for the sake of indentation. It buffers
  when its output is not a terminal, and a working run that prints nothing for
  three minutes is indistinguishable from a hung one.

## Testing the installer

`host/test-install.sh` runs the installer against a throwaway Arch **virtual
machine** and checks what it did, including that a second run changes nothing
and that `--uninstall` removes what it should and keeps what it should.

`host/test-package.sh` covers the other way in, and since 0.4.0 it takes the
path somebody with a browser takes rather than the one somebody with a terminal
takes: install the package, start the daemon on a machine that has no Incus,
check that it stays up and says so, claim it over HTTPS, press prepare through
the API, wait for it, and check that the daemon restarts into the ordinary
interface by itself. Then `polyseat-prepare` is run over the top from a
terminal, which is the idempotence check and the other caller at once, and the
removal goes through the interface as well.

Two of those checks are worth naming, because they are the ones a change here
would break quietly: that the password chosen while the machine was not ready is
still the password afterwards, and that the removal left Incus and bpftrace
installed.

The check that matters most is not a file check. An installer can put every file
in the right place and still leave a machine where nothing runs, so the harness
carries out the last instruction the installer prints, `systemctl enable --now
polyseatd`, and then asks the interface for a page over HTTPS. The certificate
and the first password are generated on that first start, so a listener that
cannot complete a handshake, or a daemon that starts and quietly restarts in a
loop, is caught there and nowhere else.

A virtual machine rather than a container, because a container shares the host's
kernel and its udev, cannot run systemd units that touch devices, and would need
nesting to run Incus inside it. The installer talks to all three.

The reason this exists at all: the machine an installer is written on already
has everything it installs, so running it there proves only that it is
idempotent. The steps that matter most are exactly the ones such a machine never
reaches, and the harness therefore resets them before every run: the idmap entry
CachyOS does not ship, the first `incus admin init`, the group the account is
not yet in. Forgetting to reset the Incus setup made that check pass on every
run after the first whether the installer did anything or not.

## Order of work

The installer was finished **after** the daemon, not before. Its main job is
installing the daemon, so writing it earlier would have meant writing it twice,
and its bootstrap parts could not be tested on a machine that was already
bootstrapped. That last problem is what the virtual machine above solves.
