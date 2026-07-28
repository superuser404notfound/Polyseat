#!/usr/bin/env python3
"""broker.py — Prototyp des polyseat-Input-Brokers.

Sunshine legt seine virtuellen Eingabegeräte im Seat an, aber `uinput` ist
nicht namespaced: der Kernel registriert sie global, der Host-udev legt die
Knoten an, und ausgerechnet im Seat sind sie unsichtbar. Der Broker schließt
diese Lücke. Für jedes neue Gerät drei Schritte, alle in M2 einzeln gemessen:

  1. **Knoten einhängen** — `incus config device add … unix-char`, zwingend
     mit `mode=0666`. Ohne das kommt der Knoten als `root:root 0660` an und
     sway scheitert mit "Failed to open device: Permission denied".
  2. **udev-Datenbankeintrag schreiben** — libudev liest Eigenschaften nicht
     aus `/sys`, sondern aus `/run/udev/data/`. Ohne `ID_INPUT=1` ignoriert
     libinput das Gerät. Der Eintrag wird *erzeugt*, nicht vom Host kopiert:
     die Ausblendregel des Hosts strippt dort genau diese Eigenschaft.
  3. **Synthetisches uevent senden** — sonst bemerkt sway das Gerät erst beim
     nächsten Neustart. Siehe `fakeudev.py`.

Die Klassifikation (Tastatur/Maus/Pad) leitet der Broker aus den
Fähigkeits-Bitmaps in `/sys` ab, nicht aus den udev-Eigenschaften des Hosts —
denn die sind für Sunshines Geräte gar nicht gesetzt und für polyseat-Geräte
absichtlich gestrippt.

**Zuordnung Gerät → Seat:** über den Seat-Tag im Gerätenamen. Sunshine liest
`XDG_SEAT` und hängt den Seat-Namen an, sobald der Seat nicht "seat0" ist —
aus "Keyboard passthrough" wird "Keyboard passthrough (seat1)". Ein Patch oder
LD_PRELOAD-Shim, wie zunächst geplant, ist dafür nicht nötig; die Funktion
gibt es bereits.

Der Broker verlangt den Tag standardmäßig. `--tag ""` schaltet ihn ab und
fällt auf reine Namensmuster zurück — das ist nur für einen einzelnen Seat
vertretbar, bei mehreren wäre die Zuordnung Raten.

    ./broker.py --seat seat1
"""

import argparse
import json
import os
import re
import subprocess
import sys
import time

SYS_INPUT = "/sys/class/input"

# Fähigkeits-Bits, die für die Einordnung reichen.
EV_KEY, EV_REL, EV_ABS = 0x01, 0x02, 0x03
ABS_X, ABS_Y = 0x00, 0x01
BTN_LEFT, BTN_GAMEPAD = 0x110, 0x130
BTN_TOOL_PEN, BTN_TOUCH, BTN_TOOL_FINGER = 0x140, 0x14A, 0x145
BTN_STYLUS = 0x14B
KEY_Q = 16          # liegt im Block der Buchstabentasten


def read(path):
    try:
        with open(path) as fh:
            return fh.read().strip()
    except OSError:
        return ""


def bitmap(path):
    """Fähigkeits-Bitmaps stehen als Folge von Hex-Wörtern, höchstes zuerst."""
    words = read(path).split()
    value = 0
    for i, word in enumerate(reversed(words)):
        value |= int(word or "0", 16) << (i * 64)
    return value


