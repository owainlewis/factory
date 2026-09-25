import unittest
from app import *

class Tests(unittest.TestCase):
    def test_failure_is_atomic(self):
        import copy
        for changes, version, error in [({'title':'new','status':'bad'},1,ValueError),
                                        ({'version':9},1,ValueError), ({'title':'new'},0,Conflict)]:
            rows = {1: {'title':'old','status':'open','version':1}}
            original = copy.deepcopy(rows)
            with self.assertRaises(error): update(rows,1,version,changes)
            self.assertEqual(rows,original)
    def test_return_copy_and_empty(self):
        rows = {1: {'title':'old','status':'open','version':1, 'labels':['bug']}}
        out = update(rows,1,1,{})
        out['labels'].append('ui')
        self.assertEqual(rows[1]['version'],2)
        self.assertEqual(rows[1]['labels'],['bug'])
        with self.assertRaises(KeyError): update(rows,2,1,{})
