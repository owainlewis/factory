Fix export_csv(issues) to produce standards-compliant CSV text with a header id,title,status in that order. Preserve commas, double quotes, newlines, empty strings, and Unicode in titles. Empty input still emits the header. Do not mutate records.

Keep the public API in app.py. Use only the Python standard library. Preserve existing behavior except where this task explicitly changes it.
