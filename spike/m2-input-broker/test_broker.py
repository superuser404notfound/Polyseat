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


if __name__ == "__main__":
    unittest.main()
