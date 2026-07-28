# Host-side artifacts

Things that live on the host rather than inside a seat. The daemon will
eventually generate and maintain these; for now they are applied by hand.

| | |
|---|---|
| `70-polyseat-hide.rules` | udev rule that keeps the seats' virtual devices off the host desktop |
| `check-hardening.sh` | reports host-side exposures that no seat-side measure can close |

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

Setting unused VTs to `K_OFF` would close it, and is deliberately **not**
recommended here: it disables the real keyboard on those consoles too, which
removes the recovery path you want when the desktop is broken. Knowing about the
window is the better trade than losing the console.
