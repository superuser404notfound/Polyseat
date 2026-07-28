# Installation: who does what

Three layers, and the boundary between them is the point of this document.
`host/install.sh` exists today in a deliberately small form; this records what
it must eventually cover, and what must not end up in it.

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
a portability nobody has tested, it should say so and refuse elsewhere. A second
distribution is a later decision, not a guess to encode now.

Watch out for mirror lag: an install once failed because two CachyOS mirrors
served a stale index, which looks like a broken package rather than a broken
mirror. Worth a clear error rather than a confusing one.

### idmap ranges

`/etc/subuid` and `/etc/subgid` need an entry for **root**:

```
root:1000000:1000000000
```

CachyOS ships only an entry for the user. Without the root entry every container
start fails with `System doesn't have a functional idmap setup`, which does not
point at subuid at all.

### Incus itself

`systemctl enable --now incus.socket`, then `incus admin init`. `--minimal` is
enough to start and picks btrfs automatically when the root filesystem is btrfs.
The shared library pool of M6 wants btrfs, so this is worth checking rather than
assuming.

### Group membership, and the trap in it

`usermod -aG incus-admin <user>` **does not affect the running session**. The
group only applies after the next login. An installer that adds the group and
then calls `incus` in the same run fails on exactly the machine it is meant to
set up, the fresh one.

Both ways out are known: re-exec under `sg incus-admin`, which is what the spike
scripts do, or tell the user to log in again and stop. Silently failing is the
one option that is not acceptable.

### Host-side pieces

The udev rule under `/etc/udev/rules.d/`, the broker and observer under
`/usr/local/lib/polyseat/`, their systemd units, and later the daemon binary
with its own unit and configuration directory.

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
