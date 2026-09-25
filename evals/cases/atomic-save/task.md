Fix save(path, data) to write JSON atomically using a temporary file in the destination directory and os.replace. On serialization or replacement failure preserve any existing destination, remove temporary files, and propagate the exception. load(path) must retain its behavior.

Keep the public API in app.py. Use only the Python standard library. Preserve existing behavior except where this task explicitly changes it.
