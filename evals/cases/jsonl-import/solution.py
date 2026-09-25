import json

def parse_jsonl(text):
    rows, errors, seen = [], [], set()
    for number,line in enumerate(text.split('\n'),1):
        if not line.strip():
            continue
        try:
            row = json.loads(line)
            if not isinstance(row,dict): raise ValueError()
            key = row.get('id')
            if type(key) is not int or key < 1 or key in seen: raise ValueError()
            if not isinstance(row.get('title'),str) or not row['title'].strip(): raise ValueError()
        except (ValueError,TypeError):
            errors.append(number)
            continue
        seen.add(key); rows.append(row)
    return rows, errors
