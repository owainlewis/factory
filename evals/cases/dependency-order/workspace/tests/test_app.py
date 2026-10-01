import unittest
from app import order_jobs
class Tests(unittest.TestCase):
    def test_order(self):
        self.assertEqual(order_jobs({'build':['fetch'],'fetch':[]}), ['fetch','build'])
