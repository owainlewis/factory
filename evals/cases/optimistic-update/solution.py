from copy import deepcopy

class Conflict(Exception):
    pass

def update(store, issue_id, expected_version, changes):
    row = store[issue_id]
    if row['version'] != expected_version:
        raise Conflict()
    if set(changes) - {'title', 'status'}:
        raise ValueError('unsupported key')
    if 'status' in changes and changes['status'] not in ('open', 'closed'):
        raise ValueError('bad status')
    row.update(deepcopy(changes))
    row['version'] += 1
    return deepcopy(row)
