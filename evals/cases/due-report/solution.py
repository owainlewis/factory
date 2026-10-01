from datetime import date
import re

def parse_date(value):
    if not isinstance(value, str) or not re.fullmatch(r'[0-9]{4}-[0-9]{2}-[0-9]{2}', value):
        raise ValueError('YYYY-MM-DD required')
    return date.fromisoformat(value)

def overdue(issues, today):
    current = parse_date(today)
    rows = []
    for issue in issues:
        if issue['status'] != 'open': continue
        try:
            due = parse_date(issue.get('due'))
        except (ValueError,TypeError):
            continue
        if due < current: rows.append((due,issue['id']))
    return [key for _,key in sorted(rows)]
