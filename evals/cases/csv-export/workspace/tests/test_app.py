import unittest
from app import *

class Tests(unittest.TestCase):
    def test_header(self):
        self.assertEqual(export_csv([]).strip(), 'id,title,status')
