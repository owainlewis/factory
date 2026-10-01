import unittest
from app import *

class Tests(unittest.TestCase):
    def test_single_label(self):
        rows = [{'id': 1, 'labels': ['bug'], 'status': 'open'}]
        self.assertEqual(select(rows, ['bug']), rows)
