# wlroots merge request

**Submitted 2026-09-05:**
https://gitlab.freedesktop.org/wlroots/wlroots/-/merge_requests/5453

**Patch:** `../patches/wlroots-headless-hdr.patch`, against tag `0.20.2`,
applies to `master` unchanged and was verified there, probe and all.

## What happened when it was filed

**The description below is not on the merge request.** GitLab's spam filter
rejected it, twice, and rejected the same text again as a comment:

    Dein Merge Request wurde als Spam erkannt.

freedesktop.org restricts new accounts because of spam, and this account was two
hours old. It is content-sensitive rather than a block on the account: a two
sentence description went through immediately, so the merge request exists and
is open. Retrying was stopped there, because repeated hits against a spam filter
are more likely to harm an account's standing than to get text posted.

That leaves the merge request carrying a short description and the whole
argument in the commit message, which is where wlroots' own CONTRIBUTING says it
belongs anyway: "anything you might put into the merge request description on
GitLab is probably fair game for going into the extended commit message as
well."

**Still to do by hand,** once the account is no longer new or has been given
full permissions through https://www.freedesktop.org/wiki/AccountRequests/ :
paste the description below onto the merge request, or post it as a comment.

## Commit message

```
backend/headless: advertise BT.2020 primaries and the PQ transfer function

A headless output has no panel behind it. Whatever the compositor
composites is what the consumer of the buffer receives, and that
consumer is a screencopy client or an encoder rather than a cable, so
unlike a DRM connector this output cannot be wrong about what it is able
to present.

supported_primaries and supported_transfer_functions are currently set in
exactly one place, backend/drm/util.c, out of the monitor's EDID. The
headless backend sets neither and does not accept
WLR_OUTPUT_STATE_IMAGE_DESCRIPTION, so wlr_output_state_set_image_description()
rejects every HDR image description and a compositor cannot put a virtual
output into HDR at all. sway reports it as "BT2020 primaries not supported
by output".

That blocks HDR for every headless use of wlroots: streaming a seat with
no monitor attached, a virtual output for remote desktop, and rendering
to a file.
```

## Merge request description

The capability fields exist so that a compositor does not ask a display for
something it cannot show. A headless output is not a display: there is nothing
behind it that could disagree, and the buffer goes to whoever asked for it.
Advertising BT.2020 and PQ there is not a claim about hardware, it is the
statement that the output presents what it is handed.

Without it, HDR on a headless output is impossible rather than merely absent.
`output_supports_hdr()` in sway asks the output for BT.2020 primaries and the PQ
transfer function before it asks the renderer for anything, so the first check
fails and nothing further is attempted.

### Measured

A short probe that creates a headless output and asks it the two questions
sway's `output_supports_hdr()` asks, then hands it an HDR output state:

```
upstream 0.20.2   output HEADLESS-1: primaries 0x0, transfer functions 0x0
                    BT.2020 primaries:  no
                    ST2084 PQ transfer: no
                    the output accepts an HDR state: no

with this MR      output HEADLESS-1: primaries 0x3, transfer functions 0xb
                    BT.2020 primaries:  yes
                    ST2084 PQ transfer: yes
                    the output accepts an HDR state: yes
```

The probe is ~50 lines and can be attached if it is wanted; it links against the
built library and needs no hardware.

### End to end

With this and one change in Sunshine's screencopy capture backend, a headless
sway session streams HDR to a Moonlight client: sway 1.12 on the Vulkan renderer
with `output HEADLESS-1 hdr on`, compositing at XBGR2101010, captured through
wlr-screencopy and encoded as HEVC Main 10 in Rec. 2020 with the PQ transfer
function. Measured on an RTX 4080 under the proprietary driver, in a container,
with the picture confirmed by eye.

### Shape

Written as an unconditional property of the headless output because that is what
it is. If you would rather it were opt-in - a `wlr_headless_output_set_*` call,
or a flag on `wlr_headless_add_output()` - that is an easy change and I am happy
to make it.

The `WLR_OUTPUT_STATE_IMAGE_DESCRIPTION` line is needed as well as the two
fields: without it `output_test()` rejects the state field before
`output_basic_test()` ever looks at the primaries.

## Steps

```
git clone https://gitlab.freedesktop.org/wlroots/wlroots.git
cd wlroots
git checkout -b headless-hdr
patch -p1 < .../patches/wlroots-headless-hdr.patch
git commit -a -F <the commit message above>
git push <your fork> headless-hdr
```
