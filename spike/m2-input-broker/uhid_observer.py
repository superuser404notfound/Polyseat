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
current. Needs root.

    sudo ./uhid-observer.py
"""

import json
import os
import re
import subprocess
import sys
import threading
import time

STATE = "/run/polyseat/uhid-owners.json"
HID_DEVICES = "/sys/bus/hid/devices"

# bpftrace is used rather than a compiled BPF object so that this stays
# readable and has no build step. The probe is the only thing that matters.
PROBE = 'kprobe:uhid_dev_create2 { printf("create %d\\n", pid); }'

CGROUP_RE = re.compile(r"(?:lxc|incus)\.payload\.([A-Za-z0-9_.-]+)")


def container_of(pid):
    """Container name from the process's cgroup, None when it runs on the host."""
    try:
        with open(f"/proc/{pid}/cgroup") as fh:
            match = CGROUP_RE.search(fh.read())
    except OSError:
        return None
    return match.group(1) if match else None


def hid_devices():
    try:
        return set(os.listdir(HID_DEVICES))
    except OSError:
        return set()


class Observer:
    def __init__(self, state_path=STATE):
        self.state_path = state_path
        self.owners = {}
        self.known = hid_devices()
        self.lock = threading.Lock()

    def write_state(self):
        os.makedirs(os.path.dirname(self.state_path), exist_ok=True)
        tmp = f"{self.state_path}.tmp"
        with open(tmp, "w") as fh:
            json.dump(self.owners, fh)
        os.replace(tmp, self.state_path)

    def prune(self):
        """Forget devices that no longer exist, so the file cannot grow forever."""
        alive = hid_devices()
        with self.lock:
            gone = [k for k in self.owners if k not in alive]
            for k in gone:
                del self.owners[k]
        return bool(gone)

    def on_create(self, pid):
        """A uhid device is being created by `pid`. Find out which one.

        The kprobe fires on entry, so the device is not registered yet. Waiting
        briefly and then diffing the HID device list is enough: creations are
        rare and serialised, and the alternative, parsing the event payload out
        of kernel memory, would buy accuracy that is not needed here.
        """
        container = container_of(pid)
        for _ in range(40):                    # up to two seconds
            time.sleep(0.05)
            current = hid_devices()
            new = current - self.known
            if new:
                self.known = current
                with self.lock:
                    for hid in new:
                        self.owners[hid] = container
                self.write_state()
                where = container or "HOST"
                for hid in sorted(new):
                    print(f"  {hid}  created by pid {pid} in {where}", flush=True)
                return
        print(f"  pid {pid} created a uhid device that never appeared", flush=True)

    def run(self):
        proc = subprocess.Popen(
            ["bpftrace", "-e", PROBE],
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1)

        last_prune = time.time()
        for line in proc.stdout:
            line = line.strip()
            if line.startswith("create "):
                try:
                    pid = int(line.split()[1])
                except (IndexError, ValueError):
                    continue
                threading.Thread(target=self.on_create, args=(pid,),
                                 daemon=True).start()
            elif line:
                print(f"  bpftrace: {line}", flush=True)

            if time.time() - last_prune > 5:
                last_prune = time.time()
                if self.prune():
                    self.write_state()

        code = proc.wait()
        print(f"bpftrace exited with {code}", file=sys.stderr)
        return code


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
    sys.exit(obs.run())


if __name__ == "__main__":
    main()
