def select(issues, labels=None, status=None):
    requested = set(labels or [])
    return [i for i in issues if requested.issubset(i.get('labels', []))
            and (status is None or i['status'] == status)]
