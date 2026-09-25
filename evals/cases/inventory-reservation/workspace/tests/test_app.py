import unittest
from app import reserve
class Tests(unittest.TestCase):
    def test_reserve(self):
        stock = {'book': 5, 'pen': 3}
        self.assertEqual(reserve(stock, [('book', 2)]), {'book': 3, 'pen': 3})
        self.assertEqual(stock['book'], 3)
