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
        with tempfile.TemporaryDirectory() as d:
            destination = Path(d) / 'destination'
            destination.mkdir()
            marker = destination / 'original.json'
            marker.write_text('original')
            # A real filesystem failure works with any valid import style.
            with self.assertRaises(OSError):
                save(destination, {'ok': 1})
            self.assertTrue(destination.is_dir())
            self.assertEqual(marker.read_text(), 'original')
            self.assertEqual(list(Path(d).iterdir()), [destination])

class Supplemental(unittest.TestCase):
    def test_existing_file_mode_is_preserved(self):
        import os,stat,tempfile
        from pathlib import Path
        with tempfile.TemporaryDirectory() as d:
            path=Path(d)/'data.json'
            path.write_text('{}')
            os.chmod(path,0o640)
            save(path,{'value':1})
            self.assertEqual(stat.S_IMODE(path.stat().st_mode),0o640)
    def test_new_file_mode_matches_open(self):
        import stat,tempfile
        from pathlib import Path
        with tempfile.TemporaryDirectory() as d:
            control=Path(d)/'control.json'; control.write_text('{}')
            candidate=Path(d)/'candidate.json'; save(candidate,{'value':1})
            self.assertEqual(stat.S_IMODE(candidate.stat().st_mode),stat.S_IMODE(control.stat().st_mode))
