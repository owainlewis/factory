import unittest
from app import Cache
class Tests(unittest.TestCase):
    def setUp(self): self.now=0
    def clock(self): return self.now
    def test_expiry_boundary_and_reads_do_not_extend(self):
        c=Cache(2,self.clock); c.put('a',1,2)
        self.now=1; self.assertEqual(c.get('a'),1)
        self.now=2
        with self.assertRaises(KeyError): c.get('a')
    def test_expired_mru_is_purged_before_live_lru_eviction(self):
        c=Cache(2,self.clock); c.put('a',1,20); c.put('b',2,1)
        self.now=1; c.put('c',3,20)
        self.assertEqual(c.get('a'),1)
        with self.assertRaises(KeyError): c.get('b')
        self.assertEqual(c.get('c'),3)
    def test_lru_replacement_and_deletion(self):
        c=Cache(2,self.clock); c.put('a',1,20); c.put('b',2,20); c.get('a'); c.put('c',3,20)
        with self.assertRaises(KeyError): c.get('b')
        c.put('a',4,1); self.assertEqual(c.get('a'),4)
        self.now=1
        with self.assertRaises(KeyError): c.get('a')
        c.put('c',9,0)
        with self.assertRaises(KeyError): c.get('c')
        c.put('x',9,-1)
        with self.assertRaises(KeyError): c.get('x')
    def test_deep_copies(self):
        c=Cache(2,self.clock); value={'items':[]}; c.put('a',value,10); value['items'].append(1)
        result=c.get('a'); self.assertEqual(result,{'items':[]}); result['items'].append(2)
        self.assertEqual(c.get('a'),{'items':[]})
    def test_invalid_arguments_and_recency(self):
        for capacity in [True,False,0,-1,1.5,'2',None]:
            with self.subTest(capacity=capacity),self.assertRaises(ValueError): Cache(capacity,self.clock)
        for ttl in [True,False,float('nan'),float('inf'),-float('inf'),'2',None,[],{}]:
            c=Cache(2,self.clock); c.put('a',1,10); c.put('b',2,10)
            with self.subTest(ttl=ttl),self.assertRaises(ValueError): c.put('a',99,ttl)
            c.put('c',3,10)
            with self.assertRaises(KeyError): c.get('a')
            self.assertEqual(c.get('b'),2)
