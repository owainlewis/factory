import unittest
from app import *

class Tests(unittest.TestCase):
    def test_recursive_and_falsey(self):
        base = {'db':{'host':'x','port':9},'list':[1],'flag':True,'x':2}
        overrides = {'db':{'port':0},'list':[],'flag':False,'x':None}
        self.assertEqual(overlay(base,overrides), {'db':{'host':'x','port':0},'list':[],'flag':False,'x':None})
    def test_no_aliasing(self):
        base = {'a':[{'x':1}], 'b':{'c':[1]}}
        override = {'d':[2]}
        out = overlay(base,override)
        out['a'][0]['x']=9; out['b']['c'].append(2); out['d'].append(3)
        self.assertEqual(base,{'a':[{'x':1}], 'b':{'c':[1]}})
        self.assertEqual(override,{'d':[2]})
