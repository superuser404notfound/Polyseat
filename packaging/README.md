# Packaging

## Where it is published, and where it is not

**Not the AUR.** New accounts cannot be registered at present, so there is no
account to publish from. That blocks less than it first appears, because the AUR
distributes recipes and not packages: installing from it means building it
yourself, which is what `host/install.sh` already does and does better, since it
prepares the machine in the same run.

**Three built packages are attached to every GitHub release**, by
[`.github/workflows/package.yml`](../.github/workflows/package.yml), so the file
somebody actually wants is one command away whichever host they are on:

```
curl -LO https://github.com/superuser404notfound/Polyseat/releases/latest/download/polyseat-x86_64.pkg.tar.zst
sudo pacman -U polyseat-x86_64.pkg.tar.zst

curl -LO https://github.com/superuser404notfound/Polyseat/releases/latest/download/polyseat_amd64.deb
sudo apt install ./polyseat_amd64.deb

curl -LO https://github.com/superuser404notfound/Polyseat/releases/latest/download/polyseat.x86_64.rpm
sudo dnf install ./polyseat.x86_64.rpm
```

Two commands rather than one because the packages carry no signature, and all
three package managers are stricter about a file they fetched themselves than
about one already on disk. `pacman -U` on a URL applies `RemoteFileSigLevel`,
which is `Required` by default and looks for a `.sig` that is not there; a file
on disk applies `LocalFileSigLevel`, which Arch ships as `Optional`. Signing
them is the fix and it needs a key that is published and trusted by hand, which
is the same key a package repository would need.

`./` in front of the last two is not decoration. Both apt and dnf read a bare
name as a package to go and find, so without it they search the repositories for
something called `polyseat_amd64.deb`.

None of those URLs carries a version and none ever has to be edited, which is
why the files are published under names that carry none either:
`releases/latest/download/` is a permanent link only while the name is
permanent. Every one of the three package managers reads the name and the
version from inside the file, so the filename is free to say nothing, and
`pacman -Qip`, `dpkg -I` or `rpm -qip` on the download says which version it
turned out to be.

makepkg's own versioned name is not published beside them, and neither is
nfpm's. Extra files on a release page where any one of them would do is a
question asked of everybody who arrives, and the release itself already says
which version it is.

**Which of the three a host installs is not the visitor's problem for long.**
The daemon works out its own family in `internal/hostpkg` and its update button
offers only the file that belongs to this machine; a host whose package manager
is none of the three is told there is a newer version and is not offered a
button that could not work.

That covers installing, upgrading and removing through pacman, which is three
things the shell scripts no longer have to be the only answer to. What it does
not cover is `pacman -Syu` noticing a new version by itself. That wants a
package repository of its own, which wants a signing key and somewhere to host
it, and neither exists yet. Until then the daemon's own update check is what
notices, and it already does.

`paru -S polyseat` still finds nothing, and anything that does turn up under
that name is not this.

## The .deb and the .rpm

[`nfpm.yaml`](nfpm.yaml) describes both, and
[`build-packages.sh`](build-packages.sh) builds them: it compiles the daemon
with the same version stamp the PKGBUILD applies, rewrites the systemd unit from
`/usr/local/bin` to `/usr/bin` the way the PKGBUILD does, and runs `nfpm` twice.
`nfpm` is pinned rather than taken at `@latest`, because this tool writes the
packages a machine installs as root and which version of it ran is part of what
a release is.

It is runnable by hand on purpose. The release list below asks for the result to
be installed on a real machine before it is announced, and a package that only
CI can produce cannot be tested that way.

**One description, two formats, and a third kept separate.** The PKGBUILD is not
generated from `nfpm.yaml` and will not be: a PKGBUILD builds from source on the
machine installing it, and `nfpm.yaml` describes a binary that has already been
built. What has to agree between them is the file list, which is why that list
is written out in both and why a change to one is a change to the other.

The scriptlets are [`scripts/postinstall.sh`](scripts/postinstall.sh) and
[`scripts/postremove.sh`](scripts/postremove.sh), and they say what
`polyseat.install` says. One file each rather than two, because dpkg and rpm
have no equivalent of pacman's separate `post_install` and `post_upgrade` hooks:
both call the same script and it has to work out which happened. They say so in
two different ways — dpkg passes `configure` and the previous version, rpm
passes how many copies will exist when the transaction finishes — so both are
read.

`nvidia-container-toolkit` is deliberately not a dependency of either. On an
NVIDIA host it is required and on an AMD host it is a shim for a driver that is
not there, so it is neither always needed nor, on Debian, installable from the
distribution's own repositories at all. `polyseat-prepare` checks for it and
says where it comes from, which is the only honest place to handle it.

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
| `/usr/share/applications/polyseat.desktop` | the entry that opens the interface on this machine |
| `/usr/share/icons/hicolor/scalable/apps/polyseat.svg` | its icon, the same file both other packages ship |

It does not write `/etc/subuid`, does not run `incus admin init`, does not add
anybody to a group and does not start anything. Those are what
`polyseat-prepare` is for, and the post-install message asks for it.

## Getting access, once

The AUR is a git server that takes pushes over SSH and nothing else. Three of
these four steps happen in a browser or in a terminal that asks questions, so
they cannot be scripted.

Step one is the one that is closed. The rest are written down because they do
not change and because the day it opens is a bad day to work them out.

1. **An account**, at https://aur.archlinux.org/register. **Registration is
   closed at the moment**, which is why none of this has happened yet.

