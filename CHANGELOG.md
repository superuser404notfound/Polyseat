# Changelog

A version is a git tag, and the daemon is stamped with the tag it was built
from. `polyseatd -version`, the line at the bottom of the web interface and the
first line in the journal all answer with the same string, so there is never a
question of which build is running.

Versions are `MAJOR.MINOR.PATCH`. Before 1.0 the minor number carries anything
that changes behaviour, including changes that need seats to be built again.
When that happens it is written here, because it is the one kind of update that
costs a few minutes per seat rather than a restart.

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
