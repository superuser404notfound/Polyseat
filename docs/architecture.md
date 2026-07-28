# Architektur

Dieses Dokument hält fest, **was** gebaut wird und vor allem **warum so** — damit
spätere Entscheidungen nicht gegen bereits bezahlte Erkenntnisse laufen.

## Randbedingungen

Aus denen sich fast alles Weitere ergibt:

- **Alle Spielenden sitzen an Moonlight-Clients**, niemand an der Konsole des
  Hosts. Es gibt daher keine physischen Controller am Host, die zugeordnet
  werden müssten — nur virtuelle Pads, die Sunshine im jeweiligen Seat anlegt.
- **Der Host-Desktop (KDE/Wayland) läuft normal weiter** und darf durch keinen
  Seat gestört werden.
- **Feste Seats pro Person.** Kein dynamischer Pool: Anna hat ihren Seat, er hat
  immer denselben Port, sie richtet Moonlight einmal ein.
- **Seats laufen dauerhaft** (idle ≈ 400 MB). On-demand-Start ist ein späteres
  Feature, kein Designzwang.
- **SDR**, kein HDR. HDR ist auf Linux/wlroots/NVIDIA der teuerste Wunsch und
  bringt für den Start nichts.
- **N Seats**, nicht fest zwei. Realistisch begrenzt die Hardware auf 2–3
  spielende Seats (siehe Kapazität).

## Warum Container — und warum Incus

Ein Seat braucht ein eigenes `$HOME` (Steam-Single-Instance-Lock, getrennte
Konten), eigenes Audio, eigene Session. Ein eigener Unix-User pro Seat würde das
lösen. Container lösen zusätzlich das, was ein User nicht löst: ein **privates,
leeres `/dev/input`**. Isolation entsteht dann strukturell, statt über udev-Regeln
gegen einen global sichtbaren Gerätebaum.

Incus statt Podman oder systemd-nspawn, aus drei konkreten Gründen:

1. **`unix-char` mit `required=false` unterstützt Hotplug in laufende Container.**
   Genau das braucht der Input-Broker: Client verbindet sich → Pad entsteht →
   Node muss in den *laufenden* Seat. Podman kann Devices nicht zu laufenden
   Containern hinzufügen. Das allein entscheidet die Wahl.
2. **`nvidia.runtime=true`** injiziert die Treiberbibliotheken des Hosts über
   libnvidia-container. Auf einem Rolling Release ist das essenziell — sonst
   driftet nach jedem `nvidia-utils`-Update der Container-Userspace gegen das
   Host-Kernelmodul. Mit nspawn müsste man libnvidia-container nachbauen.
3. **System-Container** bringen eigenes systemd, eigene User, eigenes PipeWire
   mit. Ein Seat *ist* eine kleine Maschine, statt eine zu simulieren. Dazu
   `limits.cpu` / `limits.memory` pro Seat und ein btrfs-Storage-Pool.

VMs mit GPU-Passthrough scheiden aus: eine einzelne Consumer-GPU lässt sich
nicht sinnvoll auf mehrere VMs aufteilen.

## Was die Randbedingungen wegräumen

Weil niemand am Host sitzt und niemand physische Pads einsteckt:

- **Kein Broker für physische Geräte.** Es gibt nur virtuelle Pads.
- **Kein Audio-Passthrough.** PipeWire läuft vollständig im Container, der Ton
  verlässt ihn nur als Stream. Kein `/dev/snd` im Container, keine Konflikte um
  den Default-Sink, Host-Audio bleibt strukturell unberührt.
- **Host-Peripherie ist unerreichbar**, weil sie nie in einen Container gemappt
  wird.

Übrig bleibt am Host genau ein Problem: die virtuellen Pads der Seats dürfen
nicht auf dem KDE-Desktop auftauchen.

## Die Eingabekette — das Kernrisiko

