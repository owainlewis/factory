import unittest
from app import parse_ranges
class Tests(unittest.TestCase):
    def test_simple(self):
        self.assertEqual(parse_ranges('2-4',10),[(2,4)])
