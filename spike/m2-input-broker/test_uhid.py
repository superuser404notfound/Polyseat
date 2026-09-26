#!/usr/bin/env python3
"""The race between the uhid observer and the udev rule, with time faked.

The rule asks device_owner.py whether a new gamepad came from a container, and
device_owner.py answers from the file the observer writes. Both start from the
same kernel event and the rule is usually first, so whether a seat's DualSense
is hidden from the host desktop at creation or only half a second later comes
down to who waits for whom. None of it needs a kernel: the observer's view of
sysfs is a directory of links, and the clock is one these tests advance.

    python3 -m unittest discover -s spike/m2-input-broker
"""

import json
import os
import tempfile
import unittest

import device_owner
import uhid_observer

# Where the kernel puts a uhid device, and where it puts a USB one.
UHID = "../../devices/virtual/misc/uhid/{}"
USB = "../../devices/pci0000:00/0000:00:14.0/usb1/1-2/1-2:1.0/{}"

PAD = "0003:054C:0CE6.0020"
PAD_INPUT = f"/devices/virtual/misc/uhid/{PAD}/input/input300/event30"
PAD_HIDRAW = f"/devices/virtual/misc/uhid/{PAD}/hidraw/hidraw7"


class FakeTime:
    """A clock that moves only when somebody sleeps, and runs a hook when it
    does, which is where the other side of the race gets its turn."""

    def __init__(self, hook=None):
        self.now = 1000.0
        self.slept = 0
        self.hook = hook

    def clock(self):
        return self.now

    def sleep(self, seconds):
        self.slept += 1
        self.now += seconds
        if self.hook:
            self.hook(self)


