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

## Getting access, once

The AUR is a git server that takes pushes over SSH and nothing else. Three of
these four steps happen in a browser or in a terminal that asks questions, so
they cannot be scripted.

1. **An account**, at https://aur.archlinux.org/register.

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
5. Read what `namcap` makes of it, which is not the same as doing what it says:

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

6. Push to the AUR:

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
