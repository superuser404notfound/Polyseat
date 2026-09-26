#!/usr/bin/env python3
"""What the broker decides, checked without a seat to decide it about.

Attribution is the one part of this that can be wrong quietly. A device that
does not arrive is a complaint within the minute; a device that arrives in the
wrong seat is somebody else's mouse on your screen, and the first person to
notice may be the one it was taken from. So the rules live behind two functions
that can be asked directly, and this asks them with the names real builds
produce rather than with invented ones.

    python3 -m unittest discover -s spike/m2-input-broker
"""

import os
import tempfile
import unittest

import broker


class TheNamesRealBuildsProduce(unittest.TestCase):
    """contradicts() against every device name seen on the machine this was
    written on, from both Sunshine builds and both controllers."""

    SEATS = ("joser", "vince")

    def test_a_name_with_no_parentheses_makes_no_claim(self):
        # The libvirtualhid build. There is nothing here to agree or disagree
        # with, and treating that as disagreement is exactly the bug.
        for name in ("libvirtualhid Mouse", "libvirtualhid Keyboard",
                     "libvirtualhid Touchscreen", "libvirtualhid Pen Tablet",
                     "Wireless Controller", "Wireless Controller Touchpad"):
            self.assertFalse(broker.contradicts(name, "vince", self.SEATS), name)
            self.assertFalse(broker.contradicts(name, "joser", self.SEATS), name)

    def test_our_own_tag_does_not_contradict_us(self):
        self.assertFalse(
            broker.contradicts("Mouse passthrough (vince)", "vince", self.SEATS))

    def test_a_word_in_brackets_that_is_not_a_seat_means_nothing(self):
        # The case that broke the first version of this, found on a real device
        # rather than by thinking: the pad Sunshine emulates for a seat is
        # called "Sunshine (libvirtualhid) X-Box Series Controller". A rule that
        # counts any bracketed word as a foreign tag refuses that seat its own
        # controller. "(virtual)" and "(absolute)" are the same kind of word.
        for name in ("Sunshine (libvirtualhid) X-Box Series Controller",
                     "Mouse passthrough (vince) (absolute)",
                     "Sunshine X-Box One (virtual) pad"):
            self.assertFalse(broker.contradicts(name, "vince", self.SEATS), name)

    def test_a_tag_that_is_not_last_still_counts(self):
        self.assertFalse(
            broker.contradicts("Sunshine X-Box One (virtual) pad (seat1)",
                               "seat1", ("seat1", "seat2")))

    def test_another_seats_device_does_contradict(self):
        self.assertTrue(
            broker.contradicts("Mouse passthrough (joser) (absolute)", "vince",
                               self.SEATS))
        self.assertTrue(
            broker.contradicts("Sunshine X-Box One (virtual) pad (seat1)",
                               "seat2", ("seat1", "seat2")))

    def test_with_no_other_seats_named_nothing_can_contradict(self):
        # One seat on the host, or a daemon that did not pass the list. The
        # structural answer then decides alone, which is the safe direction.
        self.assertFalse(broker.contradicts("Mouse passthrough (joser)", "vince", ()))
        self.assertFalse(broker.contradicts("anything at all", None, ()))


class WhoOwnsADevice(unittest.TestCase):
    """attribute() with the helper it asks answering from a fixture.

    The uinput case is the regression this file exists for: keyboards and mice
    are traced to their creator's cgroup and the name is not consulted, so a
    Sunshine that stopped writing seat tags must not change the answer.
    """

    def setUp(self):
        self._owner = broker.device_owner
        broker._reported.clear()
        broker._uhid_seen = {}

    def tearDown(self):
        broker.device_owner = self._owner

    def _fixture(self, owners, holders=None):
        class Owner:
            @staticmethod
            def owners():
                return owners

            @staticmethod
            def uhid_holders():
                return holders or {}

        broker.device_owner = Owner

    def test_an_untagged_uinput_device_goes_to_the_seat_that_made_it(self):
        self._fixture({"input172": "joser"})

        mine = broker.attribute(
            {"event28": {"sysname": "input172", "name": "libvirtualhid Mouse",
                         "syspath": "/sys/devices/virtual/input/input172"}},
            "joser", "joser")

        self.assertIn("event28", mine)
        self.assertEqual(mine["event28"]["attribution"], "owner")

    def test_the_same_device_does_not_go_to_the_other_seat(self):
        self._fixture({"input172": "joser"})

        mine = broker.attribute(
            {"event28": {"sysname": "input172", "name": "libvirtualhid Mouse",
                         "syspath": "/sys/devices/virtual/input/input172"}},
            "vince", "vince")

        self.assertEqual(mine, {})

    def test_a_device_made_on_the_host_belongs_to_nobody(self):
        # None is the host, and it stays refused however it is named.
        self._fixture({"input9": None})

        mine = broker.attribute(
            {"event9": {"sysname": "input9", "name": "Mouse passthrough (vince)",
                        "syspath": "/sys/devices/virtual/input/input9"}},
            "vince", "vince")

        self.assertEqual(mine, {})


