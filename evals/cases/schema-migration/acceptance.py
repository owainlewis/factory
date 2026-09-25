import unittest
from app import *

class Tests(unittest.TestCase):
    def test_preservation(self):
        source = {'version':1,'meta':{'a':1},'issues':[{'id':1,'done':True,'labels':['a']}]}
        out = migrate(source)
        self.assertNotIn('done',out['issues'][0])
        self.assertEqual(out['issues'][0]['status'],'closed')
        out['meta']['a']=2; out['issues'][0]['labels'].append('b')
        self.assertEqual(source['meta'],{'a':1})
        self.assertEqual(source['issues'][0]['labels'],['a'])
    def test_idempotency_and_invalid(self):
        source = {'version':2,'issues':[{'id':1,'status':'open','labels':[]}]}
        out = migrate(source)
        self.assertEqual(out,source)
        out['issues'][0]['labels'].append('a')
        self.assertEqual(source['issues'][0]['labels'],[])
        for d in [{}, {'version':3,'issues':[]}]:
            with self.assertRaises(ValueError): migrate(d)
