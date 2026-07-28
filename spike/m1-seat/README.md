# M1 - one complete seat

**Goal:** an Incus container running headless Sway, Sunshine and NVENC, with its
own LAN address, so that Moonlight can connect to it.

**Status: achieved** (2026-07-27). Sunshine reports `h264_nvenc [nvenc]`, output
1920x1080, one audio sink, its own LAN address.

## Procedure

```
./10-create-seat.sh        # container, macvlan, CachyOS repo, packages, user
./15-nvidia-userspace.sh   # what nvidia.runtime does NOT bring along
./20-session.sh            # PipeWire, Sway, Sunshine as user units
./30-verify.sh             # final check plus connection details
./99-cleanup.sh
```

## Networking: two interfaces, on purpose

Every seat gets **two** network interfaces:

- `eth1`, **macvlan** directly on the physical uplink. The seat gets its own LAN
  address via DHCP and is an independent host as far as Moonlight is concerned.
- `eth0`, on `incusbr0`, the management path.

The reason for eth1 matters architecturally: **with a LAN address per seat,
all port juggling disappears.** Every seat listens on the standard Sunshine
ports, nobody has to maintain port offsets, and Moonlight is set up on the
client the ordinary way.

The reason for eth0 is the well known property of macvlan: **host and container
cannot reach each other over that interface.** The Sunshine web UI is therefore
only reachable from the host browser through the management address, and that is
exactly the path the daemon will later use for its pairing proxy.

## The most expensive finding: what `nvidia.runtime` does not bring

`nvidia.runtime=true` mirrors the driver **libraries** into the container, and
nothing else. Four things are missing, and without them EGL ends up on Mesa,
CUDA cannot take over the GL context, and Sunshine silently falls back to
`libx264 [software]`. A seat would look perfectly healthy and be unusable.

| missing | where it normally comes from | symptom |
|---|---|---|
| `/usr/share/glvnd/egl_vendor.d/10_nvidia.json` | `nvidia-utils` | EGL only tries Mesa: "failed to create dri2 screen" |
| `/usr/lib/gbm/nvidia-drm_gbm.so` | `nvidia-utils` (only a symlink!) | GBM cannot find the NVIDIA backend |
| `egl-gbm`, `egl-wayland` | separate Arch packages, **not part of the driver** | "Couldn't initialize EGL display: [00003001]" |
| Vulkan ICD `nvidia_icd.json` | `nvidia-utils` | Vulkan sees no GPU |

Installing `nvidia-utils` inside the container would be the wrong answer: it
would put its own `.so` files up against the injected ones. So the manifests get
generated and the two real packages get installed normally.

**Order:** packages first, manifests second. Copying the JSON files in by hand
beforehand blocks `pacman` with a file conflict, which is exactly what happened
here.

## Further findings

- **`incus launch` returns before systemd inside the container is ready.** The
  first `systemctl` then fails with "Failed to connect to system scope bus".
  `10-create-seat.sh` therefore waits for `systemctl is-system-running`.
- **`/dev/dri/card1` arrives as `root:root 0660`**, so the player cannot open it
  and EGL fails with "Permission denied". `incus config device set gpu mode=0666`
  fixes it.
- **The libinput backend aborts without input devices.** `/dev/input` is empty
  inside the container at startup, so sway needs `WLR_LIBINPUT_NO_DEVICES=1`.
- **Sunshine must start after sway**, not at the same time. Otherwise it reports
  `[wayland] Couldn't connect to Wayland display` and `Platform failed to
  initialize`, and afterwards **all** encoders fail, including the software
  encoder, which hides the cause nicely.

  The nasty part: **Sunshine does not exit.** It keeps running broken and serves
  the web UI, systemd sees a healthy service, and the client only gets "Failed to
  initialize video capture/encoding. Is a display connected and turned on?". The
  state survived a whole night that way.

  An `import-environment` in the sway config is not enough, because a `BindsTo`
  restart pulls Sunshine up earlier than sway's `exec`, and because a
  `WAYLAND_DISPLAY` imported once can be stale after a sway restart.
  `sunshine-run.sh` therefore looks for the socket itself and waits for it.
- **By default Sunshine's CSRF protection only allows `localhost` origins.**
  Opening the web UI through the LAN or management address, which is what always
  happens, fails on save with "CSRF Protection Error" without Sunshine logging
  anything that points at it. `csrf_allowed_origins` expects a comma separated
  list of full URL prefixes **including scheme and port**; `20-session.sh`
  generates it from the seat's actual addresses.

  **Consequence for the daemon:** seat addresses have to be stable. If DHCP
  changes the address, the CSRF list no longer matches and the UI becomes
  unusable. So seats need a fixed address (DHCP reservation or statically
  configured), which is sensible anyway because the Moonlight setup on the client
  also depends on the address.
- **Sunshine lives in the CachyOS repository, not in Arch.** The container only
  pulls in the non CPU-optimised `[cachyos]` repo, and at the *end* of
  `pacman.conf`, so that Arch wins for every shared package.
- **Declare the null sink in PipeWire, do not create it with `exec pactl`.**
  PipeWire survives a sway restart, the `exec` does not, so after four restarts
  there were four identical sinks. (The null sink itself was later dropped
  entirely, see M3.)

## Input, measured on the live stream

With a Moonlight client connected (iPhone, 2026-07-27) we looked at where
Sunshine's virtual devices end up:

- Inside the container **`/dev/input` does not exist at all**.
- On the host they are there: `Mouse passthrough`, `Mouse passthrough
  (absolute)`, `Keyboard passthrough`.

**That is the M0 finding, now confirmed against real Sunshine:** `uinput` is not
namespaced, so the kernel registers the devices globally and the host's udev
creates the nodes, while in the seat that created them they are invisible. Sway
therefore has **zero** input devices. Keyboard, mouse and pad in the stream
cannot possibly work before the broker exists.

Two follow-up findings:

- **The devices are named identically in every seat.** "Keyboard passthrough"
  carries nothing to tell seats apart. (Solved in M2 through `XDG_SEAT`.)
- **They do not currently reach the KDE desktop**, because `libinput
  list-devices` does not list them: udev gives them no `ID_INPUT` properties even
  though `input_id` runs according to `udevadm test`. The cause is **unclear**,
  and the design must not rely on it. If these devices did get classified on
  another system or after an update, a seat's client would reach through into the
  host desktop. The hide rule therefore has to cover them explicitly rather than
  build on unexplained behaviour.
- **Crashed Sunshine instances leave their devices behind.** Two complete sets
  were observed side by side. The broker has to clean up orphans.
