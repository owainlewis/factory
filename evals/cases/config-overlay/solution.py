from copy import deepcopy

def overlay(base, override):
    result = deepcopy(base)
    for key,value in override.items():
        if isinstance(value, dict) and isinstance(result.get(key), dict):
            result[key] = overlay(result[key], value)
        else:
            result[key] = deepcopy(value)
    return result
