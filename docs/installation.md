# Installation: who does what

Three layers, and the boundary between them is the point of this document.
`host/install.sh` covers the host side; this records what it must eventually
cover as well, and what must not end up in it.

## The rule

**The installer does what a daemon cannot do for itself.** Everything else
belongs to the daemon, and the web interface does nothing at all except talk to
the daemon.

## What the installer must cover

Everything here is one-time, root-only host setup. Most of it was learned the
hard way, so the reasons are kept with the steps.

### Packages

`incus`, `nvidia-container-toolkit`, `bpftrace`, `python`, plus `go` while the
daemon is built from source.

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
caller whose uid is 0. The whole installation costs two password prompts:

```
sudo ./host/install.sh
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

**Whether there is an uplink for the seats.** Each seat gets a macvlan interface
so it is a host of its own on the LAN. Two things make that impossible and both
are quiet: no default route to take the interface from, and a wireless one,
where macvlan cannot work at all because 802.11 does not carry more than one MAC
address per association.

### Optional hardening

`host/check-hardening.sh --fix` pins `kernel.sysrq`. Everything else it finds is
reported rather than changed, because the remaining measures cost the machine
its text consoles. That judgement belongs to the operator, not to an installer.

## What the daemon does instead

Everything per-seat and everything at runtime. Today it lives as shell scripts
under `spike/m1-seat/` and moves into the daemon in M5:

* create the container, attach the macvlan interface, configure DHCP
* install packages inside the seat, including Steam in the base image
* repair the NVIDIA userspace that `nvidia.runtime` does not bring
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

`--uninstall` takes out the daemon, its unit, the udev rule and the helpers, and
deliberately leaves the seats, their containers and `/var/lib/polyseat` alone.
Installing again picks them up where they were.

`--purge` takes the seats as well, and exists for the order it does things in
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
