# Faugus, as a seat gets it

Every seat has Faugus Launcher beside Lutris, built from the AUR recipe of its
2.5.6 release with the source pinned:

    faugus-launcher-2.5.6-1-any.pkg.tar.zst

Faugus is in no Arch repository, and a seat has neither a compiler nor an AUR
helper, so the package is built once with `build.sh` in any running Arch
container and published on a release of this repository called
`faugus-<version>-<build>`. `provision.go` names it with `FaugusVersion`,
`FaugusBuild` and `faugusPackageSHA256` and refuses anything whose checksum is
not the one in the source. Its dependencies all come from Arch's own
repositories when pacman installs it.

It keeps the upstream name, so a newer Faugus the player builds from the AUR
replaces it, and `stepFaugus` leaves a newer one alone rather than going back.

## Moving it to a new release

1. `pkgver` in `PKGBUILD`, and the checksum of the new tag's tarball:
   `curl -sL https://github.com/Faugus/faugus-launcher/archive/refs/tags/<v>.tar.gz | sha256sum`
2. `packaging/faugus/build.sh <container>`, which prints the package's checksum.
3. `FaugusVersion` and `faugusPackageSHA256` in `internal/seat/provision.go`.
4. `gh release create faugus-<v>-1 packaging/faugus/faugus-launcher-<v>-1-any.pkg.tar.zst`
   before a Polyseat release names it, or every seat built in between fails at
   this step.

`TestTheFaugusPackageIsTheOneTheRecipeBuilds` holds the first and third to each
other.

## What the daemon does with it

See `internal/seat/faugusshare.go` and the Faugus part of
`docs/architecture.md`: Moonlight cards from `games.json`, and entries carried
between the members like Lutris's.
