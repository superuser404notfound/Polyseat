# M4 - two seats in parallel

**Goal:** two people stream and play at the same time, each in their own seat,
with input strictly separated.

**Status: achieved** (2026-07-28). Two Moonlight clients connected at once, both
streams hardware encoded, keyboard and mouse cleanly separated, host desktop
untouched.

## How the second seat was created

Freshly through the M1 scripts with `CT=seat2 SEAT=seat2`, not by copying
`seat1`. That was deliberate: it tests whether the provisioning is actually
reproducible, and it exercises exactly what the daemon will do later.

It forced three improvements that are now part of the scripts:

- **Steam belongs in the base installation.** In a fresh container it installs
  without a single file conflict. On a seat where `nvidia.runtime` has already
  been enabled it cannot be installed afterwards at all, because the injection
  leaves real symlinks behind that never go away (finding from M3).
- **`DISPLAY=:0` belongs in the sway unit**, otherwise Steam only shows "Unable
  to open a connection to X".
- The **EGL check in `15-nvidia-userspace.sh` was measured too early.** It ran
  as the player before any session existed and reported a false negative. It now
  runs as root and only warns; the authoritative check is the encoder line in
  `30-verify.sh`.

## Measured with both streams live

Two clients connected, one game running in `seat1`:

```
Virtual devices on the host        13 (5 per seat plus 3 stale, see below)
seat1 holds                        5, all tagged (seat1)
seat2 holds                        5, all tagged (seat2)
Cross-contamination                none
Devices visible to host libinput   0
NVENC encoder sessions             2, average 59 fps
GPU utilisation                    10 %, 2267 MiB of 16376 MiB
RAM                                8 GB of 31 used
  seat1 (game running)             6.34 GiB
  seat2 (idle)                     511 MiB
```

Two hardware encoded streams cost the GPU almost nothing. **RAM is the limit, as
predicted:** an idle seat is cheap, a playing seat is not.

## The seat tag does the work

Every attached device carries the tag of the seat holding it. Nothing had to be
guessed and nothing crossed over. `XDG_SEAT` in the Sunshine unit plus the
broker's tag check is the whole mechanism, and it turned out to be enough.

Two brokers run side by side without interfering. They only touch devices below
`/devices/virtual/` whose name carries their own tag, so their sets are disjoint
by construction.

## Finding: a leftover Sunshine on the host

Three untagged devices were left over: `Mouse passthrough`, `Mouse passthrough
(absolute)`, `Keyboard passthrough`, all created at 20:29 the previous day.

The cause turned out to be a **third Sunshine instance running on the host
itself**, from `sunshine-headless.service` and `sway-sunshine.service`, user
units left over from the setup that preceded this project. It had been running
for 20 hours, holding `/dev/uinput` and listening on `0.0.0.0:47984/47989/47990`,
which made the host a third Moonlight host on the LAN.

It did no harm, because the hide rule catches its devices by name and the
brokers ignore them for lack of a tag. But it is worth recording for two
reasons:

- **The tag check proved itself on an accident.** Devices that belong to nobody
  are simply left alone, rather than being assigned to whichever seat asked
  first.
- **The daemon will need to notice this.** A stray Sunshine on the host competes
  for ports and mDNS names and produces devices nobody owns. That is a case for
  the doctor.

## The gamepad leak, and why the check missed it

While both streams were live, seat1's gamepad turned up **on the host**: the
desktop user could read `/dev/input/event258`, "Sunshine X-Box One (virtual) pad
(seat1)".

Two mistakes stacked on top of each other.

**The hide rule did not cover gamepads.** It matched `polyseat:*` and
`*passthrough*`. Sunshine names keyboard, mouse, touch and pen "... passthrough"
but names gamepads after the emulated model, so the pad fell through both
patterns. The rule now matches `Sunshine*` as well.

**Stripping `ID_INPUT*` is not enough on its own.** Those devices also carry the
`uaccess` tag, and systemd-logind then grants the active desktop user an ACL on
the node:

```
crw-rw----+ 1 root input 13, 258
user:rooky:rw-
```

Applications that open evdev nodes directly, which is exactly what games and
Steam do, do not care about `ID_INPUT` at all. The rule therefore also drops the
`uaccess` and `seat` tags and pins ownership to `root:root 0600`. The seats are
unaffected, because Incus creates its own node inside the container with its own
mode and the broker only reads `/sys`.

**And the verification method was blind to it.** Isolation had been checked with

```
libinput list-devices | grep -c passthrough
```

which reported 0 and looked perfect. But **libinput ignores gamepads by
design**, the very fact recorded back in M2. So the check could not have caught
a leaking pad even in principle. The honest check is to try opening the node as
the desktop user:

```
python3 -c "open('/dev/input/eventN','rb')"   # must raise PermissionError
```

The lesson is worth more than the fix: a verification that shares a blind spot
with the mechanism it verifies will confirm whatever you hoped for.

## Two remaining exposures, raised by joleuger in the issue

Both are real, both were measured here, and neither is fully closed yet.

**The kernel VT layer receives the seats' keystrokes.** Virtual keyboards are
attached to the kernel's keyboard handler just like physical ones:

```
"Keyboard passthrough (seat1)"   Handlers=sysrq kbd event26
"Logitech G502 X PLUS"           Handlers=sysrq kbd leds event20 mouse2
```

Measured on this host, the practical risk is currently small but not zero:

| | |
|---|---|
| `kernel.sysrq` | 16, only `sync` permitted, so no reboot or crash |
| tty2, running KDE | keyboard in disabled mode (K_OFF) |
| tty1, tty3 | Unicode mode, so active |
| getty units | none running |

While KDE holds the active VT, K_OFF also blocks VT switching, so a seat client
cannot reach a text console on its own. The window that stays open is the host
switching to a text console manually while a seat is streaming: from then on the
client types into that console.

Mitigations, none of them free: put unused VTs into K_OFF (which costs the host
its text consoles), run something that holds the VTs such as joleuger's
`fallbackdm`, or set `kernel.sysrq=0`. The daemon should at least *detect* the
situation, which is a case for the doctor.

**uinput inside a container is unrestricted, and our attribution is by name.**
A seat can create a virtual device with any name it likes, including one
carrying another seat's tag. The broker would then hand it to that other seat.
Nothing exploits this today, and every seat is ours, but it is worth being
precise about what the design actually guarantees: the seat tag protects against
*accidents*, not against a compromised seat.

This is exactly where vuinputd's approach is structurally better, because
mediating `/dev/uinput` makes attribution a property of who called, not of what
they wrote into the name. See `docs/architecture.md`.

## Cleaned up along the way

- The leftover `sunshine-headless.service` and `sway-sunshine.service` user
  units from the previous setup were stopped, disabled and deleted. With their
  Sunshine gone, the three untagged devices disappeared too.
- `85-sunshine-input-isolation.rules`, also from the old setup, was removed. It
  matched virtual devices by vendor `beef:dead`, which the current Sunshine no
  longer produces, so it had been matching nothing for some time.

## Open

- **Long-run behaviour under load.** What was tested is two parallel streams
  with one game. Two demanding games at once will hit RAM before anything else.
- **Gamepads in parallel** were not exercised with two controllers at the same
  time.
