import unittest
from app import *

class Tests(unittest.TestCase):
    def test_valid(self):
        self.assertEqual(parse_jsonl('{"id":1,"title":"a"}'), ([{'id':1,'title':'a'}],[]))
