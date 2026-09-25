import unittest
from app import *

class Tests(unittest.TestCase):
    def test_failed_serialization(self):
        import tempfile
        from pathlib import Path
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / 'data.json'
            p.write_text('original')
            with self.assertRaises(TypeError):
                save(p, {'bad': object()})
            self.assertEqual(p.read_text(), 'original')
            self.assertEqual(list(Path(d).iterdir()), [p])
    def test_failed_replace(self):
        import tempfile
        from pathlib import Path
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / 'data.json'
            p.write_text('original')
            with patch('os.replace', side_effect=OSError('disk failure')):
                with self.assertRaises(OSError):
                    save(p, {'ok': 1})
            self.assertEqual(p.read_text(), 'original')
            self.assertEqual(list(Path(d).iterdir()), [p])
