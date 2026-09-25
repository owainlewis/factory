import unittest
from app import reserve
class Tests(unittest.TestCase):
    def test_cumulative_and_atomic(self):
        for requests, error in [([('a',2),('a',2)],ValueError),([('a',1),('missing',1)],KeyError),([('a',1),('b',3)],ValueError)]:
            stock={'a':3,'b':2}
            with self.subTest(requests=requests),self.assertRaises(error): reserve(stock,requests)
            self.assertEqual(stock,{'a':3,'b':2})
    def test_invalid_quantities_are_atomic(self):
        for quantity in [True,False,0,-1,1.5,'2',None,[],{}]:
            stock={'a':3,'b':2}
            with self.subTest(quantity=quantity),self.assertRaises(ValueError): reserve(stock,[('a',1),('b',quantity)])
            self.assertEqual(stock,{'a':3,'b':2})
    def test_success_copy_and_empty(self):
        stock={'a':3,'b':2}
        result=reserve(stock,[('a',1),('a',2),('b',2)])
        self.assertEqual(result,{'a':0,'b':0})
        result['a']=100
        self.assertEqual(stock,{'a':0,'b':0})
        result=reserve(stock,[])
        self.assertIsNot(result,stock)
        self.assertEqual(result,stock)
        self.assertEqual(reserve({},[]),{})
