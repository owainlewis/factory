import unittest
from app import *

class Tests(unittest.TestCase):
    def test_boundaries(self):
        items = [1, 2, 3, 4, 5]
        self.assertEqual(page(items, 2, 2), [3, 4])
        self.assertEqual(page(items, 3, 2), [5])
        self.assertEqual(page(items, 99, 2), [])
        self.assertEqual(items, [1, 2, 3, 4, 5])
    def test_invalid(self):
        for a, b in [(0, 2), (-1, 2), (1, 0), (True, 2), (1, False), (1.0, 2), (1, '2')]:
            with self.subTest(a=a, b=b), self.assertRaises(ValueError):
                page([1], a, b)
