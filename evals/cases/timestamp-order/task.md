Fix newest(issues) to sort ISO 8601 created timestamps by actual instant, newest first, accepting Z and numeric offsets. Treat naive timestamps as UTC. For equal instants retain input order. Do not mutate the input list.

Keep the public API in app.py. Use only the Python standard library. Preserve existing behavior except where this task explicitly changes it.
