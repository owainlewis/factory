from copy import deepcopy

def migrate(document):
    if document.get('version') not in (1,2):
        raise ValueError('unsupported version')
    result = deepcopy(document)
    if result['version'] == 1:
        for row in result['issues']:
            row['status'] = 'closed' if row.pop('done') else 'open'
            row.setdefault('labels', [])
        result['version'] = 2
    return result
