# Packaging

## The AUR package

[`aur/PKGBUILD`](aur/PKGBUILD) and [`aur/polyseat.install`](aur/polyseat.install)
are the source of truth. The AUR repository is a copy of those two files plus a
generated `.SRCINFO`, and nothing else: keeping the real ones here means a
change to the install message and the change that caused it live in the same
commit.

The package does what a package is allowed to do and no more:

| | |
|---|---|
| `/usr/bin/polyseatd` | the daemon, with the version stamped in from `pkgver` |
| `/usr/bin/polyseat-prepare` | `host/prepare.sh`, the half a package may not do itself |
| `/usr/lib/polyseat/*.py` | the input broker and its helpers |
| `/usr/lib/systemd/system/polyseatd.service` | with `ExecStart` rewritten to `/usr/bin` |
| `/usr/lib/udev/rules.d/72-polyseat-hide.rules` | pacman's own hooks reload udev and systemd |

It does not write `/etc/subuid`, does not run `incus admin init`, does not add
anybody to a group and does not start anything. Those are what
`polyseat-prepare` is for, and the post-install message asks for it.

## Cutting a release

1. Tag and publish the release, as
   [CONTRIBUTING.md](../CONTRIBUTING.md) describes.
2. `pkgver=` to the new version, `pkgrel=1`.
3. New checksum for the release tarball:

   ```
   curl -sL https://github.com/superuser404notfound/Polyseat/archive/refs/tags/vX.Y.Z.tar.gz | sha256sum
   ```

   `updpkgsums` does the same thing if you have `pacman-contrib`.
4. Prove it, against a virtual machine rather than against this one:

   ```
   host/test-package.sh --rebuild
   ```

   That builds the package from the current checkout with this PKGBUILD, installs
   it on a fresh Arch machine, runs `polyseat-prepare`, starts the daemon and
   removes it again. It also checks the three things the package must **not** do,
   because that is the whole reason the installer is in two halves.
5. Push to the AUR:

   ```
   git clone ssh://aur@aur.archlinux.org/polyseat.git aur-polyseat
   cp packaging/aur/PKGBUILD packaging/aur/polyseat.install aur-polyseat/
   cd aur-polyseat
   makepkg --printsrcinfo > .SRCINFO
   git commit -am "Update to X.Y.Z"
   git push
   ```

`.SRCINFO` is generated and belongs only in the AUR repository. It is not kept
here, because a copy that can disagree with the PKGBUILD beside it is worse than
no copy.

## Why there is no polyseat-git

Not for now, deliberately. `main` is where the next version is written, and a
package that follows it would put a machine other people stream from on whatever
was committed today. Anybody who wants that can clone the repository and run
`host/install.sh`, which is the same thing with the risk visible.
