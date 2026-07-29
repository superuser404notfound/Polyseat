# Security

What this setup actually guarantees, what it deliberately does not, and why.
Everything here was measured on the running two-seat rig rather than reasoned
about, on 2026-07-28 and, for what M7 added, on 2026-07-29.

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

**A seat sees no host filesystem except its own share of the game library.**
This used to be an unqualified claim, and M6 broke it: a seat that takes part in
the shared library gets one `disk` device. What it is allowed to reach is
narrow, and the narrowness is the point:

- The mount is `<library_dir>/seats/<name>`, a directory belonging to that one
  seat. It is not the pool and not any other seat's directory, so a seat can
  neither read what its neighbours installed nor write into the canonical copy.
- The daemon does the cloning in the other direction, from the host, and never
  runs anything from inside a seat to do it.
- Nothing else on the host is exposed. The pool lives under its own directory
  and the seat mount is a sibling of the other seats', not a parent of them.

A seat without the library still has no disk device at all. Turning it off is a
real change and not a label, since the device is removed on the next provision.

What a seat can do through this that it could not before: fill the filesystem
holding the library, and hand the other seats a game directory with contents of
its choosing, because a harvested title is cloned onward as it stands. Neither
is interesting under the threat model above, where the seats belong to people
in the same house who could equally hand each other a file by any other means.
Both would matter if a seat were ever given to a stranger, which is one more
reason not to.

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

That used to depend on the device being named something the udev rule knew, and
M7 found what that costs. A seat running antimicrox to turn its gamepad into a
mouse produced devices called `antimicrox Mouse Emulation`, which matched none
of the three patterns in the rule and none of the patterns the broker scanned
for. The result was both halves wrong at once: the devices stayed readable on
the host, so a controller in a seat moved the host's cursor, and they never
reached the seat that made them, so the mapping did nothing where it was meant
to.

Names no longer decide either question. The broker considers every virtual
input device and lets the structural check say whose it is, and anything it
attributes to its seat is taken off the host by the broker itself. Verified
with a device deliberately named nothing in particular:

```
seat creates "totally-unlisted-widget"   host: crw------- root root   seat: attached
host creates "hostside-widget"           host: crw-rw---- root input  untouched
```

The second line matters as much as the first. This machine runs Steam on the
host as well, and Steam makes virtual gamepads the desktop is supposed to see,
so "hide every virtual device" would have been the wrong rule.

**A gamepad is two devices and only one of them was being hidden.** Made
through uhid, it appears both under `/dev/input` and as `/dev/hidrawN`, and
hidraw is the one Steam reads a DualSense through. The rule here only ever
matched `SUBSYSTEM=="input"`, and Sunshine ships udev rules of its own that
hand both halves to the desktop user, which is correct for a Sunshine running
on this machine and wrong for one running in a seat. So a seat's controller
was reaching the host's Steam the whole time:

```
before   /dev/hidraw14  Sunshine PS5 (virtual) pad (seat1)  root:root 660  user:rooky:rw-
after    /dev/hidraw14  Sunshine PS5 (virtual) pad (seat1)  root:root 600  no entry
```

Both halves are covered now, by name in the rule and structurally in the
broker, which finds the hidraw node through sysfs from the event device it has
already attributed.

The permissions are not the whole test either, and reading them as though they
were is how this was missed once already. logind grants the desktop user an
access control entry through the uaccess tag, and that entry survives a mode
change: a node can read `root:root 0600` and still be open to somebody. The
broker checks for it and the rule strips the tag.

**Where the rule sits in the sequence is load bearing.** It used to be
`70-polyseat-hide.rules`, and `70-uaccess.rules` sorts after that, so the tag
was stripped and then put straight back. It is `72-` now: after everything that
grants, before `73-seat-late.rules`, which is what turns the tag into an actual
entry on the node.

**Covering it by name was not enough either, and the half second cost real
exposure.** A raw HID node has no `name` attribute, so no pattern can reach one:
the rule left it alone and the broker sealed it on its next pass. That is half a
second, and the host's Steam was found holding a seat's controller open, having
taken it in exactly that window. Permissions are checked when a file is opened
and never again, so sealing it afterwards does not take it back.

The answer is that the structural check does run in udev after all, for the
devices that matter most. The uinput half genuinely cannot: reading a foreign
descriptor needs `pidfd_open` and `pidfd_getfd`, and udevd's workers are behind
a filter that blocks both. But every gamepad is a uhid device, and the observer
writes down which container created each one at the moment the kernel makes it,
keyed by the HID id that is part of every path underneath. Reading that file is
allowed. So a gamepad and its raw HID node are both sealed at creation, before
anything can open either:

```
/sys/.../uhid/0005:054C:0CE6.0056/hidraw/hidraw14   POLYSEAT_OWNER=container
/sys/.../uhid/0005:054C:0CE6.0056/input/.../event257 POLYSEAT_OWNER=container
a keyboard plugged into the host                     POLYSEAT_OWNER=unknown
```

A controller the host pairs over Bluetooth is a uhid device too. The observer
has no record of it, so it comes back unknown and is left alone.

The structural check cannot run in udev, which is where it would ideally
happen. systemd-udevd runs its workers behind a syscall filter that blocks
`pidfd_open` and `pidfd_getfd`, and reading a foreign descriptor needs both:

```
under udevd's filter   POLYSEAT_OWNER=unknown
anywhere else          POLYSEAT_OWNER=container
```

So the name patterns stay in the udev rule as a fast path that closes the
window to zero for everything already known, and the broker closes the case of
everything else within one poll interval. The residual exposure is that half
second, for a device nobody has named in the rule, on a machine where the
threat model is people in the same house.

**The daemon's interface demands a password and speaks only TLS.** It listens on
all interfaces, so this is the wall. Measured against the running daemon:

