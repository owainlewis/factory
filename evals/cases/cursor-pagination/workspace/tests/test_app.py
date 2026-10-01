import unittest
from app import *

class Tests(unittest.TestCase):
    def test_first(self):
        self.assertEqual(after_page([{'id':1},{'id':2},{'id':3}]), ([{'id':1},{'id':2}],2))
