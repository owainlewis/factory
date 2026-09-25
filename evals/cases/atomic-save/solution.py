import json
import os
import tempfile
from pathlib import Path

def save(path, data):
    path = Path(path)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode='w', dir=path.parent, delete=False) as f:
            temporary = f.name
            json.dump(data, f)
        os.replace(temporary, path)
    finally:
        if temporary is not None and os.path.exists(temporary):
            os.unlink(temporary)

def load(path):
    with open(path) as f:
        return json.load(f)
