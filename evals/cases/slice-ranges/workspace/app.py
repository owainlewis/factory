def parse_ranges(text, size):
    result=[]
    for item in text.split(','):
        start,end=item.split('-')
        result.append((int(start),int(end)))
    return result
