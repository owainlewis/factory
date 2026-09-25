Fix select(issues, labels=None, status=None). Match ALL requested labels, case sensitively; labels=None or [] means no label restriction. Apply status when it is not None. Preserve input ordering and do not mutate records. Missing record labels count as empty.

Keep the public API in app.py. Use only the Python standard library. Preserve existing behavior except where this task explicitly changes it.
