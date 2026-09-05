# M8 - HDR out of a headless seat

**Goal:** a Moonlight client streaming from a seat in real HDR: Rec. 2020
primaries, the SMPTE 2084 PQ transfer function, ten bits a channel, end to end.

**Status: achieved** (2026-09-05). A Moonlight client streams from a headless
seat in Rec. 2020 with the ST2084 PQ transfer function at ten bits, and the
picture is right. Two small patches carried it, one against wlroots and one
against Sunshine, both in `patches/`.

HDR **content** took two more steps and works as well. gamescope, the usual
route, refuses on sway for reasons of its own; a Vulkan layer gets past that,
and an application in the seat now renders into an
`VK_COLOR_SPACE_HDR10_ST2084_EXT` swapchain and streams out as Rec. 2020 PQ.
See "Content is a separate problem".

The one thing no log can answer is whether the picture is right. For the desktop
it was confirmed by eye; for the ramp it has not been.

First, what is measurable without a seat at all, and repeatable by anybody:

- **Patch 1 does what it claims.** `./check-wlroots-patch.sh` builds wlroots
  0.20.2 twice, patched and as upstream ships it, and runs
  `headless-hdr-probe.c` against each. The probe creates a real headless output
  and asks it the two questions sway's `output_supports_hdr()` asks, then hands
  it an HDR output state. Upstream answers `primaries 0x0, transfer functions
  0x0` and refuses the state; the patched build answers `0x3` and `0xb` and
  accepts it. The unpatched run is required to fail, because a probe that passes
  against both would be measuring nothing.
- **Patch 2 is internally consistent.** `./check-sunshine-patch.sh` compiles the
  C++ it adds at `-Wall -Werror` against colour management headers generated
  from the real protocol xml at wayland-protocols 1.41 and 1.49 - version 1 and
  version 3 of `wp_color_manager_v1`, the spread a seat can actually present -
  and checks that its unit conversions land on BT.2020's actual red primary.
- Both patches apply cleanly to the pinned upstream versions, and the scripts
  are parsed and shellchecked by CI along with the rest of the repository's
  shell.

Since then, on an RTX 4080 seat under polyseatd 0.15.0: sway starts on the
Vulkan renderer headless in the container, the patched wlroots is the one it
loads, `features.hdr` reports true, `output HEADLESS-1 hdr on` takes, and the
output composites at `XB30`. Two of the three unknowns below are answered.

Gate four then built the patched Sunshine, and it reads the output's colour over
the protocol: `Output colour: primaries 6, transfer function 11 (HDR)`, which is
BT.2020 and ST2084 PQ. Its encoder probe encodes a real frame in
`HDR (Rec. 2020 + SMPTE 2084 PQ)` at ten bits through NVENC. All three unknowns
below are answered.

A client then closed it, and the two records line up to the second:

```
10:56:22  Color coding: HDR (Rec. 2020 + SMPTE 2084 PQ)
10:56:22  Color depth: 10-bit
          {"app":"Desktop","width":2250,"height":1206,"fps":120,
           "hdr":"true","peer":"10.20.30.32","started":"...T10:56:22Z"}
```

The left half is what Sunshine encoded; the right half is `polyseat-session`
recording what the client asked for. `"hdr":"true"` is the client's request and
the timestamps match, so this is one stream rather than two coincidences. The
earlier HDR lines in the log, at 10:42 and 10:55, are the encoder probe.

And the picture looks right, which is the only thing no log can answer. That
matters more than it sounds: the failure this was most exposed to would have
passed every check and looked wrong to a person, because the metadata conversion
had been verified arithmetically against BT.2020's red primary and never against
an eye.

Worth noting for the product: the output was at **2250x1206 at 120 Hz**, not the
seat's configured mode. `polyseat-resize` had adopted the client's size mid
stream and HDR survived the mode change, which is not something this spike set
out to test and is exactly what would have broken quietly.

## Why this is a spike and not a feature

