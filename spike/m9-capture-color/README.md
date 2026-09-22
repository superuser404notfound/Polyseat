# M9 - a capture client asks for HDR, and gets it

**Question:** after wlroots merge request 5443, is the only thing still missing
for HDR out of a seat that the capture client cannot say what colour it wants?

**Status: yes, measured** (2026-09-19). A capture client took a frame from an
output that is in plain SDR, asked for BT.2020 with the ST2084 PQ transfer
function, and received a ten bit buffer whose pixels are PQ encoded. The patch
that made the asking possible is forty lines in one file.

This matters because it is the opposite of what M8 did, and M8 was rejected for
exactly that reason.

## What M8 got wrong, in one paragraph

M8 made the **headless backend's** output claim BT.2020 and PQ. fpoisot's
objection was that a headless output does have consumers, screencopy among
them, and that they get handed a buffer they did not ask to be HDR. Work item
4108 then removes the premise altogether by giving output capture its own
render pass. Both points were right.

The place that argument does hold is one output further in. The scene based
capture source in merge request 5443 creates a **private output of its own**,
renders the scene into it a second time, and hands the result to exactly one
consumer: the client that asked for the capture. That output has no panel and
no second consumer, so what it presents is a statement about the frames it
gives out rather than a claim about hardware. wlroots' own header says as much
about the older source, in the list of what capturing an output's raw buffers
cannot do: "No color management support."

## What was found, in the source and then on the machine

Three things, in order of how much they decide.

**The render pass already converts.** `wlr_scene_output_build_state` reads the
target output's image description through `output_pending_image_description`
and builds the transform in `scene_output_combine_color_transforms`. Nothing
had to be written for this; it is what the scene graph does for any output.

**The capture output refuses to be told.** `output_test` in
`types/ext_image_capture_source_v1/scene.c` accepts four state fields:

```c
uint32_t supported =
    WLR_OUTPUT_STATE_BACKEND_OPTIONAL |
    WLR_OUTPUT_STATE_BUFFER |
    WLR_OUTPUT_STATE_ENABLED |
    WLR_OUTPUT_STATE_MODE;
```

`WLR_OUTPUT_STATE_IMAGE_DESCRIPTION` is not among them, and neither is
`WLR_OUTPUT_STATE_RENDER_FORMAT`. So the colour cannot be set and the buffer
cannot be widened past eight bits.

