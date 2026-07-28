# polyseat

Mehrere Leute spielen gleichzeitig an einem Linux-PC — jede und jeder in einer
eigenen, sauber abgeschotteten Session, gestreamt an den eigenen
Moonlight-Client. Der Desktop des Rechners läuft dabei ungestört weiter.

polyseat implementiert **weder Compositor noch Encoder noch Streaming-Protokoll**.
Die eigentliche Arbeit machen Incus, Sway/wlroots, Sunshine, PipeWire, udev und
systemd. polyseat ist der Orchestrator darüber: er legt Seats an, verdrahtet sie
kollisionsfrei, weist Eingabegeräte zu, überwacht und repariert.

Ein **Seat** ist ein Incus-System-Container mit eigenem Sway (headless), eigener
Sunshine-Instanz, eigenem PipeWire und eigenem Steam-Konto.

## Status

**Ein Seat spielt.** Ein Incus-Container mit headless Sway und Sunshine streamt
mit NVENC an einen Moonlight-Client; Steam läuft darin, ein Spiel startet, Ton
kommt an, und Tastatur, Maus und Gamepad des Clients erreichen die Session.
Am 2026-07-28 am echten Gerät bestätigt.

Noch kein Produkt: kein Daemon, keine GUI, alles von Hand über Skripte. Die
Protokolle der einzelnen Schritte liegen in [`spike/`](spike/) und halten fest,
was funktioniert, was nicht, und warum.

Architektur und die Begründungen dahinter: [`docs/architecture.md`](docs/architecture.md).

## Fahrplan

| | | |
|---|---|---|
| **M0** | Input-Spike: trägt die Container-Architektur? | ✅ |
| **M1** | Ein Seat: Sway + Sunshine + NVENC + Moonlight | ✅ |
| **M2** | Input-Broker: Tastatur, Maus und Pad erreichen den Seat | ✅ |
| **M3** | Ein Seat, der wirklich spielt: Steam, Proton, Pad, Ton | ✅ |
| **M4** | Zwei Seats parallel (Seat-Tag steht: `XDG_SEAT`) | |
| **M5** | Daemon + GUI: Seats anlegen, starten, koppeln, überwachen | |
| **M6** | Geteilter Bibliotheks-Pool auf btrfs | |
| **M7** | Komfort: Auflösung pro Client, Bibliotheks-Scanner, Feinschliff | |

## Lizenz

GPL-3.0-or-later. Siehe [LICENSE](LICENSE).
