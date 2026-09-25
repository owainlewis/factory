import unittest
from app import *

class Tests(unittest.TestCase):
    def test_update(self):
        rows = {1: {'title': 'old', 'status': 'open', 'version': 1}}
        self.assertEqual(update(rows, 1, 1, {'title': 'new'})['version'], 2)