def classify(sysdev):
    """Bildet nach, was udevs input_id tut.

    Die Reihenfolge ist nicht beliebig: Sunshines "Mouse passthrough
    (absolute)" meldet EV_ABS statt EV_REL. Wer nur auf EV_REL prüft, hält
    es für eine Tastatur — sway ordnete es dann tatsächlich als solche ein
    und der Zeiger blieb tot.
    """
    ev = bitmap(f"{sysdev}/capabilities/ev")
    key = bitmap(f"{sysdev}/capabilities/key")
    abs_ = bitmap(f"{sysdev}/capabilities/abs")
    props = ["ID_INPUT=1"]

    has = lambda bits, bit: bool(bits & (1 << bit))
    is_pointer = False

    if has(ev, EV_REL) and has(key, BTN_LEFT):
        props.append("ID_INPUT_MOUSE=1")
        is_pointer = True

    if has(ev, EV_ABS) and has(abs_, ABS_X) and has(abs_, ABS_Y):
        if has(key, BTN_STYLUS) or has(key, BTN_TOOL_PEN):
            props.append("ID_INPUT_TABLET=1")
            is_pointer = True
        elif has(key, BTN_GAMEPAD):
            props.append("ID_INPUT_JOYSTICK=1")
        elif has(key, BTN_LEFT):
            props.append("ID_INPUT_MOUSE=1")     # absoluter Zeiger
            is_pointer = True
        elif has(key, BTN_TOOL_FINGER):
            props.append("ID_INPUT_TOUCHPAD=1")
            is_pointer = True
        elif has(key, BTN_TOUCH):
            props.append("ID_INPUT_TOUCHSCREEN=1")
            is_pointer = True
    elif has(ev, EV_ABS) and has(key, BTN_GAMEPAD):
        props.append("ID_INPUT_JOYSTICK=1")

    if has(ev, EV_KEY) and has(key, KEY_Q):
        props += ["ID_INPUT_KEY=1", "ID_INPUT_KEYBOARD=1"]
    elif has(ev, EV_KEY) and not is_pointer and len(props) == 1:
        props.append("ID_INPUT_KEY=1")

    # Doppelte vermeiden, Reihenfolge erhalten.
    return list(dict.fromkeys(props))


def scan(pattern, tag=None):
    """Alle virtuellen Eventgeräte, deren Name auf das Muster passt.

    Der Filter auf `/devices/virtual/` ist die eigentliche Absicherung: nur
    was per uinput oder uhid erzeugt wurde, darf überhaupt in einen Seat.
    Ein reiner Namensfilter wäre gefährlich — "Controller" träfe auch den
    ASRock-LED-Controller des Hosts, und der hat in keinem Seat etwas zu
    suchen.
    """
    rx = re.compile(pattern, re.IGNORECASE)
    needle = f"({tag})" if tag else None
    found = {}
    for entry in os.listdir(SYS_INPUT):
        if not entry.startswith("event"):
            continue
        sysdev = f"{SYS_INPUT}/{entry}"
        if "/devices/virtual/" not in os.path.realpath(sysdev):
            continue
        name = read(f"{sysdev}/device/name")
        if not name or not rx.search(name):
            continue
        # Der Seat-Tag entscheidet, wem das Gerät gehört. Ohne ihn würde ein
        # zweiter Seat die Geräte des ersten mit einsammeln.
        if needle and needle not in name:
            continue
        dev = read(f"{sysdev}/dev")
        if not dev:
            continue
        major, minor = dev.split(":")
        found[entry] = {
            "node": entry,
            "name": name,
            "major": int(major),
            "minor": int(minor),
            "syspath": os.path.realpath(sysdev).removeprefix("/sys"),
            "props": classify(os.path.realpath(f"{sysdev}/device")),
        }
    return found


def state_path(seat):
    """Der Broker muss über einen eigenen Neustart hinweg wissen, was er
    eingehängt hat — sonst kann er beim Aufräumen kein `remove`-Ereignis
    senden, weil das Gerät am Host längst weg ist und DEVPATH und Gerätenummer
    nicht mehr auszulesen sind. Ohne dieses Ereignis behält sway tote
    Eingabegeräte in seiner Liste."""
    base = os.environ.get("XDG_RUNTIME_DIR") or "/tmp"
    return f"{base}/polyseat-broker-{seat}.json"


def load_state(seat):
    try:
        with open(state_path(seat)) as fh:
            return json.load(fh)
    except (OSError, ValueError):
        return {}


def save_state(seat, devices):
    try:
        with open(state_path(seat), "w") as fh:
            json.dump(devices, fh)
    except OSError:
        pass


def incus(*args, check=True):
    return subprocess.run(["incus", *args], capture_output=True, text=True,
                          check=check)


def attach(seat, dev):
    node, minor = dev["node"], dev["minor"]
    print(f"  + {node:<10} {dev['name']}")
    print(f"    {' '.join(dev['props'])}")

    # 1) Knoten einhängen. mode=0666 ist nicht optional.
    incus("config", "device", "add", seat, f"in-{node}", "unix-char",
          f"source=/dev/input/{node}", f"path=/dev/input/{node}",
          "mode=0666", "required=false", check=False)

    # 2) Datenbankeintrag erzeugen.
    entry = "I:1\n" + "".join(f"E:{p}\n" for p in dev["props"]) + "G:seat\nQ:seat\nV:1\n"
    subprocess.run(
        ["incus", "exec", seat, "--", "sh", "-c",
         f"mkdir -p /run/udev/data && cat > /run/udev/data/c{dev['major']}:{minor}"],
        input=entry, text=True, check=False)

    # 3) Ereignis senden, damit laufende Clients es bemerken.
    cmd = ["incus", "exec", seat, "--", "/root/fakeudev.py", "add", dev["syspath"],
           "--subsystem", "input", "--devname", f"input/{node}",
           "--major", str(dev["major"]), "--minor", str(minor)]
    for p in dev["props"]:
        cmd += ["--prop", p]
    subprocess.run(cmd, capture_output=True, check=False)