`uinput` ist **nicht namespaced**. Ein Pad, das Sunshine in Seat 3 anlegt,
registriert der Kernel global; der Host-udev legt den Node in der Host-devtmpfs
an. Der Container hat ein minimales `/dev`, dort passiert also erstmal nichts —
das ist die gewünschte Isolation, aber es heißt auch, dass der Node aktiv
zurückgereicht werden muss.

Die Kette hat zwei Hälften:

**Hälfte 1 — Node in den richtigen Seat.**

1. **Seat-Tag im Gerätenamen.** Kein Eingriff nötig: Sunshine liest `XDG_SEAT`
   und hängt den Seat-Namen selbst an, sobald der Seat nicht `seat0` ist —
   aus "Keyboard passthrough" wird "Keyboard passthrough (seat1)".
2. **Host-udev-Regel** matcht auf das Tag, macht das Gerät für KDE/libinput
   unsichtbar und meldet es dem Broker.
3. **Broker** fährt `incus config device add <seat> padN unix-char …` und beim
   Trennen wieder `remove`.

**Hälfte 2 — Enumeration im Container.** In Incus-Containern läuft kein
funktionierendes udev. Der Node ist da, aber Steam und SDL *enumerieren*
Gamepads über libudev, nicht durch Scannen von `/dev/input`. Auswege:
`SDL_JOYSTICK_DISABLE_UDEV=1`, und/oder ein Fake-udev-Shim, der libudev-Aufrufe
abfängt. Wolf (games-on-whales) hat für genau dieses Problem eine Komponente —
das Konzept ist übernehmbar, auch ohne Wolf als Produkt zu nutzen.

**Deshalb ist das der allererste Spike.** Trägt Hälfte 2 nicht, kippt die
Container-Architektur und wir landen bei einem Unix-User pro Seat.

### Ergebnis von M0 (2026-07-27): sie trägt

Gemessen, nicht vermutet — Protokoll in [`spike/m0-input/README.md`](../spike/m0-input/README.md).

- Ein im Container erzeugtes Pad erscheint am Host, im Container dagegen
  existiert `/dev/input` gar nicht. Die Isolation entsteht also tatsächlich
  strukturell.
- `unix-char`-Hotplug bringt den Knoten in den laufenden Container.
- SDL erkennt das Pad dort als Controller.
- Eine udev-Regel auf `ATTRS{name}=="polyseat:*"` hält die Pads zuverlässig
  vom Host-Desktop fern.

**Eine Auflage:** Ein Pad, das *während* eines laufenden Prozesses eingehängt
wird, bleibt unbemerkt — libudev enumeriert über `/sys` (im Container
sichtbar), der udev-Monitor hängt aber an Netlink-Uevents (erreichen den
Container nicht). Mit `SDL_JOYSTICK_DISABLE_UDEV=1` pollt SDL `/dev/input`
direkt und bemerkt Hotplug zuverlässig. **Die Variable gehört damit in die
Umgebung jedes Seats.** Ein Fake-udev-Shim wird vorerst nicht gebraucht — ob
Steam mit seinem eigenen SDL und Steam Input genauso reagiert, ist die erste
offene Frage von M1.

Sunshine legt Gamepads übrigens nicht über uinput an, sondern über
**`/dev/uhid`** (via inputtino). Beide Geräte gehören also in jeden Seat —
ohne uhid entstehen Tastatur und Maus normal, aber nie ein Pad.

## Aufbau

```
┌─ Host: CachyOS, KDE-Desktop läuft normal weiter ─────────┐
│                                                          │
│  polyseatd  — Go, System-Service, privilegiert           │
│   ├─ HTTP/JSON + WebSocket API (Unix-Socket, optional    │
│   │    TCP mit Token-Auth für Zugriff vom Handy)         │
│   ├─ Incus-Go-Client  → Seats anlegen/starten/limitieren │
│   ├─ Input-Broker     → udev-Monitor, Pad → richtiger    │
│   │                      Seat via unix-char-Hotplug      │
│   ├─ Sunshine-Proxy   → Pairing/PIN aller Seats an einem │
│   │                      Ort, Config-Generierung         │
│   └─ Doctor           → Health-Checks, Selbstdiagnose    │
│                                                          │
│  polyseat GUI — vom Daemon ausgeliefert                  │
└──────────────────────────────────────────────────────────┘
              │ Incus-API
   ┌──────────┼──────────┬───────────┐
 seat:rooky  seat:anna  seat:gast   …
 (je: Sway headless + Sunshine + PipeWire + Steam)
```

