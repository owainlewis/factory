import unittest
from app import *

class Tests(unittest.TestCase):
    def test_close(self):
        rows = {1: {'status': 'open'}}
        self.assertEqual(close_many(rows, [1]), 1)
        self.assertEqual(rows[1]['status'], 'closed')
