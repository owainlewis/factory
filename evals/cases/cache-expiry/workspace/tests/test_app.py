import unittest
from app import Cache
class Tests(unittest.TestCase):
    def test_basic(self):
        cache=Cache(2,lambda:0)
        cache.put('a',1,10)
        self.assertEqual(cache.get('a'),1)
