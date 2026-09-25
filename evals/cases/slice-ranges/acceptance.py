import unittest
from app import parse_ranges
class Tests(unittest.TestCase):
    def test_clipping_suffixes_and_merge(self):
        cases=[(' 2-4 ,\t0-1,4-8 ',10,[(0,8)]),('-3',10,[(7,9)]),('-99',4,[(0,3)]),('-0',4,[]),('4-',4,[]),('2-99',4,[(2,3)]),('9-10',4,[]),('2-',4,[(2,3)]),('5-6,1-2,3-4,1-2',8,[(1,6)]),('0-,9-10',0,[])]
        for text,size,expected in cases:
            with self.subTest(text=text,size=size): self.assertEqual(parse_ranges(text,size),expected)
    def test_syntax(self):
        for text in ['',',','-','1-2,','0-,garbage','1 -2','1- 2','+1-2','١-٢','１-２','0-1\n','\v0-1','1--2','1-2-3',None,[],True]:
            with self.subTest(text=text),self.assertRaises(ValueError): parse_ranges(text,10)
    def test_validate_even_empty_or_already_covered(self):
        for text,size in [('3-2',0),('0-,3-2',10),('bad',0),('0-,bad',0)]:
            with self.subTest(text=text,size=size),self.assertRaises(ValueError): parse_ranges(text,size)
        for size in [True,False,-1,1.5,'10',None]:
            with self.subTest(size=size),self.assertRaises(ValueError): parse_ranges('0-1',size)
    def test_position_oracle(self):
        for size in range(6):
            for a in range(7):
                for b in range(a,7):
                    actual=parse_ranges(f'{a}-{b},-2',size)
                    positions={p for first,last in actual for p in range(first,last+1)}
                    expected=set(range(a,min(b+1,size)))|set(range(max(0,size-2),size))
                    self.assertEqual(positions,expected)
                    self.assertTrue(all(actual[i][1]+1<actual[i+1][0] for i in range(len(actual)-1)))