def detach(seat, node, dev):
    print(f"  - {node:<10} {dev['name']}")
    cmd = ["incus", "exec", seat, "--", "/root/fakeudev.py", "remove",
           dev["syspath"], "--subsystem", "input", "--devname", f"input/{node}",
           "--major", str(dev["major"]), "--minor", str(dev["minor"])]
    subprocess.run(cmd, capture_output=True, check=False)
    incus("config", "device", "remove", seat, f"in-{node}", check=False)
    subprocess.run(
        ["incus", "exec", seat, "--", "rm", "-f",
         f"/run/udev/data/c{dev['major']}:{dev['minor']}"], check=False,
        capture_output=True)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--seat", required=True, help="Name des Incus-Containers")
    ap.add_argument("--match",
                    default=r"passthrough|x-?box|dualsense|dualshock|nintendo|"
                            r"sunshine|gamepad|joystick|controller",
                    help="regulärer Ausdruck auf den Gerätenamen. Greift nur "
                         "auf virtuelle Geräte — Tastatur und Maus heißen bei "
                         "Sunshine '… passthrough', Gamepads dagegen nach dem "
                         "emulierten Modell (z.B. 'Xbox One')")
    ap.add_argument("--tag", default=None,
                    help="Seat-Tag, den der Gerätename tragen muss (Vorgabe: "
                         "der Seat-Name). Sunshine hängt ihn an, wenn XDG_SEAT "
                         "gesetzt ist. \"\" schaltet die Prüfung ab.")
    ap.add_argument("--interval", type=float, default=0.5)
    args = ap.parse_args()

    if os.geteuid() != 0 and not os.access("/run/udev/data", os.R_OK):
        print("Hinweis: ohne root sind manche udev-Daten nicht lesbar.",
              file=sys.stderr)

    tag = args.seat if args.tag is None else (args.tag or None)
    if tag:
        print(f"Broker läuft für Seat '{args.seat}', verlangt Tag '({tag})'.")
    else:
        print(f"Broker läuft für Seat '{args.seat}' OHNE Tag-Prüfung — "
              f"nur bei einem einzelnen Seat vertretbar.")
    print("Strg-C beendet.\n")

    # Verwaiste Einhängungen aus früheren Läufen abräumen. Sunshine-Instanzen,
    # die abstürzen oder neu starten, hinterlassen ihre Geräte am Host; die
    # zugehörigen Einhängungen im Seat zeigen danach ins Leere und tauchen in
    # sway weiter als tote Eingabegeräte auf.
    stale = 0
    remembered = load_state(args.seat)
    listing = incus("config", "device", "list", args.seat, check=False)
    for dev_name in listing.stdout.split():
        if not dev_name.startswith("in-"):
            continue
        node = dev_name[3:]
        sysdev = f"{SYS_INPUT}/{node}"
        name = read(f"{sysdev}/device/name")
        if not name or (tag and f"({tag})" not in name):
            print(f"  ~ {node:<10} verwaist, wird entfernt")
            if node in remembered:
                detach(args.seat, node, remembered[node])
            else:
                # Ohne Erinnerung bleibt nur die Einhängung; sway behält das
                # tote Gerät dann bis zum nächsten Neustart in seiner Liste.
                incus("config", "device", "remove", args.seat, dev_name, check=False)
            stale += 1
    if stale:
        print(f"{stale} verwaiste Einhängung(en) entfernt.\n")

    known = {}
    try:
        while True:
            current = scan(args.match, tag)
            for node, dev in current.items():
                if node not in known:
                    attach(args.seat, dev)
            for node, dev in list(known.items()):
                if node not in current:
                    detach(args.seat, node, dev)
            if current != known:
                save_state(args.seat, current)
            known = current
            time.sleep(args.interval)
    except KeyboardInterrupt:
        print("\nBeendet. Eingehängte Geräte bleiben bestehen.")


if __name__ == "__main__":
    main()
