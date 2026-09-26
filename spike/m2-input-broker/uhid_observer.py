#!/usr/bin/env python3
"""uhid-observer.py - records which container created each uhid device.

uhid offers no counterpart to uinput's `UI_GET_SYSNAME`, in fact no ioctls at
all, so a descriptor cannot be asked what it created. The broker's fallback was
to correlate by the ordering of descriptor appearances, which works but is a
heuristic: a determined attacker could race it.

This closes that gap without stepping into the data path. A kprobe on
`uhid_dev_create2`, the kernel function that actually creates the device, fires
with the calling process as its context. The creator is then a fact reported by
the kernel at the moment of creation, not something inferred afterwards.

The alternative would have been a CUSE proxy mediating `/dev/uhid`, which gives
the same answer but sits between the application and the kernel: a bug there
stops gamepads from working at all. Observing costs nothing if it fails. If the
kprobe cannot be attached, the broker simply falls back to its heuristic.

One uhid device can produce several input devices. A DualSense produces three,
the pad plus "Motion Sensors" and "Touchpad". Ownership is therefore recorded
per HID device, and every input device below it inherits it.

Writes `{"0003:054C:0CE6.001C": "seat2", ...}` to the state file and keeps it
current, with null for a device the host made. Beside it, once the probe is
live, `{"pid": ..., "horizon": ...}`: which process is watching, and the newest
HID device that was already there when it started. The udev rule reads both to
tell an entry that is on its way from one that will never come. Needs root.

    sudo ./uhid-observer.py
"""

import json
import os
import re
import signal
import subprocess
import sys
import threading
import time

STATE = "/run/polyseat/uhid-owners.json"

# Whether an observer is watching, and since which device. Read by
# device_owner.py, which names it UHID_OBSERVER.
OBSERVER_STATE = "/run/polyseat/uhid-observer.json"
HID_DEVICES = "/sys/bus/hid/devices"

# bpftrace is used rather than a compiled BPF object so that this stays
# readable and has no build step. The probe is the only thing that matters.
PROBE = 'kprobe:uhid_dev_create2 { printf("create %d\\n", pid); }'

# What bpftrace says when the running kernel has no such symbol. Worth telling
# apart from every other way this fails, because it is the only one that will
# still be true in thirty seconds. uhid is a module on most kernels and its
# symbols do not exist until it is loaded, and nothing loads it early: /dev/uhid
# is a static node from modules.devname, and the module is autoloaded when
# something first opens it, which is the first seat that runs a gamepad. So the
# node is there, everything looks installed, and the probe has nothing to attach
# to.
NO_SYMBOL = "No matches for kprobe"

# Told to the supervisor as an exit code rather than left for it to read out of
# the log. It stops restarting on this one and says so once. Kept in step with
# observerCannotAttach in internal/seat/manager.go.
EXIT_CANNOT_ATTACH = 3

CGROUP_RE = re.compile(r"(?:lxc|incus)\.payload\.([A-Za-z0-9_.-]+)")


def container_of(pid):
    """Container name from the process's cgroup, None when it runs on the host."""
    try:
        with open(f"/proc/{pid}/cgroup") as fh:
            match = CGROUP_RE.search(fh.read())
    except OSError:
        return None
    return match.group(1) if match else None


def hid_devices(root=HID_DEVICES):
    try:
        return set(os.listdir(root))
    except OSError:
        return set()


def uhid_devices(root=HID_DEVICES):
    """The HID devices that came through uhid, by where their link points.

    Only these can be the answer to a uhid creation. The first version of this
    diffed every HID device, so a USB mouse plugged in at the moment a seat made
    a gamepad was written down as that seat's.
    """
    found = set()
    for hid in hid_devices(root):
        try:
            target = os.readlink(os.path.join(root, hid))
        except OSError:
            continue
        if "/uhid/" in target:
            found.add(hid)
    return found


def instance_of(hid):
    """The kernel's running number for a HID device, the part after the dot.

    hid_add_device takes it from one counter for every HID device on every bus
    and never hands one out twice, so it orders devices by age.
    """
    try:
        return int(hid.rsplit(".", 1)[1], 16)
    except (IndexError, ValueError):
        return -1


# What bpftrace prints once the probe is live: "Attaching 1 probe..." up to
# 0.2x, "Attached 1 probe" in 0.26. Not "Attachment failed for all probes".
ATTACHED_RE = re.compile(r"^Attach(?:ing|ed) \d+ probes?\b")


