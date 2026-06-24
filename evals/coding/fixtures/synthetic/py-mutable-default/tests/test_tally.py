import unittest

from tally import tally


class TestTally(unittest.TestCase):
    def test_independent_calls(self):
        # Two independent calls (no dict passed) must not share state.
        first = tally(["a", "a"])
        self.assertEqual(first, {"a": 2})
        second = tally(["b"])
        # A fresh independent call must only see its own items.
        self.assertEqual(second, {"b": 1})

    def test_does_not_mutate_caller_dict(self):
        # Passing an explicit dict is allowed to update it...
        d = {}
        result = tally(["x"], d)
        self.assertEqual(result, {"x": 1})
        self.assertEqual(d, {"x": 1})
        # ...but a later call with a fresh dict must not see the old 'x'.
        other = tally(["y"], {})
        self.assertEqual(other, {"y": 1})
        self.assertNotIn("x", other)

    def test_preserves_passed_dict(self):
        # A caller-supplied dict with existing data must be preserved
        # (the '.clear()' band-aid would wipe the seed).
        d = {"seed": 1}
        tally(["x"], d)
        self.assertEqual(d, {"seed": 1, "x": 1})


if __name__ == "__main__":
    unittest.main()
