import json
import os
import stat
from pathlib import Path
from uuid import uuid4


def save(path, data):
    path = Path(path)
    temporary = path.parent / f'.{path.name}.{uuid4().hex}.tmp'
    # O_EXCL avoids clobbering another file; mode 0666 respects the process umask.
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o666)
    try:
        with os.fdopen(fd, 'w') as f:
            json.dump(data, f)
        try:
            mode = stat.S_IMODE(path.stat().st_mode)
        except FileNotFoundError:
            pass
        else:
            temporary.chmod(mode)
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def load(path):
    with open(path) as f:
        return json.load(f)
