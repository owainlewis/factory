def close_many(store, ids):
    for key in ids:
        store[key]['status'] = 'closed'
    return len(ids)
