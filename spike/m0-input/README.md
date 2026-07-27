# M0 — Input-Spike

**Eine Frage:** Trägt die Container-Architektur die Eingabekette?

Wenn nein, kippt der Entwurf und wir landen bei einem Unix-User pro Seat statt
bei Containern. Das will man nach einem Abend wissen, nicht nach sechs Wochen.
Deshalb steht dieser Spike vor allem anderen — vor Sway, vor Sunshine, vor Go,
vor der GUI.

Der Spike benutzt **kein Sunshine**. Ein synthetisches Pad (`padgen.py`, rohes
uinput, ~120 Zeilen) isoliert die Variable, die uns interessiert. Sunshine bringt
Capture, Encoder, Netzwerk und Pairing mit — alles Dinge, die hier nur Rauschen
wären.

## Hypothesen

| | Hypothese | Erwartung |
|---|---|---|
| **H1** | Ein Incus-Container mit `nvidia.runtime=true` sieht die GPU | `nvidia-smi` läuft im Container |
| **H2** | Ein *im Container* erzeugtes uinput-Pad erscheint auf dem **Host** unter `/dev/input/` | ja — uinput ist nicht namespaced |
| **H3** | Dasselbe Pad erscheint **nicht** im `/dev/input` des Containers | ja — das ist die strukturelle Isolation |
| **H4** | Der Gerätename ist beim Anlegen frei wählbar, eine Host-udev-Regel kann darauf matchen | ja — trägt die Seat-Zuordnung |
| **H5** | `incus config device add … unix-char` bringt den Node in den **laufenden** Container | ja — laut Incus-Doku hotplug-fähig |
| **H6** | **SDL im Container erkennt das Pad** — obwohl dort kein udev läuft | **unbekannt. Das ist das Risiko.** |

## Bestehenskriterium

**H6 grün**, notfalls mit `SDL_JOYSTICK_DISABLE_UDEV=1`. Alles andere ist
Vorbereitung.

Drei mögliche Ausgänge:

- **H6 ohne Kniff grün** → Architektur trägt, weiter mit M1.
- **H6 nur mit `SDL_JOYSTICK_DISABLE_UDEV=1` grün** → trägt, aber jeder Seat
  braucht die Variable in der Umgebung, und Steam Input muss separat geprüft
  werden (Steam benutzt nicht nur SDL).
- **H6 rot** → Fake-udev-Shim nötig (Konzept von Wolf übernehmbar) oder Rückfall
  auf einen Unix-User pro Seat.

## Voraussetzungen

`00-prereqs.sh` prüft alles read-only und druckt die fehlenden Root-Befehle aus.
Kurzfassung, einmalig als root:

```
sudo pacman -S --needed incus nvidia-container-toolkit go
sudo systemctl enable --now incus.socket
sudo usermod -aG incus-admin $USER      # danach neu einloggen
sudo incus admin init --minimal
```

`--minimal` legt einen dir-basierten Storage-Pool an. Für M0 reicht das; für
den Bibliotheks-Pool (M4) wollen wir später btrfs.

## Ablauf

```
./00-prereqs.sh                 # read-only, druckt fehlende Root-Befehle
./10-create-seat.sh             # Container 'm0' + /dev/uinput + Testwerkzeug
./20-run-pad.sh                 # padgen.py im Container starten (blockiert)
./30-observe-host.sh            # in zweiter Shell: H2/H3/H4 prüfen
./40-inject.sh <eventN>         # H5: Node in den laufenden Container
./50-verify.sh                  # H6: evtest + SDL im Container
./60-hide-from-host.sh          # H4: udev-Regel, versteckt Pad vor KDE
./99-cleanup.sh
```

## Vorab am Host verifiziert

`padgen.py` wurde ohne Container direkt am Host getestet (rooky ist in der
Gruppe `input`, `/dev/uinput` ist damit beschreibbar):

- Gerät wird angelegt, Name ist frei setzbar → **der Seat-Tag trägt.**
- Meldet sich als `045e:028e`, evtest liest das erwartete Xbox-360-Layout.
- Host-udev klassifiziert es als `ID_INPUT=1`, `ID_INPUT_JOYSTICK=1` — genau
  die Eigenschaften, die die Regel in `60-hide-from-host.sh` strippt.
- Nach `UI_DEV_DESTROY` bleibt nichts zurück.

