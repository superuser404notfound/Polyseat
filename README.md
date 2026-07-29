# Polyseat

Several people playing on one Linux PC at the same time - each in their own
cleanly isolated session, streamed to their own Moonlight client. The machine's
regular desktop keeps running undisturbed.

Polyseat implements **neither a compositor, nor an encoder, nor a streaming
protocol**. The heavy lifting is done by Incus, Sway/wlroots, Sunshine,
PipeWire, udev and systemd. Polyseat is the orchestrator on top: it creates
seats, wires them up collision-free, assigns input devices, monitors and repairs.

A **seat** is an Incus system container with its own headless Sway, its own
Sunshine instance, its own PipeWire and its own Steam account.

## Status

**Seats are created from the web interface and play in parallel.** Each seat is
an Incus container running headless Sway and Sunshine, streaming via NVENC to
its own Moonlight client. Steam runs inside them, games start, audio arrives,
and each client's keyboard, mouse and gamepad reach exactly its own session. No
crossover between seats, and the host desktop sees none of their devices.

`polyseatd` builds a seat from nothing in one click: container, network, driver
userspace, Steam, session, input broker. It supervises the brokers, follows the
Incus event stream instead of polling, and converges seats that were built by an
older recipe.

Confirmed on real hardware on 2026-07-28. The logs of each step live in
[`spike/`](spike/) and record what works, what does not, and why.

Pairing happens here too, for every seat, rather than in one Sunshine page per
seat on a port of its own. The daemon owns each seat's Sunshine login and talks
to it on your behalf.

**A game installed once is playable in every seat, without being downloaded
again.** That includes the games that were already on the machine, and it keeps
working after an update: the host's library is watched rather than imported
once, and a seat that is behind is brought forward as soon as nothing in it is
using the shared library.

Each seat still has its own private, fully writable Steam library; the daemon
replicates game directories between them with reflinks, so the copies share
their blocks on disk. Taking this machine's 69 GB library into the pool took 0.8
seconds and cost 432 KB.

**Launchers other than Steam work too.** Each seat has a `shared/` directory
where one folder is one game; point Heroic, Lutris or Bottles at it and the game
appears in the other seats by itself.

**A seat is something you can actually sit down in front of.** Connecting no
longer drops you at a bare terminal: there is a desktop with an application
launcher, a bar and a file manager, and Moonlight's app list is generated from
what the seat really has, so Steam Big Picture and any launcher that has been
installed are one pick away before a stream even starts. Software goes in from
either end, with no password and no root: the player types
`flatpak install ...` in the seat, or somebody installs it into that seat from
the Polyseat web interface. Each client also gets the resolution it asked for,
because the seat's output is virtual and simply becomes that size, and the
framerate it asked for, capped from outside so that games can stay uncapped
rather than paying vsync latency for it.

**A controller is enough.** Streaming from an Apple TV or a phone means no
keyboard and no mouse, and neither Moonlight nor Steam can supply them for a
launcher's login form. So the seat carries both: an on-screen keyboard, and a
pointer driven by the gamepad. Nothing happens until Select and Start are
pressed together, so a controller in a game stays a controller.

It does not share licences: a seat can only play what its own account owns. It
needs a filesystem that can share blocks, which means btrfs or XFS created with
`reflink=1`, and the daemon says so plainly rather than quietly making full
copies.

Architecture and the reasoning behind it: [`docs/architecture.md`](docs/architecture.md).
What the isolation actually guarantees, measured rather than assumed:
[`docs/security.md`](docs/security.md). Who installs what, and where the line
between installer, daemon and interface runs:
[`docs/installation.md`](docs/installation.md).

## Running it

**Once per machine:**

```
sudo host/install.sh
sudo systemctl enable --now polyseatd
```

That builds the daemon, places the input helpers under `/usr/local/lib/polyseat`,
installs the udev rule that keeps seat devices off the host desktop, and
registers one systemd unit. It creates no seats.

**Everything else happens at `https://<this machine>:47800`.** The first
password is generated on first start and written to the log:

```
journalctl -u polyseatd | grep password
```

Sign in, change it under *Account*, then add a seat and press provision. The
daemon downloads the image, installs the packages, repairs the NVIDIA userspace
that the driver injection leaves incomplete, generates the Sunshine
configuration and starts the session.

**Pairing a device** happens under *Devices and pairing* on the seat's card.
Point Moonlight at the seat, type the PIN it shows into that field, done. The
same panel lists what is already paired and can unpair it, and shows the seat's
own Sunshine login for when you want to go there directly.

It answers on the whole network, so seats can be managed from the same phone
that runs Moonlight. The certificate is self signed, so the browser asks once,
exactly like Sunshine's own interface does. To keep it on this machine instead,
set `listen` to `127.0.0.1:47800` in `/etc/polyseat/polyseatd.json`.

**Reading what happened.** Each seat card carries its own log, and the useful
lines in it come from the input broker. It says for every device whether its
owner was verified structurally, correlated, or merely claimed by name:

```
+ event29    Keyboard passthrough (seat2)
  creator verified: ID_INPUT=1 ID_INPUT_KEY=1 ID_INPUT_KEYBOARD=1
! event260   refused: name claims (seat1) but the kernel says 'seat2' created it
```

The other line worth watching is the encoder. A seat whose EGL landed on Mesa
still starts, still streams and still looks healthy; it just encodes in
software. The card shows `h264_nvenc` when it is right and says so plainly when
it is not.

For the host itself:

```
host/check-hardening.sh     # console and device exposures
journalctl -fu polyseatd
```

## Roadmap

| | | |
|---|---|---|
| **M0** | Input spike: does the container architecture hold up? | ✅ |
| **M1** | One seat: Sway + Sunshine + NVENC + Moonlight | ✅ |
| **M2** | Input broker: keyboard, mouse and pad reach the seat | ✅ |
| **M3** | A seat that actually plays: Steam, Proton, pad, audio | ✅ |
| **M4** | Two seats in parallel, input strictly separated | ✅ |
| **M5** | Daemon + GUI: create, start, pair and monitor seats | ✅ |
| **M6** | Shared game library: install once, play in every seat | ✅ |
| **M7** | A usable seat: desktop, app list, software, resolution and framerate per client | ✅ |

## License

GPL-3.0-or-later. See [LICENSE](LICENSE).