class Observer:
    def __init__(self, state_path=STATE, observer_path=OBSERVER_STATE,
                 hid_root=HID_DEVICES):
        self.state_path = state_path
        self.observer_path = observer_path
        self.hid_root = hid_root
        self.owners = {}
        self.known = uhid_devices(hid_root)
        self.lock = threading.RLock()

    def write_state(self):
        # Under the lock because creations are handled on threads of their own,
        # and two of them writing the same temporary file at once would leave
        # one of them renaming nothing.
        with self.lock:
            _write_json(self.state_path, self.owners)

    def announce(self):
        """Say that the probe is live, and from which device on.

        device_owner.py reads this inside udev to decide whether an entry it
        does not find yet is on its way. The horizon is the newest HID device
        that already existed, of any kind, since the counter is shared: every
        uhid device above it was made while the probe was watching.

        Written only once bpftrace says the probe is attached. A device made
        before that is not seen, and if it were counted as coming, udev would
        wait for it for nothing.
        """
        with self.lock:
            # Whatever appeared while bpftrace was still compiling was made
            # unseen, and must not be claimed by the next creation that is seen:
            # that would file one seat's pad under whoever made the next one.
            self.known |= uhid_devices(self.hid_root)
            horizon = max((instance_of(h) for h in hid_devices(self.hid_root)),
                          default=-1)
        _write_json(self.observer_path, {"pid": os.getpid(), "horizon": horizon})

    def withdraw(self):
        try:
            os.unlink(self.observer_path)
        except OSError:
            pass

    def prune(self):
        """Forget devices that no longer exist, so the file cannot grow forever."""
        alive = hid_devices(self.hid_root)
        with self.lock:
            gone = [k for k in self.owners if k not in alive]
            for k in gone:
                del self.owners[k]
            self.known &= alive
        return bool(gone)

    def claim(self, container):
        """Write down the oldest uhid device nobody has claimed yet, if any.

        One device per creation, because that is what uhid_dev_create2 makes.
        The first version handed every device that had appeared since to
        whichever creation looked first, so two seats making a pad in the same
        moment could both be filed under one of them.
        """
        with self.lock:
            fresh = sorted(uhid_devices(self.hid_root) - self.known,
                           key=instance_of)
            if not fresh:
                return None
            hid = fresh[0]
            self.known.add(hid)
            self.owners[hid] = container
            self.write_state()
            return hid

    def on_create(self, pid, wait=2.0, poll=0.002,
                  clock=time.monotonic, sleep=time.sleep):
        """A uhid device is being created by `pid`. Find out which one.

        The kprobe fires on entry, before the kernel has even numbered the
        device, so the id is found by looking for the new one in sysfs.

        It looks at once and then every two milliseconds, and the speed is what
        matters. The udev rule asks this record whether a new gamepad belongs
        to a container while udev is still handling the device's first events,
        and it waits for an answer only briefly. This used to sleep fifty
        milliseconds before its first look, which was longer than udev takes,
        so the rule mostly found nothing and a pad the name list does not cover
        stayed open to the host desktop.
        """
        # At once, before anything else: the creating process may be gone in a
        # moment, and its cgroup with it.
        container = container_of(pid)
        deadline = clock() + wait
        while True:
            hid = self.claim(container)
            if hid:
                where = container or "HOST"
                print(f"  {hid}  created by pid {pid} in {where}", flush=True)
                return hid
            if clock() >= deadline:
                break
            sleep(poll)
        print(f"  pid {pid} created a uhid device that never appeared", flush=True)
        return None

    def run(self):
        # A record from an earlier run would say an observer is watching while
        # this one is not yet.
        self.withdraw()
        proc = subprocess.Popen(
            ["bpftrace", "-e", PROBE],
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1)

        last_prune = time.time()
        cannot_attach = False
        try:
            for line in proc.stdout:
                line = line.strip()
                if NO_SYMBOL in line:
                    cannot_attach = True
                if line.startswith("create "):
                    try:
                        pid = int(line.split()[1])
                    except (IndexError, ValueError):
                        continue
                    threading.Thread(target=self.on_create, args=(pid,),
                                     daemon=True).start()
                elif line:
                    print(f"  bpftrace: {line}", flush=True)
                    if ATTACHED_RE.match(line):
                        self.announce()

                if time.time() - last_prune > 5:
                    last_prune = time.time()
                    if self.prune():
                        self.write_state()
        finally:
            # Nobody should wait on a record that is no longer being kept.
            self.withdraw()

        code = proc.wait()

        if cannot_attach:
            print("this kernel has no uhid_dev_create2 to attach to. uhid is "
                  "most likely a module that is not loaded: modprobe uhid, and "
                  "an entry in /etc/modules-load.d keeps it loaded across a "
                  "reboot. Gamepads still work meanwhile; the broker attributes "
                  "them by name rather than structurally.", file=sys.stderr)
            return EXIT_CANNOT_ATTACH

        print(f"bpftrace exited with {code}", file=sys.stderr)
        return code


def _write_json(path, data):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    tmp = f"{path}.tmp"
    with open(tmp, "w") as fh:
        json.dump(data, fh)
    os.replace(tmp, path)


def owner_of_node(node, state_path=STATE):
    """Which container owns the input device `node`, if it came through uhid.

    Returns the container name, None when the creator was on the host, and
    "unknown" when the device did not come through uhid or was created before
    the observer started.
    """
    real = os.path.realpath(f"/sys/class/input/{node}")
    match = re.search(r"/misc/uhid/([^/]+)/", real)
    if not match:
        return "unknown"
    try:
        with open(state_path) as fh:
            owners = json.load(fh)
    except (OSError, ValueError):
        return "unknown"
    return owners.get(match.group(1), "unknown")


def main():
    if os.geteuid() != 0:
        sys.exit("needs root: attaching a kprobe is privileged")
    print(f"watching uhid_dev_create2, state in {STATE}", flush=True)
    obs = Observer()
    obs.write_state()
    # The daemon stops this with SIGTERM, which by default ends Python without
    # running a single finally block. As an exit instead, the record saying an
    # observer is watching goes with it, and udev stops waiting on one.
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
    sys.exit(obs.run())


if __name__ == "__main__":
    main()