The short version of what was found: **there is no off-the-shelf way to do this,
and the reason is a blind spot rather than a limitation.**

Every compositor gates HDR on a capability that only a real DRM connector has,
read out of a monitor's EDID. That is the correct rule for a physical panel,
which can be lied to, and it is a rule nobody has yet had reason to relax for a
virtual output, which has no panel to lie to. Each of these was read in the
source rather than guessed:

| where | what stops it |
|---|---|
| wlroots headless | `supported_primaries` and `supported_transfer_functions` are set in exactly one place, `backend/drm/util.c`, out of the EDID. The headless backend sets neither, and does not accept `WLR_OUTPUT_STATE_IMAGE_DESCRIPTION` either. |
| KWin virtual backend | `virtual_backend.cpp` advertises `CustomModes` alone; HDR needs `BackendOutput::Capability::HighDynamicRange`. |
| Mutter virtual monitor | `meta-output-virtual.c` never sets `supported_hdr_eotfs`, so `meta-monitor.c` never offers `META_COLOR_MODE_BT2100`. |
| Hyprland, aquamarine headless | `Headless.cpp` has no HDR code at all; `supportsPQ` and `supportsBT2020` come only from the EDID in the DRM backend. |
| vkms | no `HDR_OUTPUT_METADATA` property and no EDID at all, it calls `drm_add_modes_noedid`. |
| gamescope | the one compositor that will force HDR10 PQ on any backend, but its PipeWire output offers only BGRx and NV12 at eight bits, and it speaks neither wlr-screencopy nor the portal. Nothing can capture HDR out of it. |
| Sunshine | `is_hdr()` is virtual with a `false` default in `platform/common.h` and overridden in two files: `kmsgrab.cpp`, from the connector property, and `pipewire.cpp`, from the SPA colorimetry. `wlgrab.cpp` contains no HDR code whatsoever. |
| Sunshine, KMS capture | wants `cap_sys_admin` and a real connector. DRM master is per device rather than per connector, so even if a seat were given it, exactly one seat on the machine could ever stream. That is the opposite of what this project is. |

