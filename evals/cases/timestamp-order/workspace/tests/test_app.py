import unittest
from app import *

class Tests(unittest.TestCase):
    def test_simple(self):
        rows = [{'created': '2025-01-01T00:00:00Z'}, {'created': '2025-02-01T00:00:00Z'}]
        self.assertEqual(newest(rows), list(reversed(rows)))
