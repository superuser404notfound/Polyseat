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

**Two seats play in parallel.** Two Incus containers, each running headless Sway
and Sunshine, stream via NVENC to their own Moonlight client. Steam runs inside
them, games start, audio arrives, and each client's keyboard, mouse and gamepad
reach exactly its own session. Confirmed on real hardware on 2026-07-28: no
crossover between the seats, and the host desktop sees none of their devices.

Not a product yet: no daemon, no GUI, everything driven by hand through scripts.
The logs of each step live in [`spike/`](spike/) and record what works, what
does not, and why.

Architecture and the reasoning behind it: [`docs/architecture.md`](docs/architecture.md).
What the isolation actually guarantees, measured rather than assumed:
[`docs/security.md`](docs/security.md).

## Running it

Not a product yet, so this is the manual route. The daemon will replace all of
it.

**Once per machine:**

```
sudo host/install.sh
sudo systemctl enable --now polyseat-uhid-observer.service
```

That places the host-side pieces under `/usr/local/lib/polyseat`, installs the
udev rule that keeps seat devices off the host desktop, and registers the
systemd units. It creates no seats.

**Per seat**, until the daemon exists, through the M1 scripts:

```
cd spike/m1-seat
CT=seat1 SEAT=seat1 ./10-create-seat.sh
CT=seat1 SEAT=seat1 ./15-nvidia-userspace.sh
CT=seat1 SEAT=seat1 ./20-session.sh
CT=seat1 SEAT=seat1 ./30-verify.sh          # prints the addresses to connect to
sudo systemctl enable --now polyseat-broker@seat1.service
incus config set seat1 boot.autostart=true
```

**Checking afterwards:**

```
host/check-hardening.sh                     # host-side exposures
cd spike/m1-seat && CT=seat1 ./30-verify.sh # the seat itself
journalctl -fu polyseat-broker@seat1        # which device went where and why
```

The broker log is the useful one. It says for every device whether its owner was
verified, correlated or merely claimed:

```
+ event29    Keyboard passthrough (seat2)
  creator verified: ID_INPUT=1 ID_INPUT_KEY=1 ID_INPUT_KEYBOARD=1
! event260   refused: name claims (seat1) but the kernel says 'seat2' created it
```

## Roadmap

| | | |
|---|---|---|
| **M0** | Input spike: does the container architecture hold up? | ✅ |
| **M1** | One seat: Sway + Sunshine + NVENC + Moonlight | ✅ |
| **M2** | Input broker: keyboard, mouse and pad reach the seat | ✅ |
| **M3** | A seat that actually plays: Steam, Proton, pad, audio | ✅ |
| **M4** | Two seats in parallel, input strictly separated | ✅ |
| **M5** | Daemon + GUI: create, start, pair and monitor seats | |
| **M6** | Shared library pool on btrfs | |
| **M7** | Polish: per-client resolution, library scanner, finishing touches | |

## License

GPL-3.0-or-later. See [LICENSE](LICENSE).