Pro Seat: Sway headless (`WLR_BACKENDS=headless`, `LIBSEAT_BACKEND=noop`) als
Session-Shell, weil Sunshine dort über `wlr-screencopy`/`export-dmabuf` capturen
kann — KMS-Capture ist auf NVIDIA proprietär tot. Optional gamescope nested pro
Spiel für Skalierung und FPS-Cap.

## GUI statt CLI

Das Herz ist ein **Daemon mit API**; die GUI ist ein Client davon. Web-UI, nicht
nativ: Seats sollen vom Sofa oder vom Handy aus einrichtbar sein, Go ist stark
bei HTTP und schwach bei nativen GUIs, und Sunshine selbst arbeitet genauso.
Ein späteres Einpacken in ein natives Fenster (Wails) bleibt möglich, ohne die
Codebasis zu spalten.

Das wichtigste UX-Ziel: **eine Oberfläche für alle Seats.** Ohne sie jongliert
man N Sunshine-Web-UIs auf N Ports mit N Pairing-Dialogen.

Ein dünner CLI-Client (`status`, `doctor`) bleibt erhalten — als reiner
API-Client für den Moment, in dem die GUI nicht startet. Diagnose, kein
Bedienkonzept.

## Prinzip: Der Daemon besitzt die Konfiguration

Incus-Profile, Sunshine-Configs, udev-Regeln und systemd-Units sind **generierte
Artefakte, niemals Eingaben**. Wer sie von Hand editiert, verliert die Änderung
beim nächsten Schreiben — dafür ist der Zustand jederzeit erklärbar und
reproduzierbar. Ohne diese Regel wird eine GUI-zentrierte Verwaltung
unweigerlich inkonsistent.

## Bibliotheks-Pool

Root ist btrfs. Statt OverlayFS: `/srv/steam-pool` als Subvolume, **pro Seat ein
beschreibbarer Snapshot**. Copy-on-Write heißt, fünf Seats kosten einmal
Speicher, und Steam sieht eine voll beschreibbare Bibliothek. `compatdata/` und
`shadercache/` zeigen per Symlink auf Seat-privaten Speicher — sonst landen sie
im Snapshot und fressen die Deduplizierung auf. Pool zentral pflegen, Seats
periodisch neu snapshotten.

Die Lizenzrealität bleibt: dasselbe Spiel gleichzeitig braucht zwei Kopien im
jeweiligen Konto. Das löst keine Software.

## Kapazität

Referenzmaschine: RTX 4080 (16 GB), 24 Kerne, **31 GB RAM**, btrfs.

RAM ist der Flaschenhals, nicht die GPU. Ein AAA-Seat will 8–16 GB — fünf
gleichzeitig spielende Seats gehen damit nicht, realistisch sind 2–3 plus einige
leichte. NVENC und CPU reichen locker; VRAM wird bei drei modernen Titeln knapp.
Die Software wird N-fähig gebaut, die Hardware deckelt.

## Verworfene Alternativen

- **Wolf (games-on-whales) übernehmen.** Löst dasselbe Problem container-first
  und spricht Moonlight direkt. Bewusst nicht genommen: eigener Stack gewollt,
  Host-Desktop soll parallel weiterlaufen. Als Ideengeber (`inputtino`,
  fake-udev) weiterhin relevant.
- **Ein Unix-User pro Seat ohne Container.** Bleibt der Rückfallplan, falls die
  Eingabekette in M0 scheitert.
- **VM pro Seat.** Eine GPU, nicht teilbar.
- **Ein User, mehrere Compositor-Instanzen.** Löst weder Steam-Lock noch Input.
