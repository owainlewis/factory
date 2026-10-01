Fix close_many(store, ids). Validate that every requested ID exists before changing anything; a missing ID raises KeyError and leaves the entire store unchanged. On success close each distinct requested issue and return the number of distinct issues requested, including already-closed issues. Empty input returns zero.

Keep the public API in app.py. Use only the Python standard library. Preserve existing behavior except where this task explicitly changes it.