The way out came from [Wolf issue
222](https://github.com/games-on-whales/wolf/issues/222), where Smithay's
maintainer answers the same question for the same shape of problem. Paraphrased:
for an output with no panel, HDR is not a hardware matter at all. What is needed
is `wp-color-management-v1` so the client can say "this surface is BT.2020 PQ", a
framebuffer with at least ten bits so the values survive, and an encoder told
which colourspace it is encoding. Nothing else.

Measured against Polyseat's own stack, most of that already exists:

- **gamescope is already a complete colour management client.** Its Wayland
  backend builds `wp_image_description_v1` objects for HDR colourspaces and
  attaches them to its surfaces, and in
  `CWaylandPlane::Wayland_WPImageDescriptionInfo_Done` it sets
  `bExposeHDRSupport` when the parent's target luminance exceeds its reference
  luminance. Nothing to write.
- **sway can already do it.** 1.12 has `output <name> hdr on`, it is in Arch, and
  enabling HDR raises `render_bit_depth` to 10 on its own. It needs the Vulkan
  renderer, because `output_supports_hdr()` also asks for
  `renderer->features.output_color_transform` and gles2 has none.
- **wlroots already publishes the numbers.**
  `cm_output_handle_get_image_description` hands the output's image description
  to any client that asks, and `wlr_color_transfer_function_get_default_luminance`
  fills in the PQ defaults, 10000 against a reference of 203 - which is exactly
  the comparison gamescope makes.
- **Sunshine's encoder half already does ten bits.** `graphics.cpp` maps a ten
  bit target onto `GL_RGB10_A2`, the CUDA path takes `P010LE`, and `wlgrab` and
  `kmsgrab` call the same `cuda::make_avcodec_gl_encode_device` in their VRAM
  paths. HDR travels through `colorspace`, not through the capture format.

Which leaves two gaps, and they are what `patches/` contains.

## The chain

```
a game rendering HDR
  → gamescope --hdr-enabled, nested in the seat's sway     already works
  → sway 1.12, Vulkan renderer, output HEADLESS-1 hdr on   configuration only
  → wlroots headless admits to BT.2020 and PQ              patch 1, 10 lines
  → wlr-screencopy hands out an XRGB2101010 dmabuf         should follow
  → Sunshine wlgrab reads the output's image description   patch 2
  → NVENC HEVC Main 10, Rec. 2020 PQ                       already works
  → Moonlight in HDR
```

## The patches

**`patches/wlroots-headless-hdr.patch`**, against tag `0.20.2`, which is what
Arch ships as `wlroots0.20` and therefore what the seat's sway links against.
Ten lines: the headless output advertises sRGB and BT.2020 primaries, sRGB,
gamma 2.2 and PQ transfer functions, and accepts
`WLR_OUTPUT_STATE_IMAGE_DESCRIPTION`. The argument for it is in the comment and
is worth repeating here, because it is the whole idea: a headless output has
nothing behind it that could disagree, so advertising these is not a claim about
hardware but a statement that the output presents what it is handed.

**`patches/sunshine-wlgrab-hdr.patch`**, against commit
`4cb15e9240a7c5dc0beb07814cb00e7527b9a0f5`. It binds `wp_color_manager_v1`,
reads the captured output's image description once when the capture is set up,
and implements `is_hdr()` and `get_hdr_metadata()` from it. That is the same job
`pipewire.cpp` already does with the SPA colorimetry, over the Wayland protocol
instead. The unit conversions are the fiddly part and are commented where they
happen: the protocol carries CIE 1931 coordinates multiplied by a million and
Moonlight's CTA-861 structure wants them multiplied by fifty thousand, while the
luminances already agree.

Both are written to be offerable upstream rather than carried forever. There is
no competing merge request at wlroots; the open ones were read.

## Procedure

Run against a seat polyseatd has already provisioned. Nothing here builds a
seat, and everything it writes is either a systemd drop-in the daemon does not
write or a file under `/usr/local`, so `99-cleanup.sh` undoes all of it by
deleting files.

```
./check-wlroots-patch.sh   # no seat: does patch 1 actually change the answer
./check-sunshine-patch.sh  # no seat: is patch 2 internally consistent
./00-prereqs.sh     # versions, Vulkan, disk. Refuses early rather than late
./10-vulkan.sh      # gate 1: does sway survive the Vulkan renderer here at all
./20-wlroots.sh     # gate 2: build and load the patched wlroots
./30-hdr.sh         # gate 3: put HEADLESS-1 into HDR and check the buffer format
./40-sunshine.sh    # gate 4: build and load the patched Sunshine. The long one
./50-verify.sh      # what the whole chain did. Run it again with a client on
./60-gamescope.sh   # the usual route to HDR content, and why it does not work
./70-hdr-layer.sh   # the route that should: a Vulkan layer, no gamescope
./80-hdr-content.sh # the last mile: a client that renders real HDR
./99-cleanup.sh
```

Every script takes the seat's name from `CT`, which defaults to `seat1` because
that is what M1 built. A machine running polyseatd will have called its seats
something else, so in practice it is `CT=vince ./10-vulkan.sh` and so on, with
the same value for every script in the run. `00-prereqs.sh` lists what is
actually there when it cannot find the one it was given.

Talking to Incus needs the `incus-admin` group. Once, then log in again:

```
sudo usermod -aG incus-admin $USER
```

`INCUS_CMD="sudo incus"` works instead, and is not recommended: these scripts
call incus dozens of times each.

The two `check-` scripts are the odd ones out and deliberately so. They need no
seat, no container and no GPU, so the half of this spike that can be checked
without hardware is checkable by anybody.

`check-wlroots-patch.sh` wants `meson`, `ninja`, a C compiler and wlroots' own
build dependencies, and it skips loudly rather than failing when they are not
there - a missing toolchain is not a broken patch. Two builds, so give it a few
minutes. If the distribution has neither meson nor ninja, both install into a
venv: `python3 -m venv /tmp/mv && /tmp/mv/bin/pip install meson ninja`.

`check-sunshine-patch.sh` wants only `wayland-scanner`, `g++` and `curl`. It
compiles against two protocol versions rather than one, and that is the point
rather than thoroughness for its own sake: 1.49 adds a `ready2` event, so the
generated listener struct grows a member, and the patch uses designated
initialisers precisely so that it survives both. Compiling against one of them
would not have shown that. It says nothing about whether Sunshine as a whole
builds; only `40-sunshine.sh` says that.

Neither is in CI, on purpose. Both reach out to GitHub or to
gitlab.freedesktop.org, so they can go red for reasons that have nothing to do
with this repository, and what they check is not in the shipped tree: these
patches live here, not in `provision.go`. When they move, they earn a place in
CI. Until then they are one command each.

The gates are in that order on purpose: each is the cheapest remaining thing
that can end the idea. Gate 1 costs a restart and would kill it outright.
Gate 4 costs half an hour and several gigabytes, and there is no point paying
that before the three cheap ones have passed.

## Upstream

Both patches were offered upstream on 2026-09-05, before any of this was wired
into `provision.go`, because two rebuilt packages in every seat is the real cost
of this feature and upstream is the only thing that removes it.

| | |
|---|---|
| wlroots | [merge request 5453](https://gitlab.freedesktop.org/wlroots/wlroots/-/merge_requests/5453) |
| Sunshine | [pull request 5615](https://github.com/LizardByte/Sunshine/pull/5615) |

`upstream/` holds both texts and what happened when they were filed - the
wlroots one is worth reading before writing anything long on a fresh
freedesktop.org account.

## Content is a separate problem

The transport is HDR. Putting HDR **into** it is a different question, and on
Linux the answer for games is gamescope. That does not work on sway today, for a
reason that has nothing to do with either patch here.

gamescope only uses a compositor's colour management when the compositor
advertises the whole set it wants, in
`CWaylandBackend::SupportsColorManagement()`:

```
parametric  set_primaries  set_mastering_display_primaries
extended_target_volume  set_luminances  windows_scrgb
```

sway advertises two of the six. From `sway/server.c`:

```c
.features = {
    .parametric = true,
    .set_mastering_display_primaries = true,
},
```

Measured in the seat, the four that are missing are `set_primaries`,
`extended_target_volume`, `set_luminances` and `windows_scrgb`.

So `SupportsColorManagement()` returns false, the HDR assignment in
`Wayland_WPImageDescriptionInfo_Done` never runs, and `bExposeHDRSupport` stays
false. The log is a good liar about this, because everything either side of the
verdict looks right:

```
xdg_backend: HDR INFO
  cv_hdr_enabled: true
  uMaxLum: 10000, uRefLum: 203
  bExposeHDRSupport: false
```

The luminances are exactly the ones the seat's output publishes, 10000 against a
reference of 203, and gamescope's own test is `uMaxTargetLuminance >
uReferenceLuminance`. It is not that the test failed. It is that the test was
never reached. gamescope then brings its swapchain up as `B8G8R8A8_UNORM` in
`SRGB_NONLINEAR`, which is SDR.

This is sway's choice rather than a limit of wlroots, which can advertise all
six. Making sway advertise them is not a one-line change: it would have to
actually honour `set_primaries`, `set_luminances`, `extended_target_volume` and
`windows_scrgb`, and claiming them without honouring them would be worse than
claiming nothing. That is a third upstream conversation and has not been
started.

`--hdr-debug-force-support` is not the answer either: it makes gamescope offer
HDR to the game and then, in its own words, output it as SDR anyway. That is
tone-mapping, not the thing being measured.

### gamescope's list is gamescope's own

Read next to what other clients ask for, that requirement looks less like a
standard and more like one program's preference. Both of the other implementations
that put HDR into a Vulkan swapchain are far more forgiving:

- **Mesa's Vulkan WSI** speaks `wp-color-management-v1` directly and maps
  `VK_COLOR_SPACE_HDR10_ST2084_EXT` onto BT.2020 with ST2084 PQ. It requires no
  feature list at all: `color_management_handle_supported_features` notes only
  `set_mastering_display_primaries` and `extended_target_volume`, and otherwise
  collects whatever transfer functions and primaries the compositor offers.
- **`VK_hdr_layer`**, a Vulkan layer rather than a driver, so it sits above
  whichever ICD is in use. Its only hard requirement is `parametric`; for HDR10
  it wants BT.2020 and ST2084 PQ, and the two HDR10 entries in its format table
  carry `.extended_volume = false`, so the feature sway is missing is explicitly
  not needed.

Every one of those conditions is already met in the seat. sway advertises
`parametric` and the PQ transfer function, and
`wlr_color_manager_v1_primaries_list_from_renderer()` emits sRGB **and BT.2020**
whenever the renderer can do input colour transforms, which the Vulkan renderer
can - that is the same renderer this spike had to switch to anyway.

So the route worth trying is not gamescope. It is `ENABLE_HDR_WSI=1` with that
layer, which needs only `vulkan` and wayland to build and would sit in the seat
much as the patched wlroots does. On Mesa it would not even be needed; this seat
is NVIDIA, where the driver's own WSI is the one without colour management.

That this works on sway is not a guess about the protocol: sway issue 9115 is
somebody running DOOM Eternal in HDR on sway with `PROTON_ENABLE_WAYLAND=1` and
`PROTON_ENABLE_HDR=1`, and complaining about latency rather than about colour.

### Measured, and it works

`70-hdr-layer.sh`, on the same seat, NVIDIA, no gamescope:

```
SurfaceFormat[15]: FORMAT_A2R10G10B10_UNORM_PACK32  COLOR_SPACE_HDR10_ST2084_EXT
SurfaceFormat[19]: FORMAT_A2B10G10R10_UNORM_PACK32  COLOR_SPACE_HDR10_ST2084_EXT
[HDR Layer] Created HDR surface
```

A Wayland surface in the seat offers HDR10 with the ST2084 transfer function to
any Vulkan client that asks. The whole build is five compile steps and a
submodule; the layer sits above the driver, so the driver's own missing colour
management stops mattering.

vkcube does not choose it - it asks for `B8G8R8A8_UNORM` in `SRGB_NONLINEAR`
and always will - so `80-hdr-content.sh` brings a client that does. mpv on
`vo=gpu-next` with `--target-colorspace-hint=yes`, playing a clip generated on
the spot: a linear ramp encoded as PQ, BT.2020, ten bit, with mastering display
metadata. The layer reports what was actually negotiated:

```
[HDR Layer] Creating swapchain for id: 8
  format: VK_FORMAT_A2B10G10R10_UNORM_PACK32
  colorspace: VK_COLOR_SPACE_HDR10_ST2084_EXT
```

Against vkcube's `SRGB_NONLINEAR` on the same surface, that one line is the
whole difference. And with it streaming, at 2560x1600 at 60 Hz:

```
17:21:01  Color coding: HDR (Rec. 2020 + SMPTE 2084 PQ)
17:21:01  Color depth: 10-bit
          {"app":"Desktop","width":2560,"height":1600,"fps":60,"hdr":"true",...}
```

So the chain is closed with content that uses it, not only with a desktop
sitting inside it: an application renders HDR, sway composites it in PQ at ten
bit, screencopy hands it over, NVENC encodes it as Rec. 2020 PQ, and a client
that asked for HDR receives it.

**Generating the clip taught one thing worth keeping**: ffmpeg's `-color_trc`
and `-color_primaries` are not enough for HEVC. The file reads back as `unknown`
unless the same values also go into the bitstream through `-x265-params`. The
script checks its own output with ffprobe rather than trusting the encode.

So the gap that `60-gamescope.sh` found is real but narrow: it is gamescope's
own bar, not the compositor's, and the way past it is one small layer rather
than four protocol features in sway.

## A landmine found by accident, and it is not about HDR

Running the patched Sunshine in a seat made the mouse bleed through to the host
desktop. That is this spike's fault, and the cause is worth far more than the
spike is.

Sunshine has moved its virtual input devices to an external library,
`libvirtualhid`, and with it the names have changed and `XDG_SEAT` has gone.
`grep -rn XDG_SEAT src/` on master returns nothing. Side by side on this
machine, one seat on the packaged build and one on master:

```
vince, packaged   Mouse passthrough (vince)
                  Keyboard passthrough (vince)

joser, master     libvirtualhid Mouse
                  libvirtualhid Keyboard
                  libvirtualhid Touchscreen
                  libvirtualhid Pen Tablet
```

Two things in Polyseat key on those names, and both break:

- **`host/72-polyseat-hide.rules`** matches `*passthrough*` and `Sunshine*`. It
  matches neither of the new names, so the fast path that strips `ID_INPUT` and
  the `uaccess` tag never fires. The broker still catches them - all four were
  `root:root 0600` when this was measured - but the rule file says itself that
  the broker closes the case "within one poll interval", and that window is
  where a client's mouse reaches the host desktop.
- **Device to seat assignment**, which `spike/m2-input-broker/broker.py` does
  through the seat tag in the device name. There is no tag now. `seat.go`
  already knows this failure by another route: it refuses a seat called `seat0`
  because that "would produce untagged devices and the broker would have nothing
  to match on". Master produces untagged devices for every seat name.

**This is waiting to happen in production.** Nothing about it is specific to the
HDR patch; it needs only a Sunshine release that carries libvirtualhid, and that
is what master is. On the next version bump, every seat's input reaches the host
desktop for as long as the broker takes to notice, and per-seat attribution
stops working altogether.

Cheap to fix, and worth fixing before it arrives: add `libvirtualhid*` to the
name list in the udev rule, and find out whether the seat tag can be restored -
if libvirtualhid takes a name, Sunshine may need to be told to pass one, which
would be a third upstream conversation.

## The two patches are not optional halves

Reasoning said that patch 1 without patch 2 would give a wrong picture: the
output would be BT.2020 PQ, screencopy would hand over PQ values, and an
unpatched Sunshine would encode them as Rec. 709 and tell the client HDR was
off.

Measured on 2026-09-05 it is worse than that, and the reason is less clear than
first written here. Removing only the patched Sunshine, leaving everything on the
compositor side in place, took the seat off the air: black, then `error -1` in
Moonlight. Turning HDR off on the output did **not** bring it back. Only the full
`99-cleanup.sh` did.

**Which single change was responsible is not established.** The tidy explanation
- an older wlgrab cannot use the ten bit buffer - does not survive the fact that
`hdr off` did not help. Cleanup also takes away the patched wlroots, the Vulkan
renderer and the capability-free sway copy, and the Vulkan renderer hands out
different buffer modifiers than gles2 did (`BLOCK_LINEAR_2D` in the log), which
an older Sunshine could fail on at import.

What is established is the practical rule, and it is the one that matters: the
two patches and the renderer change go in together or stay out together. Half of
this configuration does not stream. If only one patch is ever accepted upstream,
the other has to be carried as a fork, or `output ... hdr on` has to stay off.

## Found while running it

**Arch's sway carries `cap_sys_nice`, and that makes `LD_LIBRARY_PATH` a dead
letter.** The plan was to build the patched wlroots into `/usr/local` and point
the session at it with one environment variable, leaving the packaged library
untouched. It does not work: a binary with file capabilities runs in glibc's
secure-execution mode, and ld.so discards `LD_LIBRARY_PATH` there. The variable
was in `/proc/<pid>/environ`, systemd had done everything right, and sway kept
loading `/usr/lib/libwlroots-0.20.so` with nothing anywhere saying why.

`20-wlroots.sh` now copies sway to `/usr/local/bin/sway` and runs that. File
capabilities live in the `security.capability` extended attribute and plain `cp`
does not carry them, so the copy is unprivileged and the loader reads the
variable again. Nothing packaged is modified and one file undoes it. The price,
stated rather than hidden: this sway cannot raise its own scheduling priority.
Acceptable for measuring colour; not something the product would ship, where the
patched wlroots would simply be a rebuilt package in `/usr/lib`.

**pacman cannot install anything that touches the graphics driver.** The driver
in a seat is not a package, it is a set of files libnvidia-container mirrors in
from the host, and pacman does not know that. Arch's `cuda` depends on
`opencl-nvidia` by name, so pulling in the toolkit tries to install a real
package over injected files and the whole transaction dies:

```
opencl-nvidia: /usr/lib/libnvidia-opencl.so.1 exists in filesystem
```

This is the M3 finding again, in a corner it had not reached. `provision.go`
carries four `--assume-installed` flags for exactly this and says they belong on
every pacman call that resolves dependencies. `opencl-nvidia` is a fifth, needed
here because cuda names it directly rather than through the virtual
`opencl-driver`, so assuming the virtual one is not enough.

**nvcc will not use the seat's compiler.** Arch's cuda says which gcc it can
work with by depending on a versioned one - today `gcc15`, while the seat's `cc`
is gcc 16 - and nvcc refuses anything newer. `40-sunshine.sh` reads the version
out of cuda's dependency rather than pinning it, which is what LizardByte's own
PKGBUILD does and means it does not go stale on the next toolkit.

**The log never contains the string the check was looking for.** Two mistakes at
once, and both were invisible until a seat ran: wlroots has a "Choosing primary
buffer format" message and on this path it is not the one that appears, and the
format is printed as a DRM fourcc - `XR24`, `XR30` - so a check for the string
`2101010` can never match anything. What the session really logs is sway's own
`render_format:` and, under it, `Allocated 1920x1080 GBM buffer with format XR24`
and `vulkan create_render_buffer: XR24`. `lib.sh` has `formatline` and
`formatdepth` now, and they match on the fourcc names.

This was the check that decides whether HDR reached the swapchain, so it failing
open - warning and carrying on - was the worst shape it could have. It fails
closed now.

**A check that reported the opposite of what it had just printed.** The feature
comparison in `60-gamescope.sh` listed `parametric` and
`set_mastering_display_primaries` as advertised and then marked both of them
missing, two lines apart. `wayland-info` indents with tabs, the extraction ended
in `tr -d ' '`, and `tr` was only ever asked for spaces - so every name kept its
leading tabs and no comparison could match. Worth writing down not because the
bug is interesting but because of what it produced: a diagnostic that
contradicted itself on screen and would have been believed if the two halves had
been further apart.

**`set -o pipefail` and `grep -q` do not get on.** grep leaves at the first
match, the writer on the other side of the pipe dies of SIGPIPE with 141, and
pipefail makes that 141 the answer. So a pattern that matches reads as one that
does not. It only bites once the input is big enough that the writer is still
writing, which is why it appeared the moment this stopped reading a guessed
window of the log and started reading the whole invocation: 400 lines passed,
200000 failed. Every such test in these scripts is a here-string now.

## What each failure means

- **Gate 1, sway does not come back.** The Vulkan renderer does not work on this
  driver, headless, in a container. That is the end of this approach, because
  gles2 will never do output colour transforms. Worth separating from a
  *silent* fallback: wlroots drops back to gles2 quietly when Vulkan cannot be
  created, and a seat that fell back passes every later check and then fails at
  `hdr on` for a reason that points nowhere near the cause. `10-vulkan.sh`
  therefore reads the log rather than trusting the variable.
- **Gate 3, `features.hdr` is false.** That field is `output_supports_hdr()`
  itself, over sway's IPC, so it is the direct answer: either the patched
  wlroots is not the library sway loaded, which `20-wlroots.sh` checks in
  `/proc/<pid>/maps`, or the session fell back to gles2, which `10-vulkan.sh`
  checks. sway's log says which of the three checks it failed.
- **Gate 3, `features.hdr` is true but `hdr` stays false.** sway accepted the
  command and the commit did not take. That is a new failure nobody has seen,
  and the log is the only place to look.
- **Gate 3, the buffer format is still XRGB8888.** sway is compositing HDR into
  an eight bit buffer, so the values are being thrown away before anything
  downstream sees them. This is the risk nobody can rule out from reading source.
- **Gate 4, the build fails on a missing xml.** Sunshine bundles its own
  `wayland-protocols` and that copy may predate `staging/color-management`. The
  script uses the seat's system copy and checks for the file first.
- **`50-verify.sh` says SDR with a client that asked for HDR.** Read the lines
  above it. Sunshine says "falling back to SDR" when the encoder cannot do it and
  "Couldn't get display hdr metadata" when `is_hdr()` and `get_hdr_metadata()`
  disagree, which would be a bug in patch 2.

## What is not known

Three things, and none of them can be settled by reading:

1. ~~**Does sway's Vulkan renderer work on the proprietary NVIDIA driver,
   headless, inside a container?**~~ **Answered on 2026-09-05: yes.** The seat
   had run on `WLR_RENDERER=gles2` since M1, deliberately, and nobody had ever
   started this session on Vulkan. It comes up on an RTX 4080 under the
   proprietary driver in an Incus container. wlroots cannot get a high-priority
   device queue there and falls back to a regular one, which costs nothing that
   has been noticed.

   That also settles sway's third HDR condition, the one
   `check-wlroots-patch.sh` cannot reach: `features.hdr` over sway's IPC reports
   `true`, which is `output_supports_hdr()` and therefore includes
   `renderer->features.output_color_transform`.
2. **Does wlr-screencopy actually hand out the ten bit buffer?** Half answered
   on 2026-09-05. sway composites the headless output at `XB30`, which is
   XBGR2101010, so the ten bit path takes and the swapchain really is ten bit.
   Whether screencopy then hands Sunshine a buffer in that format is a separate
   question and belongs to gate four.
3. ~~**Does NVENC Main 10 come out right through the CUDA GL path with a ten bit
   RGB source?**~~ **Answered on 2026-09-05: yes.** Sunshine's own encoder probe
   settles it, and it settles more than it looks like it does. `validate_config`
   does not merely construct an encode session: it pulls an image, converts it
   and encodes a frame, then validates the SPS of the packet that comes out. So
   `Color coding: HDR (Rec. 2020 + SMPTE 2084 PQ)` and `Color depth: 10-bit` at
   startup mean a real frame went through screencopy, through the CUDA GL path
   as P010, and out of NVENC as valid HEVC Main 10.

   That also finishes the second question above: screencopy does hand over the
   ten bit buffer, because nothing downstream would have worked otherwise.

And one question that is not technical: **two rebuilt packages in every seat is
a real maintenance cost.** If the spike succeeds, the honest next step is
offering both patches upstream before wiring any of it into `provision.go`, and
deciding what a seat does in the meantime.

## Notes

- `polyseat-session` already records `SUNSHINE_CLIENT_HDR`, so
  `session.json` carries what the client asked for. Until this works, that value
  being present is the seat quietly not delivering something. Whatever comes of
  the spike, the interface should say so.
- The stream being HDR does not depend on the content being HDR. Sunshine reads
  the *output*, and gamescope maps SDR content into a PQ output at
  `--hdr-sdr-content-nits`. So `60-gamescope.sh` running vkcube proves the path
  without proving the picture.