class Sandbox(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        root = self.dir.name
        self.hid_root = os.path.join(root, "hid")
        os.mkdir(self.hid_root)
        self.owners_path = os.path.join(root, "run", "uhid-owners.json")
        self.observer_path = os.path.join(root, "run", "uhid-observer.json")

        self._saved = (device_owner.UHID_OWNERS, device_owner.UHID_OBSERVER,
                       device_owner._observer_alive, uhid_observer.container_of)
        device_owner.UHID_OWNERS = self.owners_path
        device_owner.UHID_OBSERVER = self.observer_path
        # The test process is not called uhid_observer, so liveness is decided
        # by the test.
        self.alive = True
        device_owner._observer_alive = lambda pid: self.alive
        self.cgroup = {}
        uhid_observer.container_of = lambda pid: self.cgroup.get(pid)

    def tearDown(self):
        (device_owner.UHID_OWNERS, device_owner.UHID_OBSERVER,
         device_owner._observer_alive, uhid_observer.container_of) = self._saved
        self.dir.cleanup()

    def appear(self, hid, where=UHID):
        os.symlink(where.format(hid), os.path.join(self.hid_root, hid))

    def observer(self):
        return uhid_observer.Observer(self.owners_path, self.observer_path,
                                      self.hid_root)


class TheRuleWaitsOnlyWhenAnAnswerIsComing(Sandbox):

    def test_an_answer_that_arrives_after_udev_asked_still_hides_the_pad(self):
        # The case this exists for. The device is in sysfs, udev has started
        # the helper, and the observer has not written it down yet. It does
        # so thirty milliseconds later, which is roughly what it cost before
        # the observer stopped sleeping first.
        self.appear("0003:046D:C52B.001F")
        obs = self.observer()
        obs.announce()
        self.appear(PAD)
        self.cgroup[4242] = "vince"

        def observer_catches_up(t):
            if t.slept == 6:
                obs.claim(uhid_observer.container_of(4242))

        t = FakeTime(observer_catches_up)
        answer = device_owner.uhid_owner(PAD_INPUT, "add",
                                         clock=t.clock, sleep=t.sleep)

        self.assertEqual(answer, "container")

    def test_the_hidraw_half_gets_the_same_answer(self):
        obs = self.observer()
        obs.announce()
        self.appear(PAD)
        obs.claim("vince")

        self.assertEqual(device_owner.uhid_owner(PAD_HIDRAW), "container")

    def test_a_host_pad_already_written_down_is_answered_at_once(self):
        # A controller the host pairs over Bluetooth is a uhid device too, and
        # every one of its events passes through here.
        obs = self.observer()
        obs.announce()
        self.appear(PAD)
        obs.claim(None)

        t = FakeTime()
        answer = device_owner.uhid_owner(PAD_INPUT, "add",
                                         clock=t.clock, sleep=t.sleep)

        self.assertEqual(answer, "host")
        self.assertEqual(t.slept, 0)

    def test_a_device_older_than_the_observer_is_not_waited_for(self):
        # A coldplug trigger replays every device, and none of those will ever
        # be written down. A second each would stall the boot.
        self.appear(PAD)
        self.observer().announce()

        t = FakeTime()
        answer = device_owner.uhid_owner(PAD_INPUT, "add",
                                         clock=t.clock, sleep=t.sleep)

        self.assertIsNone(answer)
        self.assertEqual(t.slept, 0)

    def test_without_a_live_observer_nothing_is_waited_for(self):
        self.observer().announce()
        self.appear(PAD)
        self.alive = False

        t = FakeTime()
        answer = device_owner.uhid_owner(PAD_INPUT, "add",
                                         clock=t.clock, sleep=t.sleep)

        self.assertIsNone(answer)
        self.assertEqual(t.slept, 0)

    def test_without_an_observer_record_nothing_is_waited_for(self):
        self.appear(PAD)

        t = FakeTime()
        answer = device_owner.uhid_owner(PAD_INPUT, "add",
                                         clock=t.clock, sleep=t.sleep)

        self.assertIsNone(answer)
        self.assertEqual(t.slept, 0)

    def test_a_remove_event_is_not_waited_for(self):
        self.observer().announce()

        t = FakeTime()
        answer = device_owner.uhid_owner(PAD_INPUT, "remove",
                                         clock=t.clock, sleep=t.sleep)

        self.assertIsNone(answer)
        self.assertEqual(t.slept, 0)

    def test_the_wait_ends(self):
        # An observer that died between saying it was watching and writing the
        # device down. udev gets "unknown" and the broker takes over.
        self.observer().announce()
        self.appear(PAD)

        t = FakeTime()
        answer = device_owner.uhid_owner(PAD_INPUT, "add", wait=1.0,
                                         clock=t.clock, sleep=t.sleep)

        self.assertIsNone(answer)
        self.assertLessEqual(t.now - 1000.0, 1.0 + device_owner.UHID_POLL)

    def test_an_instance_past_four_digits_is_still_read(self):
        # The counter is printed with at least four digits, not exactly four.
        obs = self.observer()
        obs.announce()
        hid = "0003:054C:0CE6.1002A"
        self.appear(hid)
        obs.claim("vince")

        path = f"/devices/virtual/misc/uhid/{hid}/input/input301/event31"
        self.assertEqual(device_owner.uhid_owner(path), "container")


class TheObserverAnswersAtOnce(Sandbox):

    def test_a_device_already_there_is_claimed_without_sleeping(self):
        # The fifty milliseconds the first version slept before its first look
        # were longer than udev takes to ask.
        obs = self.observer()
        self.appear(PAD)
        self.cgroup[7] = "joser"

        t = FakeTime()
        hid = obs.on_create(7, clock=t.clock, sleep=t.sleep)

        self.assertEqual(hid, PAD)
        self.assertEqual(t.slept, 0)
        with open(self.owners_path) as fh:
            self.assertEqual(json.load(fh), {PAD: "joser"})

    def test_a_device_that_appears_a_little_later_is_found(self):
        obs = self.observer()
        self.cgroup[7] = "joser"

        def kernel_adds(t):
            if t.slept == 3:
                self.appear(PAD)

        t = FakeTime(kernel_adds)
        self.assertEqual(obs.on_create(7, clock=t.clock, sleep=t.sleep), PAD)

    def test_a_usb_device_plugged_in_at_the_same_moment_is_not_the_seats(self):
        obs = self.observer()
        self.appear("0003:046D:C52B.001F", USB)
        self.appear(PAD)
        self.cgroup[7] = "joser"

        t = FakeTime()
        obs.on_create(7, clock=t.clock, sleep=t.sleep)

        self.assertNotIn("0003:046D:C52B.001F", obs.owners)

    def test_two_creations_get_one_device_each(self):
        obs = self.observer()
        self.appear("0003:054C:0CE6.0021")
        self.appear("0003:054C:0CE6.0022")
        self.cgroup[7], self.cgroup[8] = "joser", "vince"

        t = FakeTime()
        obs.on_create(7, clock=t.clock, sleep=t.sleep)
        obs.on_create(8, clock=t.clock, sleep=t.sleep)

        self.assertEqual(obs.owners, {"0003:054C:0CE6.0021": "joser",
                                      "0003:054C:0CE6.0022": "vince"})

    def test_what_appeared_before_the_probe_was_live_is_not_claimed(self):
        # Made while bpftrace was still compiling, so its creation was never
        # seen. The next creation that is seen must not take it.
        obs = self.observer()
        self.appear("0003:054C:0CE6.0021")
        obs.announce()
        self.appear("0003:054C:0CE6.0022")

        t = FakeTime()
        obs.on_create(7, clock=t.clock, sleep=t.sleep)

        self.assertEqual(list(obs.owners), ["0003:054C:0CE6.0022"])

    def test_the_record_is_withdrawn(self):
        obs = self.observer()
        obs.announce()
        with open(self.observer_path) as fh:
            meta = json.load(fh)
        self.assertEqual(meta["pid"], os.getpid())

        obs.withdraw()
        self.assertFalse(os.path.exists(self.observer_path))

    def test_bpftrace_saying_it_is_attached_is_recognised(self):
        for line in ("Attaching 1 probe...", "Attached 1 probe",
                     "Attached 2 probes"):
            self.assertTrue(uhid_observer.ATTACHED_RE.match(line), line)
        self.assertFalse(
            uhid_observer.ATTACHED_RE.match("Attachment failed for all probes."))


if __name__ == "__main__":
    unittest.main()
