# M0 - input spike

**One question:** does the container architecture hold up the input chain?

If not, the design collapses and we end up with one Unix user per seat instead
of containers. That is something you want to know after an evening, not after
six weeks. Which is why this comes before everything else - before Sway, before
Sunshine, before Go, before the GUI.

The spike uses **no Sunshine**. A synthetic pad (`padgen.py`, raw uinput, ~120
lines) isolates the variable we care about. Sunshine brings capture, encoder,
networking and pairing along - all of which would just be noise here.

## Hypotheses

| | Hypothesis | Expectation |
|---|---|---|
| **H1** | An Incus container with `nvidia.runtime=true` sees the GPU | `nvidia-smi` works inside the container |
| **H2** | A uinput pad created *inside the container* shows up on the **host** under `/dev/input/` | yes - uinput is not namespaced |
| **H3** | The same pad does **not** show up in the container's `/dev/input` | yes - that is the structural isolation |
| **H4** | The device name is freely settable, and a host udev rule can match on it | yes - this carries the seat assignment |
| **H5** | `incus config device add … unix-char` gets the node into the **running** container | yes - hotplug-capable per the Incus docs |
| **H6** | **SDL inside the container recognises the pad** - even though no udev runs there | **unknown. This is the risk.** |

## Pass criterion

**H6 green**, if need be with `SDL_JOYSTICK_DISABLE_UDEV=1`. Everything else is
preparation.

Three possible outcomes:

- **H6 green with no tricks** → the architecture holds, on to M1.
- **H6 green only with `SDL_JOYSTICK_DISABLE_UDEV=1`** → it holds, but every
  seat needs the variable in its environment, and Steam Input has to be checked
  separately (Steam does not only use SDL).
- **H6 red** → a fake-udev shim is required (concept borrowable from Wolf), or
  fall back to one Unix user per seat.

## Prerequisites

`00-prereqs.sh` checks everything read-only and prints the missing root
commands. Short version, once, as root:

```
sudo pacman -S --needed incus nvidia-container-toolkit go
sudo systemctl enable --now incus.socket
sudo usermod -aG incus-admin $USER      # log in again afterwards
sudo incus admin init --minimal
```

`--minimal` creates a dir-backed storage pool. Good enough for M0; for the
library pool (M6) we will want btrfs.

## Procedure

```
./00-prereqs.sh                 # read-only, prints missing root commands
./10-create-seat.sh             # container 'm0' + /dev/uinput + test tools
./20-run-pad.sh                 # start padgen.py in the container (blocks)
./30-observe-host.sh            # in a second shell: check H2/H3/H4
./40-inject.sh <eventN>         # H5: node into the running container
./50-verify.sh                  # H6: evtest + SDL inside the container
./60-hide-from-host.sh          # H4: udev rule, hides the pad from KDE
./99-cleanup.sh
```

## Verified up front on the host

`padgen.py` was tested directly on the host without a container (rooky is in
the `input` group, so `/dev/uinput` is writable):

- The device gets created and the name is freely settable → **the seat tag
  works.**
- It reports as `045e:028e`, and evtest reads the expected Xbox 360 layout.
- Host udev classifies it as `ID_INPUT=1`, `ID_INPUT_JOYSTICK=1` - exactly the
  properties the rule in `60-hide-from-host.sh` strips.
- After `UI_DEV_DESTROY` nothing is left behind.

**Finding for the later broker:** `UI_GET_SYSNAME` returns `inputNN`, but the
device node is called `eventM` - and **the numbers do not match** (observed:
`input37` → `/dev/input/event7`). The mapping goes through
`/sys/class/input/inputNN/eventM/`. If the broker creates the pads itself, that
is the reliable correlation path - the name tag is then only needed for the
udev rule, no longer for the assignment.

## Log - run on 2026-07-27

**Result: the container architecture holds.** All hypotheses green, with one
condition (see H7).

| Hypothesis | Result | Note |
|---|---|---|
| H1 | ✅ | `nvidia-smi` inside the container reports the RTX 4080 |
| H2 | ✅ | pad created in the container appears on the host as `/dev/input/event24` |
| H3 | ✅ | `/dev/input` does **not exist at all** in the container - isolation by default |
| H4 | ✅ | udev rule strips `ID_INPUT`, `libinput list-devices` finds nothing |
| H5 | ✅ | `unix-char` hotplug gets the node into the running container |
| H6 | ✅ | SDL recognises the pad as an "Xbox 360 Controller" - **even with no tricks** |
| H7 | ⚠️ | hotplug at runtime **only** with `SDL_JOYSTICK_DISABLE_UDEV=1` |

### H7 - the condition

H6 only answers whether SDL finds what is already there when it starts. That is
not enough: Steam runs permanently inside a seat, and pads only come into
existence once somebody connects.

Measured with `55-hotplug.sh`: a second pad attached while the watcher is
running goes **unnoticed**. The node does arrive - an `sdlprobe` started
afterwards sees both pads - but the *notification* is missing. The reason:
libudev enumerates via `/sys`, which is visible inside the container, while the
udev *monitor* hangs off netlink uevents, and those do not reach the container.

With `SDL_JOYSTICK_DISABLE_UDEV=1` SDL polls `/dev/input` directly instead and
reliably reports the pad attached afterwards.

**Consequence for the architecture:** the variable belongs in every seat's
environment. A fake-udev shim is not needed for now.

**Open:** Steam bundles its own SDL and also uses udev for Steam Input. Whether
the variable works there too has to be checked in M1 with real Steam -
`sdlprobe` does not answer that.

### Further findings from the run

- **`nvidia.runtime=true` only mirrors libraries**, no device nodes. Without an
  additional `gpu` device, `nvidia-smi` runs and reports "No devices found". On
  a related note: `nvidia-smi -L` exits 0 even then - checks have to look at the
  output, not the return code.
- **The order is mandatory:** install packages, *then* enable `nvidia.runtime`.
  Otherwise pacman collides with the injected driver files
  (`mesa: /usr/lib/libGLX_indirect.so.0 exists in filesystem`). `--overwrite`
  would be the wrong answer - it replaces the driver file.
- **Incus needs its own idmap ranges.** CachyOS ships only an entry for the
  user; without `root:1000000:1000000000` in `/etc/subuid` and `/etc/subgid`
  every container start fails with "System doesn't have a functional idmap
  setup".
- **SDL renames known devices.** Inside the container the pad is called "Xbox
  360 Controller", not what the evdev name says. So the seat tag is nothing that
  anything *above* evdev may rely on - it is fine for udev rules, not for
  application logic.
- `UI_GET_SYSNAME` returns `inputNN`, the node is called `eventM`, and the
  numbers do not match (`input40` → `event24`). Map via
  `/sys/class/input/inputNN/eventM/`.