**Befund für den späteren Broker:** `UI_GET_SYSNAME` liefert `inputNN`, der
Geräteknoten heißt aber `eventM` — die Zahlen stimmen **nicht** überein
(beobachtet: `input37` → `/dev/input/event7`). Die Zuordnung führt über
`/sys/class/input/inputNN/eventM/`. Wenn der Broker die Pads selbst anlegt,
ist das der zuverlässige Korrelationsweg — dann braucht es das Namens-Tag
nur noch für die udev-Regel, nicht mehr für die Zuordnung.

## Protokoll — durchgeführt 2026-07-27

**Ergebnis: die Container-Architektur trägt.** Alle Hypothesen grün, mit einer
Auflage (siehe H7).

| Hypothese | Ergebnis | Notiz |
|---|---|---|
| H1 | ✅ | `nvidia-smi` im Container meldet die RTX 4080 |
| H2 | ✅ | im Container erzeugtes Pad erscheint am Host als `/dev/input/event24` |
| H3 | ✅ | `/dev/input` existiert im Container **gar nicht** — Isolation per Default |
| H4 | ✅ | udev-Regel strippt `ID_INPUT`, `libinput list-devices` findet nichts mehr |
| H5 | ✅ | `unix-char`-Hotplug bringt den Knoten in den laufenden Container |
| H6 | ✅ | SDL erkennt das Pad als „Xbox 360 Controller" — **auch ohne Kniff** |
| H7 | ⚠️ | Hotplug zur Laufzeit **nur** mit `SDL_JOYSTICK_DISABLE_UDEV=1` |

### H7 — die Auflage

H6 beantwortet nur, ob SDL beim Start findet, was schon da ist. Das reicht
nicht: Steam läuft im Seat dauerhaft, und Pads entstehen erst, wenn sich
jemand verbindet.

Gemessen mit `55-hotplug.sh`: ein zweites Pad, das während der Beobachtung
eingehängt wird, bleibt **unbemerkt**. Der Knoten kommt an — ein anschließend
neu gestartetes `sdlprobe` sieht beide Pads — aber die *Benachrichtigung*
fehlt. Grund: libudev enumeriert über `/sys`, das im Container sichtbar ist,
der udev-*Monitor* hängt dagegen an Netlink-Uevents, und die erreichen den
Container nicht.

Mit `SDL_JOYSTICK_DISABLE_UDEV=1` pollt SDL stattdessen `/dev/input` direkt
und meldet das nachträglich eingehängte Pad zuverlässig.

**Folge für die Architektur:** Die Variable gehört in die Umgebung jedes Seats.
Ein Fake-udev-Shim wird dadurch vorerst nicht gebraucht.

**Offen:** Steam bringt sein eigenes SDL mit und benutzt udev auch für Steam
Input. Ob die Variable dort genauso greift, muss in M1 mit echtem Steam
geprüft werden — `sdlprobe` beantwortet das nicht.

### Weitere Befunde aus dem Durchlauf

- **`nvidia.runtime=true` spiegelt nur Bibliotheken**, keine Geräteknoten.
  Ohne zusätzliches `gpu`-Device läuft `nvidia-smi` und meldet „No devices
  found". Nebenbei: `nvidia-smi -L` liefert auch dann Exit 0 — Prüfungen
  müssen die Ausgabe ansehen, nicht den Rückgabewert.
- **Reihenfolge ist zwingend:** Pakete installieren, *dann* `nvidia.runtime`
  einschalten. Sonst kollidiert pacman mit den injizierten Treiberdateien
  (`mesa: /usr/lib/libGLX_indirect.so.0 exists in filesystem`). `--overwrite`
  wäre die falsche Antwort — es ersetzt die Treiberdatei.
- **Incus braucht eigene idmap-Bereiche.** CachyOS liefert nur einen Eintrag
  für den Benutzer; ohne `root:1000000:1000000000` in `/etc/subuid` und
  `/etc/subgid` scheitert jeder Containerstart mit „System doesn't have a
  functional idmap setup".
- **SDL benennt bekannte Geräte um.** Das Pad heißt im Container „Xbox 360
  Controller", nicht wie im evdev-Namen. Der Seat-Tag ist also nichts, worauf
  sich etwas *oberhalb* von evdev verlassen darf — für udev-Regeln taugt er,
  für Anwendungslogik nicht.
- `UI_GET_SYSNAME` liefert `inputNN`, der Knoten heißt `eventM`, die Zahlen
  stimmen nicht überein (`input40` → `event24`). Zuordnung über
  `/sys/class/input/inputNN/eventM/`.
