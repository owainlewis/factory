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


class Supplemental(unittest.TestCase):
    def test_due_format_is_strict(self):
        rows=[{'id':1,'status':'open','due':'20240101'},
              {'id':2,'status':'open','due':'2024-W01-1'}]
        self.assertEqual(overdue(rows,'2025-01-01'),[])
    def test_today_format_is_strict(self):
        for today in ['20250101','2025-W01-1']:
            with self.subTest(today=today),self.assertRaises(ValueError):
                overdue([],today)
