# Changelog

A version is a git tag, and the daemon is stamped with the tag it was built
from. `polyseatd -version`, the line at the bottom of the web interface and the
first line in the journal all answer with the same string, so there is never a
question of which build is running.

Versions are `MAJOR.MINOR.PATCH`. Before 1.0 the minor number carries anything
that changes behaviour, including changes that need seats to be built again.
When that happens it is written here, because it is the one kind of update that
costs a few minutes per seat rather than a restart.

## Unreleased

- **A seat record on disk says which layout it is in**, and a build refuses one
  it does not understand rather than reading it anyway. Records written before
  this field are the same layout by definition and keep working untouched. It is
  here now because a field like this is worth nothing added later: it cannot say
  anything about the records that came before it.
- **The interface says so on an AMD machine**, since that path has never been
  run on real hardware by its author and whoever opens the page has quite
  possibly not read the readme.
- `CONTRIBUTING.md`, `SECURITY.md` and issue forms that ask for
  `sudo polyseatd -report` first. Security problems have a private channel now
  rather than a public issue.
- **Polyseat installs from the AUR.** `packaging/aur/PKGBUILD` builds it, and
  the installer is now two halves because only one of them can be packaged: an
  Arch package may place files and may not initialise Incus, write to
  `/etc/subuid` or add an account to a group. That half is `host/prepare.sh`,
  which the package installs as `polyseat-prepare` and asks for after
  installing. The checkout install runs it for you and is otherwise unchanged.
- The daemon finds its input helpers under `/usr/local/lib` or `/usr/lib`
  without being told which, since the same binary is installed both ways.
- `host/test-package.sh` builds the package and installs it on a fresh virtual
  machine, the way `host/test-install.sh` does for the checkout. It also checks
  the three things a package must not do.

## 0.2.0

Noticing that there is a newer version, and a way to take it.

Seats are untouched by this one. Nothing in it changes how a seat is built, so
updating to it is a rebuild and a restart of the daemon and no more. Anybody on
0.1.0 has to do that update by hand, since `host/update.sh` arrives with this
version rather than before it:

```
git fetch --tags && git checkout v0.2.0 && sudo host/install.sh
```

- **The interface says when a newer Polyseat has been published.** One request
  to GitHub every six hours, a line at the top when there is something to say,
  and nothing else: it never downloads and never installs. It sends nothing
  about the machine, and `"update_check": false` in the configuration turns it
  off, after which no request is made at all.
- Only a build sitting exactly on a release tag is told anything. A build from
  an untagged commit cannot be compared with a release, and being told to
  "update" to something older than what is running is worse than silence.
- **`host/update.sh`** does the update: fetch, check out the newest release, run
  the installer. It refuses a checkout with uncommitted work in it rather than
  stashing or forcing, and it waits for a moment when nobody is streaming,
  because installing restarts the daemon and that takes every seat's input
  broker with it. `--check` looks without changing anything, `--now` skips the
  waiting, `--tag` goes to a particular release.
- Doing it by hand is unchanged and still documented.

## 0.1.0

The first release, and the state the project was in when it stopped being an
experiment. Everything here has been run on the machine it was written on: one
Arch host with an RTX 4080, seats streaming 4K to Apple TVs over Moonlight,
installed and uninstalled from scratch once to make sure the instructions are
the instructions.

### Seats

- One click takes a seat from nothing to a running session: an Incus system
  container with headless Sway, its own Sunshine, its own PipeWire and its own
  Steam account, on a macvlan of its own so it is a host on the LAN and can use
  the standard Sunshine ports.
- The NVIDIA userspace that the container toolkit leaves incomplete is repaired
  during provisioning. Without it a seat comes up, streams and looks healthy
  while encoding on the CPU.
- AMD is built differently and more simply: Mesa and `vulkan-radeon` are
  ordinary packages inside the seat, nothing is injected across the container
  boundary, and Sunshine encodes with VA-API. **Never run on real hardware.**
  What was verified and what was not is in [`docs/amd.md`](docs/amd.md).
