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

## Protokoll

Ergebnisse hier eintragen, damit spätere Entscheidungen darauf aufbauen können.

| Hypothese | Ergebnis | Notiz |
|---|---|---|
| H1 | | GPU im Container |
| H2 | | uinput nicht namespaced |
| H3 | | `/dev/input` im Container leer |
| H4 | teilweise ✓ | Name frei setzbar, `ID_INPUT_JOYSTICK` gesetzt — KDE-Test offen |
| H5 | | unix-char-Hotplug |
| H6 | | **das Risiko** |
