Implement overdue(issues, today) returning IDs of open issues with due dates strictly before today, sorted by due date then numeric ID. today and due are YYYY-MM-DD strings interpreted as calendar dates. Ignore missing/None/empty due dates, invalid due dates, and closed issues. An invalid today raises ValueError. Do not mutate input.

Keep the public API in app.py. Use only the Python standard library. Preserve existing behavior except where this task explicitly changes it.