- Seats built by an older recipe are recognised as such. The interface names
  them and offers one button that brings every one of them forward.
- Pairing happens in the Polyseat interface for every seat, rather than in one
  Sunshine page per seat.

### Input

- Each client's keyboard, mouse and gamepad reach that client's session and
  nothing else, and the host desktop sees none of them.
- Ownership is decided structurally, from what the kernel says created a
  device, not from the name the device claims. The seat log says for every
  device which of the two it was.
- A gamepad is enough to use a seat: an on-screen keyboard, and a pointer on the
  sticks that turns itself on when the desktop is in front and hands the
  controller back to a fullscreen game. Its speed is a slider that takes effect
  while somebody is holding the pad.

### The shared library

- A game installed once is playable in every seat without being downloaded
  again. Each seat keeps its own fully writable copy and the copies share their
  blocks on disk through reflinks. Taking this machine's 69 GB library into the
  pool took 0.8 seconds and 432 KB.
- The pool is the seat's only Steam library, so there is nothing to choose in
  the install dialog and nothing to get wrong.
- The host's own Steam library is a member of the pool, both as a source and as
  a destination.
- Launchers other than Steam use `shared/`, one folder per game, which works for
  Heroic, Lutris and Bottles alike.
- Requires a filesystem that can share blocks. btrfs and XFS with `reflink=1`
  can, ext4 cannot. The daemon finds out by cloning a block rather than by
  trusting the filesystem's name, and says plainly when the answer is no. Seats
  work either way.

### The desktop in a seat

- Connecting lands on a desktop with a launcher, a bar and a file manager.
- Moonlight's app list and the seat's own launcher are generated from the same
  scan of what is really installed, with box art, so a game is in both menus
  without anybody making a shortcut.
- Software goes in from either end without a password and without root: from
  inside the seat, or from the Polyseat interface with a progress bar. AppImages
  count, which matters because many emulators are published no other way.
- Every seat carries Proton CachyOS beside Valve's own, from the project's
  GitHub releases rather than from a package repository, set as the default
  compatibility tool. It updates itself and waits for a seat that is neither
  streaming nor holding the files open.

### Picture and latency

- The seat's output is virtual, so it becomes the size and refresh rate each
  client asks for.
- The framerate is capped from outside instead of by turning vsync on: games
  stay uncapped and pay no vsync latency. Measured in a seat, 14866 fps uncapped
  becomes 60.00 fps at 0.03 ms of frametime jitter, against 0.40 ms for vsync.
- Measured from an Apple TV over wifi, one seat, 4K HEVC at 60: host processing
  latency 3.9 ms average, no frames dropped by the network.

### Host

- `host/install.sh` sets up a fresh machine: packages, the idmap range every
  container start needs, Incus initialised if nobody has, the daemon, the input
  helpers, the udev rule and one systemd unit. Tested against a fresh VM by
  `host/test-install.sh`.
- `--uninstall` leaves the seats alone, `--purge` takes them along and asks
  first, `--library` also removes the shared games. `host/reset-machine.sh` puts
  the machine back the way it was.
- `host/lan-bridge.sh` turns the uplink into a bridge, which is what local
  multiplayer between the host and a seat needs. It rolls back completely on any
  failure, because the first version of it took this machine off the network.
- Whether a particular seat can reach the host is a checkbox on its card.
- The interface is password protected, TLS only, and the first password is
  chosen by whoever opens the page first rather than generated into a log.

### Known limits

- **Arch based hosts only.** The installer queries pacman rather than pretending
  to be portable.
- **The AMD path has never run on real hardware.**
- **A host NVIDIA driver update needs seats to be provisioned again**, or
  Sunshine in them falls back to the software encoder. This does not apply to
  AMD, where the driver is a package inside the seat.
- **Nothing checks for a newer Polyseat.** Updating is checking out the tag and
  running the installer again.
- **A deleted seat's game directory stays on disk** and nothing in the interface
  shows it. That is deliberate, so nobody's games vanish quietly, but it means
  the space has to be found by hand.
- **Licences are not shared and cannot be.** Files being present does not mean a
  seat's Steam account may run them.
