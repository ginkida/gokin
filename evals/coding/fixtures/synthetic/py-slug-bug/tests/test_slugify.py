import sys
import os
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "src"))

from slugify import slugify


class TestSlugify(unittest.TestCase):
    def test_lowercases(self):
        self.assertEqual(slugify("Hello World"), "hello-world")

    def test_collapses_and_trims_dashes(self):
        self.assertEqual(slugify("  Big -- News!!  "), "big-news")

    def test_plain(self):
        self.assertEqual(slugify("already-fine"), "already-fine")


if __name__ == "__main__":
    unittest.main()
