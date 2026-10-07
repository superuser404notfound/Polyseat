# Sunshine, as a seat gets it

A seat does not run the Sunshine package LizardByte publish. It runs the same
release, built here with one patch, as a package of its own:

    sunshine-2026.914.233613-1.1-x86_64.pkg.tar.zst

`provision.go` names it three ways, `SunshinePin`, `SunshineBuild` and
`sunshinePackageSHA256`, fetches it from a release of this repository called
`sunshine-<pin>-<build>` and refuses anything whose checksum is not the one in
the source.

## What the patch does

Sunshine's wlr capture sleeps on a clock of its own and then asks sway for a
frame with a plain `copy`. That request makes sway commit at a time Sunshine
chose, and the game's next frame and that request then compete for the same
commit. Depending on how the two clocks stand, the encoder is handed one
picture twice and never sees another.

Nothing reports it. Measured on 2026-10-07 on a wired 4K60 stream out of a
seat: 60.00 frames a second on the wire, not a packet lost, every statistic on
the client clean, and 5 to 11 frames in a hundred a repeat of the one before.
It shows as microstutter and as nothing else.

`wlgrab-event-driven.patch` makes the capture ask with `copy_with_damage`
instead, which sway answers with the next commit that changed the picture:

- only on an output that refreshes at least half again as fast as the stream.
  On one running at the stream's own rate nothing changes, because there the
  commits a paced copy forces are part of what keeps the output's clients on
  time, and taking them away made it worse;
- the first frame is still a plain copy, so a picture that is not moving has
  something to show;
- a request that outlived the one second timeout is kept rather than made
  again;
- every captured frame moves the earliest time of the next request on by one
  frame interval, which holds a game rendering faster than the stream to the
  stream's rate.

## Why it needs the output at twice the client's rate

The first of those points is why `polyseat-resize` sets the seat's output to
twice what the client asked for. The two were measured together, on the same
seat, game and client:

| Sunshine | output | frames a second | repeats | gaps over 25 ms |
|---|---|---|---|---|
| LizardByte's | 60 Hz | 60.00 | 5 to 11 % | none |
| LizardByte's | 120 Hz | 60.00 | 0 % or 10.7 %, by connection | none |
| patched, forced on | 60 Hz | 54 to 57 | 0 to 7 % | 4 to 5 a second |
| patched | 120 Hz | 60.00 | 0.0 % | none |

The last row held over seven connections with the clocks reshuffled in between.
Rates that are not a whole multiple are worse than either: 90 Hz gave 43 frames
a second, 240 Hz gave 57.

## Building it

    packaging/sunshine/build.sh <container>

in any running Arch container Incus knows, a seat for instance. It installs the
build dependencies there, among them the CUDA toolkit, runs `makepkg` on the
`PKGBUILD` next to it, pulls the package out and prints its checksum. The
toolkit is taken out again if the script put it in. Half an hour, most of it
compiling.

Four compilers at a time, on purpose. See the comment in the script.

The CUDA toolkit is needed to build and not to run: Sunshine converts the
captured frame for NVENC in a CUDA kernel, and at run time it loads only
`libcuda.so.1`, which comes with the driver. A seat never has the toolkit.

## Moving to a new Sunshine release

1. Change `_commit`, `pkgver` and, if the patch still applies, nothing else in
   `PKGBUILD`. `pkgrel` goes back to `1.1`.
2. Build, and publish the package on a release named
   `sunshine-<pkgver>-<pkgrel>`.
3. Change `SunshinePin`, `SunshineBuild` and `sunshinePackageSHA256` in
   `internal/seat/provision.go`. A test fails if the three disagree with the
   recipe.
4. Everything `SunshinePin`'s comment already asks for: pair a client against
   it, check that input stops at the seat boundary.

Upstream this is LizardByte/Sunshine#5751, with #5748 open for the same change
in a simpler form. When a release carries it, the pin moves there, the seats go
back to LizardByte's package, and this directory goes away. The doubled output
rate stays either way.
