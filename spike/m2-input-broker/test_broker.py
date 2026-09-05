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
    written on, from both Sunshine builds."""

    def test_a_name_with_no_parentheses_makes_no_claim(self):
        # The libvirtualhid build. There is nothing here to agree or disagree
        # with, and treating that as disagreement is exactly the bug.
        for name in ("libvirtualhid Mouse", "libvirtualhid Keyboard"):
            self.assertFalse(broker.contradicts(name, "vince"), name)
            self.assertFalse(broker.contradicts(name, "joser"), name)

    def test_our_own_tag_does_not_contradict_us(self):
        self.assertFalse(broker.contradicts("Mouse passthrough (vince)", "vince"))

    def test_a_second_parenthesised_word_is_not_a_foreign_tag(self):
        # "Mouse passthrough (vince) (absolute)" is one of this seat's own
        # devices. Reading only the last group would call it somebody else's,
        # and it is a real name rather than a hypothetical one.
        self.assertFalse(
            broker.contradicts("Mouse passthrough (vince) (absolute)", "vince"))

    def test_a_tag_that_is_not_last_still_counts(self):
        # And the mirror image: the pad names carry "(virtual)" after the model
        # and the seat at the end.
        self.assertFalse(
            broker.contradicts("Sunshine X-Box One (virtual) pad (seat1)", "seat1"))

    def test_another_seats_device_does_contradict(self):
        self.assertTrue(
            broker.contradicts("Mouse passthrough (joser) (absolute)", "vince"))
        self.assertTrue(
            broker.contradicts("Sunshine X-Box One (virtual) pad (seat1)", "seat2"))

    def test_no_tag_to_check_against_means_no_objection(self):
        # --tag "" is "ignore names entirely", not "refuse everything".
        self.assertFalse(broker.contradicts("Mouse passthrough (joser)", None))
        self.assertFalse(broker.contradicts("anything at all", None))


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
