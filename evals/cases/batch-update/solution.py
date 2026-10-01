def close_many(store, ids):
    keys = list(dict.fromkeys(ids))
    for key in keys:
        if key not in store:
            raise KeyError(key)
    for key in keys:
        store[key]['status'] = 'closed'
    return len(keys)
