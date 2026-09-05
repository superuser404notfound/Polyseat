# Sunshine pull request

**Submitted 2026-09-05:** https://github.com/LizardByte/Sunshine/pull/5615

Open, four files, 306 lines added, no deletions, mergeable. Branch
`wlgrab-hdr` on the fork, based on `fa462d2`.

**Patch:** `../patches/sunshine-wlgrab-hdr.patch`, written against `4cb15e9`,
applies to `fa462d2` unchanged. `check-sunshine-patch.sh` was re-run with
`SUNSHINE_COMMIT=fa462d2` before submitting, so what the pull request claims
about compiling was measured against the commit it is based on rather than the
one it was written against.

**Not an issue.** Sunshine sets `blank_issues_enabled: false` and ships only a
bug-report template; its `config.yml` routes feature requests to the LizardByte
discussions. This is a change with working code behind it, so a pull request is
the right door.

## Review round one, 2026-09-05

No human review yet. SonarCloud's quality gate failed with 26 findings, and the
split is worth writing down because it will recur for anything that talks to a C
protocol:

| rule | n | what | done |
|---|---|---|---|
| `cpp:S1186` | 8 | empty method bodies | explained inside each body |
| `cpp:S5945` | 2 | C-style arrays | `std::array` |
| `cpp:S5008` | 14 | `void *` | `NOSONAR`, it is the listener ABI |
| `cpp:S107` | 2 | ten parameters | `NOSONAR`, it is the event's shape |

Ten were fair. The other sixteen are what a Wayland listener is: the first
parameter of every callback is `void *data` by definition, and `primaries`
delivers eight coordinates plus the proxy and the user pointer. The tree already
uses `// NOSONAR(cpp:SXXXX): reason` for exactly this kind of thing, so that is
what was used.

**Nothing re-ran.** All four workflows on the new head sit at
`action_required`: a first-time contributor's pull request does not run CI until
a maintainer approves it. So SonarCloud has not re-analysed, the 26 findings
still visible through its API are the first analysis of the first commit, and
Read the Docs times out waiting for check runs that cannot appear. Until someone
at LizardByte clicks approve, the local checks are the only evidence the fixes
work.

One detail worth knowing if this recurs: `NOSONAR(rule1,rule2)` with a comma
list is documented for Python and not for C++. The bare `// NOSONAR` suppresses
every issue on its line under any reading, so the two lines that carry both
`cpp:S107` and `cpp:S5008` use that form with the rule keys named in the prose.
The twelve single-rule lines keep the parenthesised form the tree already uses.

The two Read the Docs failures are not from this change. Both builds end in
`Timeout waiting for check runs` with `Check runs count: 0`, which is what a
fork's pull request does before a maintainer approves its workflows.

After the revision: still compiles at `-Wall -Werror` against wayland-protocols
1.41 and 1.49, the conversion check still lands on 35400,14600, and
`clang-format --dry-run -Werror` is clean on all three files. The patch in
`../patches/` was regenerated to match and is now pinned against `fa462d2`.

## Where it stands, 2026-09-05

Everything that can go green has:

| check | |
|---|---|
| SonarCloud quality gate | OK, 0 new issues |
| Semantic PR | pass |
| Read the Docs, `sunshinestream` | pass |
| Read the Docs, `lizardbyte-gh-pages-sunshine` | fail, and not ours |
| CI, common lint, CodeQL, Build GH-Pages | `action_required` |

The four workflows have never run. GitHub does not run them for a first-time
contributor until a maintainer approves, so there is no build, no lint and no
CodeQL result on this branch and nothing anyone can do about it from this side.

The second Read the Docs project is the same thing wearing a different hat. Its
build script waits for GitHub check runs and gives up after sixty tries:

```
Checking check runs: 60
Check runs count: 0
Timeout waiting for check runs
```

Zero doxygen errors in that log. It is waiting on the check runs the unapproved
workflows would have produced. One approval turns both of these green.

Two corrections worth keeping, because both were confidently wrong:

- **SonarCloud does not run inside the CI workflow.** It analyses through its own
  GitHub app and re-ran on every push while the workflows sat unapproved. The
  claim that it could not re-analyse was wrong.
