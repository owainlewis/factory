import unittest
from app import *

class Tests(unittest.TestCase):
    def test_mixed_lines(self):
        import json
        lines = ['', 'bad', json.dumps({'id':1,'title':' '}),
                 json.dumps({'id':1,'title':' ok ','extra':True}),
                 json.dumps({'id':1,'title':'duplicate'}), '[]',
                 json.dumps({'id':True,'title':'bad'}),
                 json.dumps({'id':2.0,'title':'bad'}),
                 json.dumps({'id':2,'title':'fine'})]
        records, errors = parse_jsonl('\n'.join(lines))
        self.assertEqual(errors,[2,3,5,6,7,8])
        self.assertEqual(records,[{'id':1,'title':' ok ','extra':True},{'id':2,'title':'fine'}])
