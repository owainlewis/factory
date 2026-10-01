import unittest
from app import *

class Tests(unittest.TestCase):
    def test_overdue(self):
        self.assertEqual(overdue([{'id':1,'status':'open','due':'2025-01-01'}], '2025-01-02'),[1])
