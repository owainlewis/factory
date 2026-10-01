import unittest
from app import *

class Tests(unittest.TestCase):
    def test_round_trip(self):
        import tempfile
        from pathlib import Path
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / 'data.json'
            save(p, {'a': 1})
            self.assertEqual(load(p), {'a': 1})
