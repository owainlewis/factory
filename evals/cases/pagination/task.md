Fix page(items, number, size). Pages are one-based. Reject bool/non-integer number or size and values below 1 with ValueError. Return an empty list beyond the end; never mutate the input.

Keep the public API in app.py. Use only the Python standard library. Preserve existing behavior except where this task explicitly changes it.
