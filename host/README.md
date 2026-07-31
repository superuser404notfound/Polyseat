# Host-side artifacts

Things that live on the host rather than inside a seat. `install.sh` puts them
where they belong; the rest are run by hand, when you want them.

| | |
|---|---|
| `install.sh` | sets the machine up, builds and installs the daemon, and restarts it if it was already running. `--uninstall`, `--purge` and `--purge --library` take it back out |
| `update.sh` | moves the checkout to the newest release and hands over to `install.sh`. Refuses a checkout with uncommitted work, waits for nobody to be streaming. `--check`, `--now`, `--tag`, `--yes` |
| `lan-bridge.sh` | turns the uplink into a bridge, which is what local multiplayer between the host and a seat needs. `--undo` puts it back |
| `reset-machine.sh` | puts the machine back the way it was before Polyseat, keeping the game library |
| `check-hardening.sh` | reports host-side exposures that no seat-side measure can close |
| (`polyseatd -report`) | not in this directory, but this is where people look: one description of the whole installation, for a bug report. Runs without the daemon |
| `test-install.sh` | runs `install.sh` against a fresh VM, which is the only way to test the parts that only a fresh machine reaches |
| `test-gpu-detect.sh` | checks the card detection against made up `/dev/dri` layouts |
| `72-polyseat-hide.rules` | udev rule that keeps the seats' virtual devices off the host desktop, input and raw HID alike |
| `polyseatd.service` | the one unit. The broker and observer units it replaced are removed by `install.sh` |

## Why the udev rule is not optional

The seats' virtual devices are created in the host kernel and appear there, no
matter how the attribution works. Without this rule the desktop user gets an ACL
on them through the `uaccess` tag, and applications that open evdev nodes
directly, which is what games and Steam do, can read them. That is how a seat's
gamepad ended up readable on the host once already.

## The exposure that stays

Virtual keyboards are attached to the kernel's VT and sysrq handlers exactly
like a physical keyboard, and there is no per-device way to prevent that.
`/sys/class/input/inputN/inhibited` exists but inhibits the device for *all*
handlers, including the seat's own, so it is not usable here.

While a graphical session holds the active VT its keyboard mode is `K_OFF`,
which also blocks VT switching, so a client cannot reach a text console by
itself. **The window that stays open is the host switching to a text console by
hand while a seat is streaming.** From then on that client types along, and with
`getty@.service` enabled there is a login prompt waiting there.

Three ways to close it were weighed, and none is taken today:

* **`K_OFF` on the free VTs.** Closes it hardest, and locks you out: on a K_OFF
  console the kernel ignores `Ctrl+Alt+F2` as well, so you cannot get back to
  the desktop. Only viable with a watchdog that undoes it, and a crash of that
  watchdog strands you.
* **Masking the gettys**, so a free console has no shell to type into. Costs the
  local text login, which is precisely the recovery path you want on a machine
  under active development.
* **SSH as the replacement recovery path**, which would make masking acceptable.
  Not today: opening a network service is a larger hole than the console while
  the account password is weak.

So the trade taken is: leave the consoles usable, and **detect the state instead
of describing it**. `check-hardening.sh` reports when the exposure is actually
open, meaning a text console holds the active VT while seat devices exist.

**Revisit this when a guest gets a seat.** Then the client is no longer somebody
you trust and the arithmetic changes. The order would be: change the password,
set up SSH with keys, then mask the gettys.
