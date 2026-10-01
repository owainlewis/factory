import re

def parse_ranges(text, size):
    if isinstance(size,bool) or not isinstance(size,int) or size<0 or not isinstance(text,str):
        raise ValueError('invalid input')
    ranges=[]
    for item in text.split(','):
        match=re.fullmatch(r'([0-9]*)-([0-9]*)',item.strip(' \t'))
        if not match or not any(match.groups()):
            raise ValueError('invalid range')
        first,last=match.groups()
        if first:
            start=int(first)
            end=int(last) if last else size-1
            if last and start>end:
                raise ValueError('reversed range')
        else:
            count=int(last)
            start=max(0,size-count)
            end=size-1
        end=min(end,size-1)
        if start<=end:
            ranges.append((start,end))
    merged=[]
    for start,end in sorted(ranges):
        if merged and start<=merged[-1][1]+1:
            merged[-1]=(merged[-1][0],max(merged[-1][1],end))
        else:
            merged.append((start,end))
    return merged