class SealingReachesWhatTheHostAlreadyHolds(unittest.TestCase):
    """seal() against a sysfs, /dev and udev database built in a directory.

    A seat's DualSense as the kernel lays it out: one uhid device with an event
    node, a joystick node beside it, and a raw HID node. The broker is asked to
    seal it, and what it would have done to the host is recorded rather than
    done.
    """

    PAD = "devices/virtual/misc/uhid/0003:054C:0CE6.0020"

    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        root = self.dir.name
        sys_root = os.path.join(root, "sys")
        self.sys_root = sys_root

        inp = f"{sys_root}/{self.PAD}/input/input300"
        for node, number in (("event30", "13:94"), ("js2", "13:2")):
            os.makedirs(f"{inp}/{node}")
            with open(f"{inp}/{node}/dev", "w") as fh:
                fh.write(number + "\n")
        os.makedirs(f"{sys_root}/{self.PAD}/hidraw/hidraw7")
        with open(f"{sys_root}/{self.PAD}/hidraw/hidraw7/dev", "w") as fh:
            fh.write("243:7\n")

        os.makedirs(f"{sys_root}/class/input")
        os.makedirs(f"{sys_root}/class/hidraw")
        os.symlink(f"{inp}/event30", f"{sys_root}/class/input/event30")
        os.symlink(f"{sys_root}/{self.PAD}/hidraw/hidraw7",
                   f"{sys_root}/class/hidraw/hidraw7")

        self.db = os.path.join(root, "udev-data")
        os.makedirs(self.db)

        self._saved = (broker.SYS_ROOT, broker.SYS_INPUT, broker.DEV_ROOT,
                       broker.SEALED, broker.UDEV_DB, broker.udev_trigger,
                       broker.seal_path)
        broker.SYS_ROOT = sys_root
        broker.SYS_INPUT = f"{sys_root}/class/input"
        broker.DEV_ROOT = os.path.join(root, "dev")
        broker.SEALED = os.path.join(root, "sealed")
        broker.UDEV_DB = self.db
        broker._reannounced.clear()

        self.events = []
        self.sealed = []

        def trigger(action, devpath):
            # Whether the mark was there when the event went out is the whole
            # point of the order: the add has to come back hidden.
            marked = os.path.isdir(f"{broker.SEALED}{devpath}")
            self.events.append((action, devpath, marked))

        broker.udev_trigger = trigger
        broker.seal_path = lambda path: self.sealed.append(path) or True

    def tearDown(self):
        (broker.SYS_ROOT, broker.SYS_INPUT, broker.DEV_ROOT, broker.SEALED,
         broker.UDEV_DB, broker.udev_trigger, broker.seal_path) = self._saved
        self.dir.cleanup()

    def hidden(self, number):
        with open(f"{self.db}/c{number}", "w") as fh:
            fh.write("E:ID_INPUT=1\nE:POLYSEAT_HIDDEN=1\n")

    def test_the_joystick_node_is_sealed_too(self):
        # js is how Steam finds a controller, and seal() used to do the event
        # node and the raw HID node and leave this one open.
        broker.seal("event30")

        names = [os.path.basename(p) for p in self.sealed]
        self.assertEqual(names, ["event30", "js2", "hidraw7"])

    def test_a_node_the_rule_missed_is_announced_again_after_it_is_marked(self):
        broker.seal("event30")

        event = f"/{self.PAD}/input/input300/event30"
        self.assertIn(("remove", event, True), self.events)
        self.assertIn(("add", event, True), self.events)
        self.assertLess(self.events.index(("remove", event, True)),
                        self.events.index(("add", event, True)))
        self.assertTrue(all(marked for _, _, marked in self.events))

    def test_a_node_the_rule_hid_is_left_where_it_is(self):
        # Hidden at creation means nothing on the host ever opened it.
        for number in ("13:94", "13:2", "243:7"):
            self.hidden(number)

        broker.seal("event30")

        self.assertEqual(self.events, [])

    def test_a_device_is_announced_again_once(self):
        # A rule file older than the broker never says POLYSEAT_HIDDEN, and the
        # host must not see the pad leave and return twice a second.
        broker.seal("event30")
        count = len(self.events)
        broker.seal("event30")

        self.assertEqual(count, 6)
        self.assertEqual(len(self.events), count)

    def test_marks_of_devices_that_are_gone_are_dropped(self):
        broker.seal("event30")
        os.makedirs(f"{broker.SEALED}/devices/virtual/input/input9/event9")

        broker.unmark_gone()

        self.assertFalse(os.path.exists(
            f"{broker.SEALED}/devices/virtual/input/input9"))
        self.assertTrue(os.path.isdir(
            f"{broker.SEALED}/{self.PAD}/input/input300/js2"))


if __name__ == "__main__":
    unittest.main()
