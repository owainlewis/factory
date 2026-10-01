Implement after_page(rows, cursor=None, limit=2) returning (items, next_cursor). Sort rows by integer id ascending without mutating input. cursor is an exclusive integer ID boundary (it need not exist). limit must be a positive integer, not bool; otherwise raise ValueError. next_cursor is the last returned ID only when more matching rows remain, else None. IDs are unique.

Keep the public API in app.py. Use only the Python standard library. Preserve existing behavior except where this task explicitly changes it.
