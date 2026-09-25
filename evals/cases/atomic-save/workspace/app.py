import json

def save(path, data):
    with open(path, 'w') as f:
        json.dump(data, f)

def load(path):
    with open(path) as f:
        return json.load(f)
