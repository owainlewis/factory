def after_page(rows, cursor=None, limit=2):
    if type(limit) is not int or limit < 1: raise ValueError('invalid limit')
    matching = sorted((r for r in rows if cursor is None or r['id'] > cursor), key=lambda r:r['id'])
    items = matching[:limit]
    return items, items[-1]['id'] if len(matching) > limit else None