**Nobody is working on it.** Every open wlroots merge request and issue was
read on 2026-09-19. There is no implementation of
`ext-image-capture-color-management-v1`, the protocol from
[wayland-protocols merge request 448](https://gitlab.freedesktop.org/wayland/wayland-protocols/-/merge_requests/448),
which is the accepted way for a capture client to ask. Its own checklist has
two implementations ticked, jay and wl-mirror, with review and member ACKs
still open.

## The patch

`patches/wlroots-capture-image-description.patch`, against merge request 5443
at the commit named in `patches/BASE`. Four changes in
`types/ext_image_capture_source_v1/scene.c` and a declaration in the header:

1. the private capture output advertises sRGB and BT.2020 primaries, and the
   sRGB, gamma 2.2 and PQ transfer functions
2. `output_test` accepts an image description and a render format
3. `source_render` applies both when one has been set
4. `wlr_ext_image_capture_source_v1_set_image_description()`, which refuses for
   any source that does not composite the scene again, and picks a ten bit
   format when the caller passes `DRM_FORMAT_INVALID` and asks for PQ

It is deliberately the smallest thing that answers the question. **It is not an
implementation of merge request 448**: there is no protocol glue, no way for a
client to reach this, and the setter is a C call the compositor makes. What it
shows is that the protocol would have somewhere to land.

## The probe

`capture-probe.c` is one process that forks. The parent is a headless wlroots
compositor with a single output, a scene holding one rect, and the two capture
globals. The child connects to the socket the parent just opened and takes one
frame through `ext-image-copy-capture-v1`.

The rect is **mid grey**, and that is the whole design. Black and white survive
almost any transfer function unchanged, so either would hide the difference.
The primaries conversion leaves a neutral alone, so the number that comes back
reports the transfer function and nothing else. In a ten bit buffer the three
possible answers are far apart:

| | value | meaning |
|---|---|---|
| nothing applied | 512 | the signal was passed through |
| linear light | 219 | converted, but not encoded |
| BT.2020 PQ | 437 | what was asked for |

Measured: **438**. The arithmetic behind 437 is the ordinary one, mid grey
through the sRGB curve to 0.214 of full, against wlroots' default SDR reference
of 203 cd/m², giving 43.5 cd/m², which PQ encodes as 0.427 of 1023. Being
within about one code value of that is the result; landing on 512 or 219 would
have meant the opposite.

Two controls, because a patch that carried nothing would look the same:

- **the same run without the patch has to fail.** It cannot even link, since
  the setter does not exist. `check-capture-color.sh` requires that.
- **the same run with only the two state flags removed has to fail.** It does:
  the capture output refuses the state and the session ends up offering no
  format at all. Everything else about the patch stays in place for that run,
  so this is the flags and not the rest.

And the raw output source, asked the same question, refuses:
`this source will not take BT.2020 PQ`. Which is correct, and is what its own
documentation says.

## Two things the machine taught

**The Vulkan renderer is not optional.** `features.output_color_transform` is
true in `render/vulkan/renderer.c` and false in both gles2 and pixman. On gles2
the ten bit buffer comes up and the pixel comes back at 127 of 255 with no
conversion at all, which looks like a working HDR path in every log and is not
one. M8 had already found that sway needs Vulkan for the same reason; this is
the same fact from the other end.

**The right ten bit format belongs to the card, not the protocol.** On the RTX
4080 here, EGL lists XR30 and XB30 alike, and `gbm_bo_create` then refuses XR30
with `Invalid argument`. XB30 works. A hardcoded `DRM_FORMAT_XRGB2101010` would
have looked like a broken patch on this machine and worked on another.

A third, smaller, and worth reporting upstream if it survives a second look:
under the **gles2** renderer the capture session advertised `ABGR2101010` as an
shm format while `wl_shm` itself advertised no ten bit format at all, so a
client that believed the session could not create the buffer. Under Vulkan both
lists agree. The two are built from different sets, render formats on one side
and shm texture formats on the other.

## What this does not show

The probe is a rect, not a game, and shm, not dmabuf. Sunshine captures over
dmabuf, and nothing here has been through Sunshine, a seat or a client. It says
one thing only, and says it firmly: the compositor half is a small patch away
once the protocol exists.

It also says nothing about whether the picture looks right. That has never been
answerable by a log and still is not.

## Procedure

```
./check-capture-color.sh
```

No seat, no container, no GPU-specific setup beyond a working Vulkan driver.
It fetches wlroots at merge request 5443, builds it twice, and runs the probe
against each. Allow ten minutes and a couple of gigabytes.

It fetches wayland-protocols and the Vulkan headers into a prefix of its own
rather than asking anybody to install them, because both were missing here.
meson and ninja it will not install; if the distribution has neither, the
script says the venv line to run first.

`KEEP_WORK=/some/dir` keeps the build tree, which is what to do when the patch
needs another round.

## Where this goes

The next step is not more measuring, it is asking. fpoisot wrote, of an
implementation of merge request 448, "When we get an implementation of that,
sure!" This is not that implementation, but it is evidence that the shape works
and that the cost is small. The question worth putting to him is whether a
wlroots implementation would be welcome and in what form, before several
hundred lines of protocol glue exist that have to be argued about afterwards.

Separately, and regardless of HDR: merge request 5149 removes
`wlr-screencopy-unstable-v1` from wlroots. Sunshine's `wlgrab` is built on it
and Polyseat's seats run `capture = wlr`. That port to
`ext-image-copy-capture-v1` is coming either way, and the colour work sits on
top of it rather than beside it.
