class Conflict(Exception):
    pass

def update(store, issue_id, expected_version, changes):
    raise NotImplementedError
