import unittest
from app import *

class Tests(unittest.TestCase):
    def test_scalar(self):
        self.assertEqual(overlay({'a':1}, {'a':2}), {'a':2})
