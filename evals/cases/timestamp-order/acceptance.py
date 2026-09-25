import unittest
from app import *

class Tests(unittest.TestCase):
    def test_offsets_and_stability(self):
        rows = [{'id': 1, 'created': '2025-01-01T10:00:00+02:00'},
                {'id': 2, 'created': '2025-01-01T09:00:00Z'},
                {'id': 3, 'created': '2025-01-01T08:00:00'},
                {'id': 4, 'created': '2025-01-01T07:30:00-01:00'}]
        self.assertEqual([r['id'] for r in newest(rows)], [2, 4, 1, 3])
        self.assertEqual([r['id'] for r in rows], [1, 2, 3, 4])
