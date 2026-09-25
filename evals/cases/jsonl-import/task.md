Implement parse_jsonl(text) returning (records, errors). Ignore blank lines. Every other line must be a JSON object with id as a positive integer (not bool) and title as a non-whitespace string. Preserve valid records exactly and in order. Reject duplicate IDs among accepted records; an invalid record must not reserve its ID. Continue after bad lines. errors is a list of physical 1-based line numbers for rejected lines.

Keep the public API in app.py. Use only the Python standard library. Preserve existing behavior except where this task explicitly changes it.
