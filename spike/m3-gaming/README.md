# M3 — ein Seat, der wirklich spielt

**Ziel:** Steam, ein echtes Spiel, Ton und Gamepad im Seat.

**Status: erreicht** (2026-07-28). Steam läuft im Seat, ein Spiel startet, Ton
kommt beim Client an, der Controller wird erkannt.

Dieser Schritt hat kein eigenes Skript — die Änderungen sind in
[`../m1-seat/`](../m1-seat/) und [`../m2-input-broker/`](../m2-input-broker/)
eingeflossen. Hier steht, was dabei gelernt wurde.

## Steam installieren: drei Fallen

**1. Steam zieht sonst einen zehn Jahre alten Treiber herein.** Es hängt an den
Virtualpaketen `vulkan-driver` und `lib32-vulkan-driver`, und pacman nimmt den
erstbesten Anbieter — im CachyOS-Repo ist das `lib32-nvidia-390xx-utils`. Das
Paket würde genau die injizierten Treiberdateien überschreiben.

Richtig ist, diese Abhängigkeiten als erfüllt zu **erklären**, denn in einem
Seat kommt der Treiber grundsätzlich vom Host:

```
pacman -S --assume-installed vulkan-driver \
          --assume-installed lib32-vulkan-driver \
          --assume-installed opengl-driver \
          steam lib32-libglvnd lib32-vulkan-icd-loader
```

**2. `lib32-libglvnd` verlangt hart `lib32-mesa`**, und dessen
`/usr/lib32/libGLX_indirect.so.0` kollidiert mit einem Symlink der
NVIDIA-Injektion. Das führt zur nächsten Falle.

**3. `nvidia.runtime` räumt seine Symlinks nicht wieder weg.** Beim ersten
Start legt libnvidia-container echte Symlinks im Container-Dateisystem an. Ein
späteres `nvidia.runtime=false` entfernt sie **nicht** — nur die
bind-gemounteten Bibliotheken verschwinden.

Damit gilt der M1-Merksatz „erst Pakete, dann NVIDIA einschalten" **nur für
einen frischen Container**. Bei einem bereits gestarteten Seat bleiben die
Symlinks als Dateikonflikte liegen und müssen einzeln weggeräumt werden.
Konsequenz für den Daemon: **Steam gehört in die Grundinstallation des
Seat-Images**, nicht in eine Nachinstallation.

## Steam braucht `DISPLAY`

Steam ist eine X11-Anwendung. sway startet Xwayland erst beim ersten X-Client,
und sways eigene Umgebung enthält bis dahin **kein** `DISPLAY`. Ohne die
Variable erscheint nur ein Fenster „Unable to open a connection to X" — kein
Fehler im Log, nur ein Dialog, den man im Stream sieht.

Mit `DISPLAY=:0` startet Xwayland sauber nach. Die Variable gehört damit in die
Session-Umgebung des Seats.

## Ton: der Sink gehört Sunshine

Der Null-Sink aus M1 war ein Fehler, mitgeschleppt aus einem Aufbau ohne
Container. **Sunshine legt beim Streamen seinen eigenen Sink an**
(`sink-sunshine-stereo`, dazu Surround-Varianten) und setzt ihn als Standard,
damit Anwendungen dorthin spielen. Wer in der Konfiguration `audio_sink` auf
einen anderen Sink setzt, lässt Sunshine den falschen abgreifen: das Spiel
spielt in Sunshines Sink, übertragen wird **Stille**.

Im Log ist das eindeutig zu sehen und trotzdem leicht zu übersehen, weil alles
gesund aussieht — Opus initialisiert, ein Sink-Input läuft, nur eben in einen
anderen Sink als den abgegriffenen.

Im Container gibt es keine echte Soundkarte. Der Grund, aus dem man auf einem
Host den Standard-Sink schützt, existiert hier also gar nicht. `audio_sink`
bleibt leer, Sunshine regelt es selbst.

## Gamepad: `/dev/uhid` ist nicht optional

Sunshine benutzt **inputtino** und legt Gamepads als **HID-Geräte über
`/dev/uhid`** an, nicht über uinput. Wird uhid nicht durchgereicht, meldet
Sunshine zwar „Gamepad 0 will be Xbox One controller", aber im Seat entsteht
nie ein Gerät. Tastatur, Maus, Touch und Pen funktionieren dabei völlig normal
— der Fehler sieht deshalb nach einem Gamepad-Problem des Clients aus.

Beide Geräte gehören in jeden Seat:

```
incus config device add SEAT uinput unix-char source=/dev/uinput mode=0666
incus config device add SEAT uhid   unix-char source=/dev/uhid   mode=0666
```

## Der Broker musste zweimal nachgebessert werden

- **Das Namensmuster war zu eng.** `passthrough` trifft Tastatur und Maus, ein
  Gamepad heißt aber nach dem emulierten Modell. Das Muster ist jetzt ein
  regulärer Ausdruck.
- **Und dadurch zu gefährlich:** Ein Muster mit „Controller" hätte auch den
  ASRock-LED-Controller des Hosts erfasst. Der Broker fasst deshalb nur noch
  Geräte unterhalb von `/devices/virtual/` an — nur was per uinput oder uhid
  entstanden ist, darf überhaupt in einen Seat.

## Ein Betriebsproblem, das den Daemon betrifft

Beim Container-Neustart hat sich Incus verhakt: Der Container war tot — keine
Prozesse, keine cgroup — aber Incus meldete weiter `RUNNING` und hing an einem
nicht abbrechbaren „Stopping instance". Erst ein Neustart des Incus-Daemons
löste es.

Der Verdacht liegt beim Broker-Prototyp, der im halben Sekundentakt
`incus exec` aufruft und in den Stopp hineingelaufen sein dürfte. **Der Daemon
darf nicht blind pollen**, sondern muss den Lebenszyklus des Containers kennen
und während eines Stopps stillhalten.

## Offen

- **Proton im Detail** — getestet wurde ein startendes Spiel, nicht Shader-
  Kompilierung, Controller-Rumble oder Steam Overlay.
- **Steam Input** benutzt neben SDL auch hidraw. Der Broker reicht bisher nur
  `event*`-Knoten durch, keine `hidraw*`. Bisher hat nichts gefehlt, aber die
  Lücke ist bekannt.
