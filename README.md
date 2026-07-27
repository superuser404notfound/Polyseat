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

**M0 — Input-Spike.** Noch kein Produkt, keine GUI, kein Daemon. Aktuell wird
genau eine Frage beantwortet: Trägt die Container-Architektur die Eingabekette?
Siehe [`spike/m0-input/`](spike/m0-input/).

Architektur und die Begründungen dahinter: [`docs/architecture.md`](docs/architecture.md).

## Fahrplan

| | |
|---|---|
| **M0** | Input-Spike: Ein Container, ein virtuelles Pad, SDL erkennt es |
| **M1** | Ein Seat vollständig von Hand: Sway + Sunshine + NVENC + Moonlight |
| **M2** | Zwei Seats parallel — Port-, mDNS- und Ressourcenkollisionen |
| **M3** | Daemon + GUI: Seats anlegen, starten, koppeln, überwachen |
| **M4** | Geteilter Bibliotheks-Pool auf btrfs |
| **M5** | Komfort: Auflösung pro Client, Bibliotheks-Scanner, Feinschliff |

## Lizenz

GPL-3.0-or-later. Siehe [LICENSE](LICENSE).
