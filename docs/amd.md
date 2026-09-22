# AMD

**One AMD machine has now run this, and one is not many.** This was written on
a machine with a single NVIDIA card in it and cannot be finished there. What
follows is what was built, what was actually verified, and what is still open,
and then what the one machine reported. Read both of the last two sections
before trusting any of it.

Since 0.9.0 there is a second untested axis, and the two are independent: the
host may be Debian or Fedora rather than Arch, and neither of those has been run
on real hardware either. Everything below is about the card and says nothing
about the distribution. An AMD card on a Fedora host is two untested paths
crossing, and worth reporting as such if you try it —
[docs/installation.md](installation.md) covers the host half.

## What changes and what does not

AMD is the simpler of the two, and most of the difference is subtraction.

The whole NVIDIA arrangement exists because the proprietary driver arrives in a
seat as files injected past the package manager: `nvidia.runtime` mirrors the
host's libraries in, and everything the driver's own package would have brought
with it has to be put back by hand afterwards, which is the `glvnd` manifest,
the GBM backend symlink and the Vulkan ICD. It also means the host and the
seats share one driver version, so an update to the host's NVIDIA userspace
silently leaves the seats behind until they are provisioned again.

On AMD none of that applies. The kernel driver is `amdgpu` on the host, Mesa is
an ordinary package inside the seat, and nothing crosses the boundary except
the device node itself. There is no host userspace for a seat to borrow, so
there is nothing to repair and no version to keep in step: **a host driver
update cannot break an AMD seat.**

| | NVIDIA | AMD |
|---|---|---|
| Host package | the driver's userspace, plus `nvidia-container-toolkit` | none, the kernel driver is enough |
| Into the container | injected by libnvidia-container | `mesa`, `vulkan-radeon` and their 32 bit halves, as packages |
| Incus keys | `nvidia.runtime=true`, `nvidia.driver.capabilities=all` | `nvidia.runtime=false`, the `gpu` device alone |
| Repair step | glvnd manifest, GBM symlink, Vulkan ICD | none |
| pacman | `--assume-installed` on every call | nothing, the providers resolve normally |
| Session environment | `GBM_BACKEND=nvidia-drm`, `__GLX_VENDOR_LIBRARY_NAME=nvidia` | `WLR_RENDER_DRM_DEVICE=<node>` |
| Encoder | `nvenc` | `vaapi`, and `adapter_name` naming the render node |
| Host driver update | seats must be provisioned again | nothing to do |

Capture stays `wlr` on both. On NVIDIA that is because KMS capture does not
work with the proprietary driver; on AMD it works in general and still cannot
be used here, for a reason that is about seats rather than about AMD. KMS
capture wants `cap_sys_admin`, and the capability is the smaller half of it:
DRM master is held per device, so on a machine with one card exactly one seat
could ever capture that way and the others would have nothing. `wlr` capture
asks the seat's own compositor instead, which is per seat by construction. So
the same setting is right for both vendors, for different reasons.

VA-API is the only hardware encoder AMD has on Linux. AMF is a Windows library,
and Sunshine's `amf` option cannot be reached from here at all.

## How the card is chosen

The daemon reads the machine once at startup and every seat gets the same
answer. It walks the **render nodes** under `/sys/class/drm/renderD*` rather
than the cards, because a render node exists only where a driver is bound to a
device that can actually render: a server's management chip and a `simpledrm`
fallback both appear as cards and neither can serve a seat.

With cards from both vendors in one machine NVIDIA wins, because that is the
path that has been run in anger. To override it, name the node:

```json
{ "gpu_render_node": "/dev/dri/renderD129" }
```

in `/etc/polyseat/polyseatd.json`. A device rather than a vendor name, because
on a machine with two AMD cards "amd" still would not say which one.

`host/install.sh` follows the same rule, with one addition: a machine whose
driver is not loaded has no render node at all, and that machine is precisely
the one the installer exists to help, so when the nodes say nothing it asks the
PCI devices instead.

## What was actually verified

On the NVIDIA machine this was written on:

- **The detection agrees with reality.** `DetectGPU("/sys")` on this machine
  reports `nvidia (nvidia, 0000:01:00.0, /dev/dri/renderD128)`, and the stack
  it produces is byte for byte what seats were built with before any of this
  existed.
- **The detection agrees with machines this one is not.** Both the daemon's
  (`go test ./internal/seat -run GPU`) and the installer's
  (`host/test-gpu-detect.sh`) are checked against sysfs trees built by hand: an
  AMD card alone, two cards at once in both orders, a card with no driver
  bound, an Intel card, an AMD device that is not a GPU, and a machine with no
  card at all. Both detections follow the same rule and are checked to give the
  same answer, since a machine with two cards is exactly what somebody testing
  this will have.
- **Every one of those checks was deliberately broken once** to confirm it
  fails: nine mutations of the daemon's code and eight of the installer's, each
  caught by at least one test.
- **The package set resolves.** In a fresh `archlinux/current` container with
  multilib enabled, the AMD package set together with everything else a seat
  installs resolves to 491 packages and pulls **no** NVIDIA package. Steam then
  resolves on top of it with no `--assume-installed` at all.
- **And the order that makes that work was measured, not assumed.** Asked for
  Steam in an empty container, pacman picks `nvidia-utils` and
  `lib32-nvidia-utils` as the providers of `vulkan-driver` and
  `lib32-vulkan-driver`. With `vulkan-radeon` and `lib32-vulkan-radeon`
  installed first it does not. That is why the driver packages go in during the
  packages step and Steam comes after, and it is a real constraint rather than
  a tidy one.
- **Incus passes the device through under its own name.** A seat on this
  machine shows `/dev/dri/card1` and `/dev/dri/renderD128`, mode `0666`, the
  same names the host has, with no `by-path` directory inside. That is what
  makes writing the host's node path straight into Sunshine's `adapter_name`
  correct rather than a guess.
- **Sunshine already depends on `libva`**, so nothing extra is needed for the
  loader. Arch folded `libva-mesa-driver` into `mesa` (which `replaces` it as
  of `1:24.2.7-1`), and `mesa` carries `radeonsi_drv_video.so`, the VA-API
  driver itself. `libva-utils` is installed for `vainfo` alone.
- **One Vulkan manifest serves both architectures.** `vulkan-radeon` ships
  `/usr/share/vulkan/icd.d/radeon_icd.json` with a bare
  `"library_path": "libvulkan_radeon.so"`, and `lib32-vulkan-radeon` ships only
  the library, so the loader finds the right one by architecture. Nothing has
  to be written for the 32 bit side, which is the opposite of the NVIDIA case.
- **The encoder readout needs nothing.** Sunshine names its encoders
  `h264_vaapi` and so on, and the parser already takes the part after the
  underscore, so an AMD seat reports `vaapi` in the interface without a change.

## What is not verified, and cannot be here

Everything that needs the card to exist:

1. **That it encodes in hardware at all.** The one that matters. `vainfo` is
   checked during provisioning and warns rather than refuses, and the encoder
   line in the interface is the real answer. If it says `libx264`, the GPU path
   is broken.
2. **That wlroots renders headless on `amdgpu` inside a container.** The
   backend needs a render node and GBM, both of which are there, but "should"
   is not "does".
3. **That Sunshine's wlr capture hands its frames to VA-API** without a copy
   through system memory. If it does copy, it will still work and be slower.
4. **That 32 bit games find the GPU.** `lib32-vulkan-radeon` and `lib32-mesa`
   are installed and resolve; whether a 32 bit process in a seat gets hardware
   acceleration from them is untested.
5. **Which codecs come out.** AV1 needs RDNA 3 or newer, HEVC most things
   since Polaris. Sunshine probes and offers what it finds, so this should look
   after itself, but nobody has watched it.
6. **Anything about a specific card.** Everything since GCN has VCE or VCN.
   Pre GCN cards want the `r600` VA driver, which is in `mesa` but has no
   encoder worth the name.

One case is knowingly not handled: **swapping an NVIDIA card for an AMD one
under seats that already exist.** The Incus keys are corrected, so the seats
still start, but the injected NVIDIA libraries are real files in each container
filesystem by then and pacman will collide with them when it tries to install
Mesa over the top. Delete those seats and build them again. The shared library
survives that, so the games do not have to be downloaded twice.

