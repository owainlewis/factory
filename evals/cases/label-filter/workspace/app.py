def select(issues, labels=None, status=None):
    return [i for i in issues if (not labels or set(labels) & set(i.get('labels', [])))
            and (not status or i['status'] == status)]
