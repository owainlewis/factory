from datetime import date

def overdue(issues, today):
    current = date.fromisoformat(today)
    rows = []
    for issue in issues:
        if issue['status'] != 'open': continue
        try:
            due = date.fromisoformat(issue.get('due'))
        except (ValueError,TypeError):
            continue
        if due < current: rows.append((due,issue['id']))
    return [key for _,key in sorted(rows)]