## If you have an AMD card, this is the useful hour

```
sudo host/install.sh                     # says AMD in its first step
```

Add a seat, provision it, and then read three things:

```
# 1. What the provisioning log said at the gpu step. It prints the card and,
#    on AMD, whether VA-API offers an encoder at all.

# 2. The encoder line on the seat's card in the web interface.
#    vaapi = right. libx264 = the GPU path is broken and this is the bug.

# 3. From inside the seat, the two commands that say why:
sudo incus exec <seat> -- vainfo --display drm --device /dev/dri/renderD128
sudo incus exec <seat> -- sudo -u player env XDG_RUNTIME_DIR=/run/user/1000 eglinfo -B
```

Then a stream, and whether a game runs. Please report what happens either way,
including that it simply worked: this document exists because nobody knows.

## What one AMD machine has reported

[Issue #3](https://github.com/superuser404notfound/Polyseat/issues/3), against
0.18.0 on a CachyOS host with two AMD cards in it: an RX 9070 XT at
`0000:03:00.0` and a Radeon AI PRO R9700 at `0000:0b:00.0`, with
`gpu_render_node` naming the second one's node because it has no display
attached. A seat was built on it, came up and streamed to a Moonlight client.

What that settles, in the order of the list above:

- **The detection and the override are right on a real AMD machine.** The
  daemon logged `amd (amdgpu, 0000:0b:00.0, /dev/dri/renderD129)` and built the
  seat against the node it was told to, not the one it found first.
- **wlroots does render headless on `amdgpu` inside a container**, which was
  item 2 and could only ever be answered by a card. The seat has a session, a
  desktop and a stream on it.
- **The package set installs on a real AMD machine**, not only in the container
  the resolution was measured in.

What it does not settle:

- **Whether it encodes in hardware**, which is item 1 and still the one that
  matters. The report does not carry the encoder line, which is why the report
  now prints it for a running seat rather than asking for it in prose.
- **What the picture and the sound are worth.** The same machine reports a
  large performance drop and audio that is cut harshly, with the client on
  wifi, and neither has been separated from the network yet.

And it turned up one thing this document did not have a line for. **On a
machine with two cards, three things have to agree about which one, and until
0.20.0 only two of them were set:** the compositor through
`WLR_RENDER_DRM_DEVICE` and Sunshine through `adapter_name`. The third is the
game, and nothing pointed it anywhere: the Incus `gpu` device passes every card
in the host through, so both render nodes were inside the seat, and a Vulkan
game takes the first one the loader offers. Where that is not the node the seat
composites and encodes on, every frame crosses the bus twice on its way to the
client, and on the machine in that issue one of the two slots runs at x4.

**A seat gets one card now, where the machine has more than one.** The device
carries the chosen card's PCI address, so there is one render node in the seat
and nothing left inside it to choose wrongly. That is the answer rather than an
environment variable pointing the game at a node, because `DRI_PRIME` and
`MESA_VK_DEVICE_SELECT` are read by a Mesa layer that a seat does not install,
and because a seat with one node in it needs nobody to read anything.

The address is written only where it can matter and only while the machine
still has that card. Incus refuses to start a container whose GPU address
matches nothing, measured against 7.4:

```
Failed to start device "gpu": Invalid PCI address (no device found): 0000:09:00.0
```

so a card moved to another slot would otherwise be a seat that cannot start at
all. The device is written again before every start, which is what takes the
address back off. A machine with one card is not touched: the device it gets is
the device it already had, checked against the two seats here, which Incus
reports as `type: gpu, mode: 0666` and nothing else.

`gpu_all_cards` in `/etc/polyseat/polyseatd.json` puts every card back in the
seat, for the machine that wants the split on purpose: encoding on one card to
leave the other free for the game. It is an honest escape hatch rather than a
feature, because which card the game then takes is its loader's decision again
and nothing here can make it. Nobody has reported that setup.

What nobody has measured is what the split actually cost, because the machine
that has two cards has not yet said which one its games were running on.
