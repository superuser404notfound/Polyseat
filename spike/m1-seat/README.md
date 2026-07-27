# M1 — ein vollständiger Seat

**Ziel:** Ein Incus-Container, in dem headless Sway, Sunshine und NVENC laufen,
mit eigener LAN-Adresse, sodass sich Moonlight verbindet.

**Status: erreicht** (2026-07-27). Sunshine meldet `h264_nvenc [nvenc]`,
Ausgang 1920x1080, ein Audio-Sink, eigene LAN-Adresse.

## Ablauf

```
./10-create-seat.sh        # Container, macvlan, CachyOS-Repo, Pakete, Benutzer
./15-nvidia-userspace.sh   # was nvidia.runtime NICHT mitliefert
./20-session.sh            # PipeWire, Sway, Sunshine als User-Units
./30-verify.sh             # Endkontrolle + Verbindungsdaten
./99-cleanup.sh
```

## Netzwerk: zwei Karten, mit Absicht

Jeder Seat bekommt **zwei** Netzwerkkarten:

- `eth1` — **macvlan** direkt am physischen Uplink. Der Seat holt sich per DHCP
  eine eigene LAN-Adresse und ist für Moonlight ein eigenständiger Host.
- `eth0` — an `incusbr0`, der Verwaltungsweg.

Der Grund für eth1 ist architektonisch bedeutsam: **mit eigener LAN-Adresse pro
Seat entfällt jede Port-Jonglage.** Jeder Seat lauscht auf den
Standard-Sunshine-Ports, niemand muss Portversätze pflegen, und Moonlight wird
auf dem Client ganz normal eingerichtet.

Der Grund für eth0 ist die bekannte Eigenschaft von macvlan: **Host und
Container können sich über dieses Interface nicht direkt erreichen.** Die
Sunshine-Weboberfläche ist vom Host-Browser deshalb nur über die
Verwaltungsadresse erreichbar — und genau diesen Weg wird später auch der
Daemon für seinen Pairing-Proxy nutzen.

## Der teuerste Befund: was `nvidia.runtime` nicht mitbringt

`nvidia.runtime=true` spiegelt die Treiber**bibliotheken** in den Container —
und sonst nichts. Es fehlen vier Dinge, und ohne sie landet EGL bei Mesa, CUDA
kann den GL-Kontext nicht übernehmen, und Sunshine fällt still auf
`libx264 [software]` zurück. Ein Seat sähe dann funktionsfähig aus und wäre
unbenutzbar.

| fehlt | woher es normalerweise kommt | Symptom |
|---|---|---|
| `/usr/share/glvnd/egl_vendor.d/10_nvidia.json` | `nvidia-utils` | EGL probiert nur Mesa: „failed to create dri2 screen" |
| `/usr/lib/gbm/nvidia-drm_gbm.so` | `nvidia-utils` (nur ein Symlink!) | GBM findet den NVIDIA-Backend nicht |
| `egl-gbm`, `egl-wayland` | eigene Arch-Pakete, **kein Treiberbestandteil** | „Couldn't initialize EGL display: [00003001]" |
| Vulkan-ICD `nvidia_icd.json` | `nvidia-utils` | Vulkan sieht keine GPU |

`nvidia-utils` im Container zu installieren wäre die falsche Antwort — es würde
eigene `.so`-Dateien gegen die injizierten setzen. Die Manifeste werden deshalb
erzeugt, die beiden echten Pakete regulär installiert.

**Reihenfolge:** erst die Pakete, dann die Manifeste. Wer die JSONs vorher von
Hand hineinkopiert, blockiert `pacman` mit einem Dateikonflikt — genau das ist
hier passiert.

## Weitere Befunde

- **`incus launch` kehrt zurück, bevor systemd im Container bereit ist.** Das
  erste `systemctl` scheitert dann mit „Failed to connect to system scope bus".
  `10-create-seat.sh` wartet deshalb auf `systemctl is-system-running`.
- **`/dev/dri/card1` kommt als `root:root 0660` an** — der Spieler kann es nicht
  öffnen, EGL scheitert mit „Permission denied". `incus config device set gpu
  mode=0666` löst es.
- **Der libinput-Backend bricht ohne Eingabegeräte ab.** `/dev/input` ist im
  Container beim Start leer, also braucht sway `WLR_LIBINPUT_NO_DEVICES=1`.
- **Sunshine muss nach sway starten**, nicht gleichzeitig. Sonst findet es keine
  Capture-Plattform („Platform failed to initialize") und **alle** Encoder
  scheitern — auch der Software-Encoder, was die Ursache gut verschleiert.
  `WAYLAND_DISPLAY` kommt per `systemctl --user import-environment` aus der
  sway-Config.
- **Den Null-Sink deklarativ anlegen, nicht per `exec pactl`.** PipeWire
  überlebt einen sway-Neustart, das `exec` nicht — nach vier Neustarts gab es
  vier identische Sinks.
- **Sunshine liegt im CachyOS-Repo, nicht in Arch.** Der Container bindet nur
  das nicht CPU-optimierte `[cachyos]`-Repo ein, und zwar ans *Ende* der
  `pacman.conf`, damit Arch bei allen gemeinsamen Paketen gewinnt.

## Offen — der nächste Schritt

**Eingabe ist noch nicht geprüft.** Aus M0 ist bekannt, dass SDL mit
`SDL_JOYSTICK_DISABLE_UDEV=1` Hotplug bemerkt. Für **sway** gilt das nicht:
dessen libinput-Backend hängt an udev-Uevents, die den Container nicht
erreichen. Tastatur und Maus, die Sunshine beim Verbinden eines Clients als
virtuelle Geräte anlegt, werden vom Compositor daher voraussichtlich **nicht**
bemerkt.

Damit wird der Fake-udev-Shim, den M0 noch überflüssig gemacht hatte, für
Tastatur und Maus vermutlich doch gebraucht. Das ist die erste Frage von M2.