```
without a session   /api/state         401
                    /api/events        401
                    DELETE /api/seats/seat1   401
                    /  (the page itself)      200
wrong password      401, same message for a wrong user name
6 failed attempts   429, then a doubling delay per further attempt
forged cookie       401
real cookie with the expiry moved forward   401
```

The details behind that:

* The password is stored as an **argon2id** hash, memory hard on purpose. The
  alternative, a fast hash with many rounds, is exactly what a GPU is good at.
* The session cookie is **HMAC signed** rather than stored server side, so a
  daemon restart does not sign everybody out. The signature covers the expiry,
  which is why moving it forward fails.
* The cookie is `HttpOnly`, `Secure` and `SameSite=Strict`. That last one is
  what stops another site from making a browser act on the session: every
  state changing call here is a plain request carrying a cookie, and without it
  a link in a mail could delete a seat.
* **Changing the password ends every session**, because it rotates the signing
  key. That is the behaviour people expect from changing a password and would
  otherwise not get from signed cookies.
* The certificate is **self signed**, so the browser asks once. Same as
  Sunshine, whose own interfaces the seat cards link to at
  `https://<seat>:47990`. The daemon logs the fingerprint at startup so it can
  be compared against what the browser is asking to trust.

**There is a window in which the machine can be claimed, and it is deliberate.**
A daemon that has never been set up has no credentials at all, and the first
person to open the page chooses the password. Until they do, anybody who can
reach the page could choose it instead. Sunshine makes the same trade.

This replaced the opposite one, and the reason is worth stating rather than
implying. The first version generated a password on first start and wrote it to
the log, so the window never existed; what it cost was a terminal, on a machine
whose entire point is that it is driven from a browser and a gamepad. `journalctl
-u polyseatd | grep password` is not a step that belongs in front of somebody
setting up a games machine for their household.

What limits it: the window is open only until somebody walks through it, and it
closes behind them. `Claim` checks and writes as one step, so two browsers
arriving at the same moment cannot both succeed. An unclaimed store refuses
every sign in rather than accepting an empty form, which it would otherwise do,
since comparing no stored name and hash against an empty name and password
succeeds on both counts. And the daemon says on the way up that it has no
password yet, so a machine sitting unclaimed is visible in its log rather than
silent.

What does not limit it: nothing about the network. This is a tool for a
household's own machine on its own network, which is the threat model at the top
of this document, and it is not one to expose to the internet. Set the password
when the daemon is first started, not later.

The static page is served without a session on purpose. It is the same markup
for everybody and useless without the API behind it, and serving it openly is
what lets the page render a login form at all.

**Each seat's Sunshine login belongs to the daemon.** It generates one while
provisioning and keeps it in `/var/lib/polyseat/secrets/`, `0600` and root only.
Deliberately not part of the seat definition, because that is what the interface
reads on every refresh and a password has no business travelling with it; it is
fetched from its own endpoint when somebody asks to see it.

The daemon reaches Sunshine over the Incus bridge with **certificate
verification off**. Sunshine serves a certificate it generated for itself, so
there is nothing to verify against. What makes that acceptable is the path
rather than the certificate: the connection runs from the host to a container of
its own over a local bridge, and anything positioned to intercept it is already
root on this machine.

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

### The interface answers on the whole network

Listening only on localhost would be safer and was the default until the
interface grew a password. It is on the network because that is the point: seats
are meant to be manageable from the couch, from the same phone that runs
Moonlight.

What that makes the password worth being clear about: **a valid session is full
control of a daemon running as root.** It can create and destroy every seat and
everything installed in it. It cannot reconfigure the daemon itself, since the
bootstrap configuration is read only over the API, and nothing from a request
reaches a shell on the host, but that is a limit on the blast radius rather than
a second wall.

So the password carries real weight and should not be a short one. The interface
refuses anything under eight characters for that reason.

Anyone who wants the old posture back sets `listen` to `127.0.0.1:47800` in
`/etc/polyseat/polyseatd.json`.

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

### bwrap is setuid inside a seat

Seats carry `bubblewrap-suid` rather than the plain package, which means one
setuid root binary in each container. It is there so that flatpak works, and the
alternative was worse.

The measurement, because the shape of the problem is not obvious. An
unprivileged container refuses to mount a fresh `/proc` inside a nested
namespace. bwrap only needs that when it also unshares the pid namespace, and
that is exactly what separates the two users of it here:

```
steam pressure-vessel, no --unshare-pid    works
flatpak, --unshare-pid                     bwrap: Can't mount proc on /newroot/proc
```

So Proton was never affected and never will be by this; only flatpak was. The
obvious fix is `security.nesting=true` on the container, which relaxes what the
whole container may do for the sake of one program. This is the narrower one:
the container is unprivileged, so its root is host uid 1000000, and a setuid
binary in it grants that and nothing more. Someone who gained container root
through bwrap would hold an ordinary unprivileged account on the host, which is
the same position a compromised game in the seat already holds.

`security.nesting` stays `false`, and is still set explicitly on every seat.

### A seat can install its own software

The player has no sudo, so `flatpak --user` is the route: the installation lives
under the player's home, the Flathub remote is added per user rather than system
wide, and nothing is written outside that home. The daemon's own install button
runs the same command as the same user, so what the daemon installs and what the
player installs are one list with one set of rules.

What this widens: a seat can fetch and run arbitrary code from Flathub. Under
the threat model at the top that is not a new power, since a seat already runs
whatever its Steam account owns. It would matter for a seat given to a stranger,
where the answer is the same as everywhere else on this page: do not.

### Steam listens on the network inside a seat

`0.0.0.0:27036` for Remote Play discovery. Reachable from the LAN like any other
seat port.

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
