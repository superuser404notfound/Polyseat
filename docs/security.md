# Security

What this setup actually guarantees, what it deliberately does not, and why.
Everything here was measured on the running two-seat rig on 2026-07-28 rather
than reasoned about.

## The threat model

Polyseat is built for a **local machine with people you trust**: a household,
where every seat belongs to somebody who already has physical access to the
computer. It is not built to hand a seat to a stranger over the internet, even
though nothing structurally prevents that.

That matters, because several of the decisions below are only defensible under
that assumption. Where they stop being defensible is called out.

## What holds, verified

**The containers are unprivileged.** Container root maps to host uid 1000000,
`security.privileged` is unset. A root shell in a seat is an ordinary
unprivileged user on the host.

**No host filesystem is mounted into a seat.** Neither container has a single
disk device. Everything a seat sees is its own storage volume.

**The Incus socket is not reachable from a seat.** It is `root:incus-admin 0660`
on the host and not passed through, so a seat cannot manage containers.

**Sunshine's web interface requires authentication.** Both seats answer `401` on
`/` and on `/api/config`. The credentials live in a separate `credentials/`
directory rather than in `sunshine_state.json`.

**A seat cannot reach the host over the LAN.** That is the macvlan property:
`ping` from either seat to the host's LAN address fails. The management bridge
is a separate matter, see below.

**Input devices are attributed structurally**, not by the name their creator
chose. For uinput the creating descriptor is asked directly through
`UI_GET_SYSNAME`; for uhid a kprobe records the creating process at the moment
of creation. Both forgery paths were demonstrated and refused:

```
! refused: created on the host, not in a container
! refused: name claims (seat1) but the kernel says 'seat2' created it
```

**The host desktop cannot read a seat's input devices.** They are
`root:root 0600` with the `uaccess` tag stripped, and opening one as the desktop
user raises `PermissionError`.

## What is deliberately accepted

### Passwordless sudo for the desktop user

`rooky ALL=(ALL) NOPASSWD: ALL`. Every process running as the desktop user is
effectively root, including anything a browser or a game on the host starts.
This is the single largest weakness on the host and it is a conscious choice for
a development machine. The safety net is btrfs snapshots through snapper rather
than access control.

### The account password equals the account name

Harmless while nothing listens on the network for it. It becomes the weakest
point in the whole setup the moment SSH or any other authenticating service is
enabled, which is why the console hardening in `host/` deliberately does **not**
recommend enabling SSH.

### Seats sit directly on the LAN

macvlan is what buys the setup its simplicity: every seat has its own address
and uses the standard Sunshine ports, so no port juggling is needed. The cost is
that a seat is a full participant in the home network. Measured:

```
seat1 -> seat2 (10.20.30.72)   reachable
seat1 -> gateway (10.20.30.1)  reachable
seat1 -> host LAN address      blocked
```

So a compromised game in a seat can scan and attack the network, the router and
every other machine on it, but not the host over that path. **This is the
assumption that breaks first if a seat is ever given to somebody you do not
trust.** The fix then is a separate VLAN or an isolated bridge rather than
macvlan onto the main network.

### Seats reach the host on the management bridge

`incusbr0` exists precisely so the host can talk to the seats, so the reverse
holds too. What is actually reachable there was measured:

```
port 22   closed        port 53    open  (Incus dnsmasq)
port 631  closed        port 5355  open  (systemd-resolved)
port 47989/47990 closed
```

The exposure is therefore dnsmasq and resolved, not a general door. Incus's own
nftables chain only accepts DNS and a few ICMP types from the bridge, but its
policy is `accept` rather than `drop`, so this is "nothing else listens" rather
than "a firewall says no".

### The GPU is shared without partitioning

Consumer NVIDIA hardware offers no isolation between contexts. A seat can
enumerate GPU processes host-wide through `nvidia-smi`, and the usual caveats
about reading another context's GPU memory apply. Nothing here changes that; it
is inherent to putting several tenants on one consumer card.

### Steam listens on the network inside a seat

`0.0.0.0:27036` for Remote Play discovery. Reachable from the LAN like any other
seat port.

### The daemon's web interface has no authentication

`polyseatd` runs as root and serves an unauthenticated API that can create,
delete and reconfigure containers. It binds `127.0.0.1` by default, so reaching
it means already running code on the host as some user.

On this machine that grants nothing new: the desktop user has passwordless sudo,
so anything that could talk to the daemon could already become root directly.
The moment either of those changes, the other has to as well. In particular,
moving the listener off localhost so a phone can reach it needs authentication
in front of it first, and that does not exist yet.

### The kernel console

Virtual keyboards reach the kernel VT and sysrq handlers exactly like a physical
keyboard, and there is no per-device switch. Handled in
[`../host/README.md`](../host/README.md): `kernel.sysrq` is pinned, and
`check-hardening.sh` reports when the exposure is actually open rather than
merely possible.

## The broker runs as root and consumes container-controlled data

Device names come from inside the seats, and the broker matches on them. Two
things keep that from being a hole:

* **Nothing is passed through a shell.** All external calls are argv lists.
  The one `sh -c` call interpolates only the major and minor numbers, which are
  integers read from sysfs.
* **The name no longer decides anything.** Since attribution became structural,
  a crafted name can at most produce a misleading log line.

## The inconsistency that was found here is gone

`seat1` used to carry `security.nesting=true` while `seat2` did not, for no
better reason than the order the two were built in. The daemon now sets the key
explicitly to `false` on every seat rather than leaving it implicit, and all
seats report the same configuration.

That is the general shape of the fix rather than a one-off: provisioning is a
recipe that converges, and a generation number marks seats built by an older
one, so drift like this shows up in the interface instead of waiting to be
noticed.

## If a seat is ever given to somebody untrusted

In this order:

1. **Change the account password**, then set up SSH with keys, then mask the
   gettys to close the console window.
2. **Take the seats off the main LAN.** A separate VLAN or an isolated bridge
   with forwarded ports instead of macvlan.
3. **Reconsider passwordless sudo** on the host.
4. Accept that GPU-level isolation is not achievable on this hardware.
