import unittest
from app import *

class Tests(unittest.TestCase):
    def test_first_page(self):
        self.assertEqual(page([1, 2, 3], 1, 2), [1, 2])
