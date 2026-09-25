import unittest
from app import *

class Tests(unittest.TestCase):
    def test_boundary(self):
        rows = [{'id':7},{'id':1},{'id':3},{'id':5}]
        self.assertEqual(after_page(rows,2,2), ([{'id':3},{'id':5}],5))
        self.assertEqual(after_page(rows,3,2), ([{'id':5},{'id':7}],None))
        self.assertEqual(after_page(rows,7,2), ([],None))
        self.assertEqual([r['id'] for r in rows],[7,1,3,5])
    def test_bad_limits(self):
        for value in [0,-1,True,1.5,'2']:
            with self.assertRaises(ValueError): after_page([],limit=value)
