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

## Open

- **Long-run behaviour under load.** What was tested is two parallel streams
  with one game. Two demanding games at once will hit RAM before anything else.
- **Gamepads in parallel** were not exercised with two controllers at the same
  time.
