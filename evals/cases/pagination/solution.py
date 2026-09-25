def page(items, number, size):
    if type(number) is not int or type(size) is not int or number < 1 or size < 1:
        raise ValueError('positive integers required')
    start = (number - 1) * size
    return items[start:start + size]