2. **A key.** The passphrase question is why this is typed rather than run for
   you: a key without one is more convenient for a push every few weeks and less
   safe if the machine is ever shared.

   ```
   ssh-keygen -t ed25519 -f ~/.ssh/aur -C aur
   printf 'Host aur.archlinux.org\n  User aur\n  IdentityFile ~/.ssh/aur\n  IdentitiesOnly yes\n' >> ~/.ssh/config
   ```

   `IdentitiesOnly` matters on a machine with several keys. Without it ssh
   offers them in its own order and the AUR refuses after the wrong few.

3. **The public half in the account**, at https://aur.archlinux.org/account
   under *SSH Public Key*, from `cat ~/.ssh/aur.pub`.

4. **Prove it works** before it matters:

   ```
   ssh aur@aur.archlinux.org help
   ```

   A list of AUR commands is right. `Permission denied (publickey)` means the
   key is not in the account yet, or `~/.ssh/config` points somewhere else.

## The first upload

The repository does not exist until the first push creates it, so git says
"warning: You appear to have cloned an empty repository" and that is the normal
case rather than a fault.

```
git clone ssh://aur@aur.archlinux.org/polyseat.git ~/aur-polyseat
cd ~/aur-polyseat
cp ~/polyseat/packaging/aur/PKGBUILD ~/polyseat/packaging/aur/polyseat.install .
makepkg --printsrcinfo > .SRCINFO
git add PKGBUILD polyseat.install .SRCINFO
git commit -m "Initial import: polyseat 0.3.0"
git push
```

## Cutting a release

1. Write the CHANGELOG entry, point the README at the new tag, commit that as
   `Release X.Y.Z`, tag it `vX.Y.Z` and push both. Then publish the GitHub
   release, which is what creates the source tarball everything below needs.
2. `pkgver=` to the new version, `pkgrel=1`.
3. New checksum for the release tarball:

   ```
   curl -sL https://github.com/superuser404notfound/Polyseat/archive/refs/tags/vX.Y.Z.tar.gz | sha256sum
   ```

   `updpkgsums` does the same thing if you have `pacman-contrib`.
4. Commit 2 and 3 as `Carry the package to X.Y.Z` and push. That commit is
   what triggers
   [`.github/workflows/package.yml`](../.github/workflows/package.yml), which
   builds the Arch package in an Arch container, the `.deb` and the `.rpm` with
   `nfpm` on the runner, and attaches all three to the release. It is triggered
   by the PKGBUILD rather than by the tag because this is the first moment
   everything agrees: the checksum in step 3 is of a tarball that only exists
   once the tag does, and both jobs read the version out of that same file.

   A wrong checksum fails that build rather than shipping a package built from
   something other than the tag it names, which is the one mistake this order
   makes easy to make.

5. Prove it, against a virtual machine rather than against this one:

   ```
   host/test-package.sh --rebuild
   ```

   That builds the package from the current checkout with this PKGBUILD, installs
   it on a fresh Arch machine, runs `polyseat-prepare`, starts the daemon and
   removes it again. It also checks the three things the package must **not** do,
   because that is the whole reason the installer is in two halves.

   **There is no equivalent for the other two yet, and that is the standing gap
   in this list.** `host/test-distro.sh` checks that `host/distro.sh` reaches
   for the right tool with the right arguments, with every package manager
   replaced by a stub, and CI runs it. What nobody has done is install the
   `.deb` on a Debian machine or the `.rpm` on a Fedora one. Until somebody
   does, those two are in the position the AMD path is in: written, reasoned
   about, and unproven. Say so when announcing a release rather than letting
   somebody find out.
6. Read what `namcap` makes of it, which is not the same as doing what it says.
   The workflow already prints both of these, so this is reading its log rather
   than running anything, unless something needs looking at more closely:

   ```
   namcap packaging/aur/PKGBUILD
   namcap /path/to/polyseat-X.Y.Z-1-x86_64.pkg.tar.zst
   ```

   Six warnings are expected and all six are the tool being wrong or being
   pedantic. It finds dependencies by reading ELF links and shebangs, so it
   calls `incus` and `bpftrace` unnecessary when they are a socket and a program
   the observer runs. It calls `broker.py`'s imports of `uhid_observer` and
   `device_owner` uninstalled dependencies when they are two files beside it in
   the same package. And it notes that `bash` and `glibc` are satisfied through
   something else, which they are, by `base`. Anything beyond those six is
   worth reading.

7. Push to the AUR, on the day there is an account to push from:

   ```
   git clone ssh://aur@aur.archlinux.org/polyseat.git aur-polyseat
   cp packaging/aur/PKGBUILD packaging/aur/polyseat.install aur-polyseat/
   cd aur-polyseat
   makepkg --printsrcinfo > .SRCINFO
   git commit -am "Update to X.Y.Z"
   git push
   ```

   The AUR checks server-side that `.SRCINFO` agrees with the PKGBUILD beside
   it, and rejects the push when it does not.

`.SRCINFO` is generated and belongs only in the AUR repository. It is not kept
here, because a copy that can disagree with the PKGBUILD beside it is worse than
no copy.

## Why there is no polyseat-git

Not for now, deliberately. `main` is where the next version is written, and a
package that follows it would put a machine other people stream from on whatever
was committed today. Anybody who wants that can clone the repository and run
`host/install.sh`, which is the same thing with the risk visible.
