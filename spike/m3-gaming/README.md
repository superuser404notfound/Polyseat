# M3 - a seat that actually plays

**Goal:** Steam, a real game, audio and a gamepad inside the seat.

**Status: achieved** (2026-07-28). Steam runs in the seat, a game starts, audio
arrives at the client, the controller is recognised.

This step has no scripts of its own; the changes went into
[`../m1-seat/`](../m1-seat/) and [`../m2-input-broker/`](../m2-input-broker/).
What follows is what was learned.

## Installing Steam: three traps

**1. Otherwise Steam pulls in a ten year old driver.** It depends on the virtual
packages `vulkan-driver` and `lib32-vulkan-driver`, and pacman picks the first
provider it finds, which in the CachyOS repository is
`lib32-nvidia-390xx-utils`. That package would overwrite exactly the injected
driver files.

The right move is to **declare** those dependencies satisfied, because inside a
seat the driver always comes from the host:

```
pacman -S --assume-installed vulkan-driver \
          --assume-installed lib32-vulkan-driver \
          --assume-installed opengl-driver \
          steam lib32-libglvnd lib32-vulkan-icd-loader
```

**2. `lib32-libglvnd` hard-depends on `lib32-mesa`**, whose
`/usr/lib32/libGLX_indirect.so.0` collides with a symlink from the NVIDIA
injection. Which leads to the next trap.

**3. `nvidia.runtime` does not clean up its symlinks.** On first start
libnvidia-container creates real symlinks in the container filesystem. Setting
`nvidia.runtime=false` later does **not** remove them; only the bind-mounted
libraries disappear.

So the M1 rule "packages first, then enable NVIDIA" only holds **for a fresh
container**. On a seat that has already been started, the symlinks stay behind as
file conflicts and have to be cleared one by one. Consequence for the daemon:
**Steam belongs in the base installation of the seat image**, not in a later
add-on install.

## Steam needs `DISPLAY`

Steam is an X11 application. sway only starts Xwayland when the first X client
appears, and until then sway's own environment contains **no** `DISPLAY`.
Without the variable all you get is a window saying "Unable to open a connection
to X", with nothing in the log, only a dialog visible in the stream.

With `DISPLAY=:0` Xwayland starts up cleanly. The variable therefore belongs in
the seat's session environment.

## Audio: the sink belongs to Sunshine

The null sink from M1 was a mistake, carried over from a setup without
containers. **When streaming, Sunshine creates its own sink**
(`sink-sunshine-stereo`, plus surround variants) and makes it the default so
that applications play into it. Setting `audio_sink` in the configuration to a
different sink makes Sunshine capture the wrong one: the game plays into
Sunshine's sink and what gets transmitted is **silence**.

The log shows this unambiguously and it is still easy to miss, because
everything looks healthy: Opus initialised, a sink input running, just into a
different sink than the one being captured.

There is no real sound card inside the container. The reason one protects the
default sink on a host therefore does not exist here at all. `audio_sink` stays
empty and Sunshine handles it itself.

## Gamepad: `/dev/uhid` is not optional

If `/dev/uhid` is not passed through, Sunshine happily logs "Gamepad 0 will be
Xbox One controller" while no device ever appears inside the seat. Keyboard,
mouse, touch and pen work perfectly throughout, which makes the failure look
like a gamepad problem on the client side.

**Which interface a pad actually uses depends on the emulated model**, measured
in M4 with two controllers connected at once:

```
Sunshine X-Box One (virtual) pad   ->  uinput
Sunshine PS5 (virtual) pad         ->  uhid  (0005:054C:0CE6)
```

The log says where the choice came from, either "(default)" or "(auto-selected
by client-reported type)", and Sunshine's `gamepad` option can force one.

The part that stays unexplained: without uhid *no* pad appears at all, not even
an Xbox One one, although that model uses uinput. So uhid seems to be needed for
gamepad support to come up at all, whatever the pad ends up using. An earlier
version of this document claimed all gamepads go through uhid, which is wrong
and has been corrected upstream in the issues where it was repeated.

Both devices belong in every seat:

```
incus config device add SEAT uinput unix-char source=/dev/uinput mode=0666
incus config device add SEAT uhid   unix-char source=/dev/uhid   mode=0666
```

## The broker needed two fixes

- **The name pattern was too narrow.** `passthrough` matches keyboard and mouse,
  but a gamepad is named after the emulated model. The pattern is now a regular
  expression.
- **And thereby too dangerous:** a pattern containing "Controller" would also
  have matched the host's ASRock LED controller. The broker therefore only
  touches devices below `/devices/virtual/`: only what was created through
  uinput or uhid may enter a seat at all.

## An operational problem that concerns the daemon

During a container restart Incus got stuck: the container was dead, no
processes, no cgroup, yet Incus kept reporting `RUNNING` and hung on a
non-cancellable "Stopping instance". Only a restart of the Incus daemon resolved
it.

The suspicion falls on the broker prototype, which calls `incus exec` twice a
second and probably ran into the stop. **The daemon must not poll blindly**; it
has to know the container lifecycle and hold still during a stop.

## Open

- **Proton in detail.** What was tested is a game that starts, not shader
  compilation, controller rumble or the Steam overlay.
- **Steam Input** uses hidraw alongside SDL. The broker so far only passes
  through `event*` nodes, no `hidraw*`. Nothing has been missing so far, but the
  gap is known.
