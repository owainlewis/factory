import unittest
from app import *

class Tests(unittest.TestCase):
    def test_mixed(self):
        rows = [{'id':2,'status':'open','due':'2024-02-29'},
                {'id':1,'status':'open','due':'2024-02-29'},
                {'id':3,'status':'closed','due':'2024-01-01'},
                {'id':4,'status':'open','due':'2024-03-01'},
                {'id':5,'status':'open','due':'2023-02-29'},
                {'id':6,'status':'open','due':None},
                {'id':7,'status':'open'}]
        self.assertEqual(overdue(rows,'2024-03-01'),[1,2])
        self.assertEqual(rows[0]['id'],2)
        with self.assertRaises(ValueError): overdue(rows,'bad')
