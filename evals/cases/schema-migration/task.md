Implement migrate(document). Version 1 documents contain issues with boolean done; return version 2 with done removed, status open/closed, and labels [] when absent. Preserve other document/issue fields and existing labels. Version 2 returns an independent unchanged copy. Missing or unsupported version raises ValueError. Never mutate the input or share mutable objects with it.

Keep the public API in app.py. Use only the Python standard library. Preserve existing behavior except where this task explicitly changes it.
