import unittest
from app import *

class Tests(unittest.TestCase):
    def test_upgrade(self):
        out = migrate({'version':1,'issues':[{'id':1,'done':False}]})
        self.assertEqual(out['issues'][0]['status'],'open')
        self.assertEqual(out['version'],2)
