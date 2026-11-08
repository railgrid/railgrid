import unittest
from clamp import clamp


class ClampTests(unittest.TestCase):
    def test_inclusive_clamping(self):
        for value, expected in [(-5, 0), (0, 0), (4, 4), (10, 10), (20, 10)]:
            with self.subTest(value=value):
                self.assertEqual(clamp(value, 0, 10), expected)

    def test_equal_bounds(self):
        self.assertEqual(clamp(20, 3, 3), 3)

    def test_reversed_bounds(self):
        with self.assertRaises(ValueError):
            clamp(5, 10, 0)


if __name__ == "__main__":
    unittest.main()
