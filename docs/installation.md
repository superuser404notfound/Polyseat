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

**This binds the installer to Arch.** It queries `pacman`. Rather than claiming
a portability nobody has tested, it says so and refuses elsewhere. A second
distribution is a later decision, not a guess to encode now.

Note that this binds only the *host*. Inside a seat nothing CachyOS specific is
left: Sunshine comes from LizardByte's own Arch package with each release, so a
seat is a plain Arch container. The M1 spike bootstrapped the CachyOS keyring
into every seat out of the host's package cache, which tied the seats to a
CachyOS host and was where the mirror lag problem came from. Provisioning
removes that repository from seats that still carry it.

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

## Order of work

The installer is finished **after** the daemon, not before. Its main job will be
installing the daemon, so writing it now means writing it twice, and its
bootstrap parts cannot be tested on a machine that is already bootstrapped.
