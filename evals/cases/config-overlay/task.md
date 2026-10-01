Implement overlay(base, override) as a recursive dictionary merge. Dictionary values merge only when both sides are dictionaries. All other override values replace the base value, including None, False, zero, empty lists, and empty strings. Neither input may be mutated; the result must share no mutable nested objects with either input.

Keep the public API in app.py. Use only the Python standard library. Preserve existing behavior except where this task explicitly changes it.
