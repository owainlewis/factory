import unittest
from app import *

class Tests(unittest.TestCase):
    def test_roundtrip(self):
        import csv, io
        titles = ['a,b', 'say "hi"', 'line1\nline2', '', 'café']
        rows = [{'id': i, 'title': t, 'status': 'open'} for i,t in enumerate(titles)]
        parsed = list(csv.DictReader(io.StringIO(export_csv(rows))))
        self.assertEqual([r['title'] for r in parsed], titles)
        self.assertEqual([r['id'] for r in parsed], [str(i) for i in range(5)])
        self.assertEqual([r['title'] for r in rows], titles)
