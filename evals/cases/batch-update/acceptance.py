import unittest
from app import *

class Tests(unittest.TestCase):
    def test_rollback(self):
        rows = {1: {'status': 'open'}}
        with self.assertRaises(KeyError):
            close_many(rows, [1, 99])
        self.assertEqual(rows, {1: {'status': 'open'}})
    def test_duplicate(self):
        rows = {1: {'status': 'closed'}}
        self.assertEqual(close_many(rows, [1,1]), 1)
        self.assertEqual(close_many(rows, []), 0)
