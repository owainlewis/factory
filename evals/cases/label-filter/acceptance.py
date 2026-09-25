import unittest
from app import *

class Tests(unittest.TestCase):
    def test_all_labels_and_status(self):
        rows = [{'id': 1, 'labels': ['bug'], 'status': 'open'},
                {'id': 2, 'labels': ['bug', 'ui'], 'status': 'closed'},
                {'id': 3, 'labels': ['bug', 'ui'], 'status': 'open'}]
        self.assertEqual([r['id'] for r in select(rows, ['bug','ui'], 'open')], [3])
        self.assertEqual(len(select(rows, [])), 3)
        self.assertEqual(rows[0]['labels'], ['bug'])
    def test_empty_status(self):
        rows = [{'id': 1, 'status': ''}, {'id': 2, 'status': 'open'}]
        self.assertEqual(select(rows, status=''), [rows[0]])
        self.assertEqual(select(rows, labels=['Bug']), [])