- **The Read the Docs failures were ours after all.** The first was the timeout
  described below; the second got further and found six real Doxygen errors, all
  introduced by this change. A single comment block above five methods documents
  only the first of them, and a `//` line above a member is not documentation at
  all where `///<` after it is. Sunshine builds its docs with warnings as errors,
  so that is a hard failure. Fixed, and the build passes now.

## Title

```
feat(linux/wlgrab): report HDR from the output's colour-management image description
```

## Description

### What this fixes

`platform/common.h` gives `display_t::is_hdr()` a `false` default, and it is
overridden in two places: `kmsgrab.cpp`, from the connector's
`HDR_OUTPUT_METADATA` property, and `pipewire.cpp`, from the SPA colorimetry.
`wlgrab.cpp` overrides neither.

`colorspace_from_client_config()` is then decisive:

```cpp
if (config.dynamicRange > 0 && hdr_display) {
  colorspace.colorspace = colorspace_e::bt2020;
} else {
  ...
}
```

So over `capture = wlr`, a client that asks for HDR gets a 10-bit encode of
Rec. 709 and is correctly told HDR is off. That is right today, because the
backend has no way to know better. It does not have to stay that way:
colour-management-v1 publishes the output's whole image description to any
client that asks, it carries more than the DRM property does, and it is
available on a virtual output exactly as it is on a physical one.

### What it does

- Binds `wp_color_manager_v1`, deliberately at version 1: everything read here
  is in the first version, and a later one only adds events that would need
  handlers.
- Reads the captured output's image description once, when the capture is set
  up. That is also every moment at which the answer could have changed, since
  Sunshine rebuilds the encode session on reconnect and on reinit.
- Implements `is_hdr()` as BT.2020 with the ST2084 PQ transfer function, and
  `get_hdr_metadata()` from the same description.

It is the job `pipewire.cpp` already does with the SPA colorimetry, over the
Wayland protocol instead.

The unit conversions are the fiddly part and are commented where they happen:
the protocol carries CIE 1931 coordinates multiplied by a million and
`SS_HDR_METADATA` carries them multiplied by fifty thousand, while the
luminances already agree - minimum in ten-thousandths of a nit, the rest whole
nits.

### Fallback

A compositor without the protocol, or one that will not describe the output,
leaves the colour unknown, which reads as SDR everywhere it is used. That is
what every wlroots compositor did before this existed, so nothing regresses.

### Tested

Compiles at `-Wall -Werror` against protocol headers generated from
wayland-protocols 1.41 and 1.49, which is version 1 and version 3 of
`wp_color_manager_v1`; the designated initialisers are there so it survives
both. The metadata conversion is checked against BT.2020's red primary, 0.708
and 0.292, which must come out as 35400 and 14600.

On hardware: a headless sway 1.12 session on the Vulkan renderer, in an Incus
container, on an RTX 4080 under the proprietary driver, streaming to Moonlight
with HDR enabled on the client.

```
Info: Output colour: primaries 6, transfer function 11 (HDR)
Info: Color coding: HDR (Rec. 2020 + SMPTE 2084 PQ)
Info: Color depth: 10-bit
```

The picture is correct, checked by eye rather than only in the log. The output
was at 2250x1206 at 120 Hz, adopted from the client mid-stream, so HDR survives
a mode change.

### One dependency worth stating

This reports what the compositor says. For a headless output to be able to say
anything, wlroots has to admit that a virtual output can present BT.2020 and PQ,
which it does not today - the capability is read from a monitor's EDID and a
headless output has no monitor. That is a ten-line change and is going to
wlroots separately. On a compositor whose output can already be in HDR, this
change stands on its own.

## Steps

```
gh repo fork LizardByte/Sunshine --clone --remote=false
cd Sunshine && git checkout -b wlgrab-hdr
patch -p1 < .../patches/sunshine-wlgrab-hdr.patch
git commit -a -m "feat(linux/wlgrab): report HDR from the output's colour-management image description"
git push -u origin wlgrab-hdr
gh pr create --repo LizardByte/Sunshine --title ... --body-file ...
```
