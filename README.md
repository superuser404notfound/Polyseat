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

**One seat plays.** An Incus container running headless Sway and Sunshine
streams via NVENC to a Moonlight client; Steam runs inside it, a game starts,
audio arrives, and the client's keyboard, mouse and gamepad reach the session.
Confirmed on real hardware on 2026-07-28.

Not a product yet: no daemon, no GUI, everything driven by hand through scripts.
The logs of each step live in [`spike/`](spike/) and record what works, what
does not, and why.

Architecture and the reasoning behind it: [`docs/architecture.md`](docs/architecture.md).

## Roadmap

| | | |
|---|---|---|
| **M0** | Input spike: does the container architecture hold up? | ✅ |
| **M1** | One seat: Sway + Sunshine + NVENC + Moonlight | ✅ |
| **M2** | Input broker: keyboard, mouse and pad reach the seat | ✅ |
| **M3** | A seat that actually plays: Steam, Proton, pad, audio | ✅ |
| **M4** | Two seats in parallel (seat tag solved: `XDG_SEAT`) | |
| **M5** | Daemon + GUI: create, start, pair and monitor seats | |
| **M6** | Shared library pool on btrfs | |
| **M7** | Polish: per-client resolution, library scanner, finishing touches | |

## License

GPL-3.0-or-later. See [LICENSE](LICENSE).
