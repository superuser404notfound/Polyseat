# M2 — der Input-Broker

**Ziel:** Tastatur, Maus und Pad aus dem Moonlight-Client erreichen die
Seat-Session.

**Status: gelöst und am echten Gerät bestätigt** (2026-07-28). sway im Seat
listet Sunshines Geräte als Zeiger und Tastaturen, sowohl beim Start als auch
bei Hotplug zur Laufzeit. Vom iPhone aus lässt sich der Mauszeiger bewegen und
im Terminal der Session tippen.

## Das Problem

`uinput` ist nicht namespaced. Sunshine legt seine virtuellen Geräte im Seat
an, der Kernel registriert sie global, der Host-udev legt die Knoten an — und
ausgerechnet im Seat sind sie unsichtbar. Sway hatte null Eingabegeräte.

## Die Lösung: drei Schritte, alle einzeln gemessen

Für jedes Gerät:

1. **Knoten einhängen** — `incus config device add … unix-char`,
   **zwingend mit `mode=0666`**. Ohne das kommt er als `root:root 0660` an
   und sway scheitert mit `Failed to open device: Permission denied`.
2. **udev-Datenbankeintrag schreiben** — nach `/run/udev/data/cMAJ:MIN` im
   Container. libudev liest Eigenschaften nicht aus `/sys`, sondern von dort.
   Ohne `ID_INPUT=1` ignoriert libinput das Gerät vollständig.
3. **Synthetisches uevent senden** — `fakeudev.py`, sonst bemerkt sway das
   Gerät erst beim nächsten Neustart.

Schritt 1 und 2 genügen für Geräte, die beim Sway-Start schon da sind.
Schritt 3 macht Hotplug möglich — und das ist der Normalfall, weil Sunshine
seine Geräte erst beim Verbinden eines Clients anlegt.

## Warum kein vollwertiges Fake-udev nötig ist

Die Vermutung aus M0 und M1 war, dass ein Shim gebraucht wird, der libudev
komplett nachbildet. Das ist nicht so, weil die Enumeration bereits
funktioniert: **libudev läuft dafür `/sys` ab, und `/sys` sieht der Container
vollständig** (`udevadm trigger --dry-run` im Container listet die Geräte des
Hosts). Nur die *Benachrichtigung* fehlt, denn der Uevent-Netlink hängt am
Netzwerk-Namespace.

`fakeudev.py` legt deshalb einfach selbst eine Nachricht auf die
udev-Multicast-Gruppe **innerhalb** des Containers. udevd wird dabei umgangen,
die libudev-Clients (libinput, SDL) hören direkt darauf. Das sind rund 40
Zeilen statt eines nachgebauten udev.

Zwei Fallstricke dabei:

- Die Nachricht braucht den korrekten **MurmurHash2 des Subsystems** im Kopf,
  sonst filtern die Clients sie weg.
- Der Absender muss **im Container als root** laufen. libudev verwirft
  Nachrichten, deren Absender nicht uid 0 ist, und die Kennung wird beim
  Übersetzen in den User-Namespace geprüft. Ein Host-Prozess, der nur per
  `setns` in den Netzwerk-Namespace wechselt, fällt durch diese Prüfung.

## Ablauf

```
./10-uevent-test.sh        # belegt: kein uevent erreicht den Container
./11-udevdb-test.sh        # belegt: der Datenbankeintrag allein genügt nicht
./12-sway-enumeration.sh   # belegt: Enumeration funktioniert, Rechte fehlten
./20-broker.sh             # Broker starten — muss während des Streams laufen
```

## Befunde

- **libinput ignoriert Gamepads grundsätzlich.** Ein Pad taucht in
  `swaymsg -t get_inputs` niemals auf — das ist richtig so, Spiele lesen es
  direkt über evdev. Wer die Eingabekette gegen sway prüft, muss Tastatur
  oder Maus nehmen. Ein halber Abend ging dafür drauf, ein Gamepad als Probe
  zu benutzen.
- **Die Klassifikation muss der Broker selbst machen.** Sunshines Geräte
  bekommen vom Host-udev gar keine `ID_INPUT`-Eigenschaften, und polyseats
  eigene werden von der Ausblendregel absichtlich gestrippt. Der Broker liest
  deshalb die Fähigkeits-Bitmaps aus `/sys` und bildet nach, was `input_id`
  tut.
- **Reihenfolge in der Klassifikation ist entscheidend.** „Mouse passthrough
  (absolute)" meldet `EV_ABS` statt `EV_REL`. Wer nur auf `EV_REL` prüft, hält
  es für eine Tastatur — sway ordnete es dann auch als solche ein, und der
  absolute Zeiger blieb tot.
- **Die Ausblendregel am Host muss Sunshines Namen erfassen.** Bis M2 deckte
  sie nur `polyseat:*` ab. Dass die „passthrough"-Geräte trotzdem nicht auf
  dem KDE-Desktop auftauchten, lag an fehlenden `ID_INPUT`-Eigenschaften aus
  ungeklärter Ursache — darauf darf man sich nicht verlassen.

## Offen

- **Zuordnung Gerät → Seat.** Sunshines Geräte heißen in jedem Seat identisch.
  Bei einem Seat egal, bei mehreren der Kern des Problems. Es braucht einen
  Seat-Tag im Gerätenamen: ein kleiner Sunshine-Patch (Konfigurationsoption
  für einen Namenspräfix, upstream-fähig) oder ein LD_PRELOAD-Shim um
  `UI_DEV_SETUP`. **Das ist die erste Aufgabe von M3.**
- **Verwaiste Geräte.** Abgestürzte Sunshine-Instanzen hinterlassen ihre
  Geräte; beobachtet wurden zwei vollständige Sätze nebeneinander. Der Broker
  räumt bisher nur auf, was er selbst eingehängt hat.
- **Der Prototyp pollt** `/sys` im halben Sekundentakt. Der Daemon sollte am
  udev-Monitor des Hosts hängen.
- **Steam Input ungeprüft.** Steam bringt sein eigenes SDL mit und benutzt
  udev auch selbst.
