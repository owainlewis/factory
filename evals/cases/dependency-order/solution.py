import heapq

def order_jobs(dependencies):
    remaining = {name:set(needs) for name,needs in dependencies.items()}
    dependents = {name:[] for name in dependencies}
    for name, needs in remaining.items():
        for need in needs:
            if need not in dependencies:
                raise ValueError('unknown prerequisite')
            dependents[need].append(name)
    ready = [name for name,needs in remaining.items() if not needs]
    heapq.heapify(ready)
    result = []
    while ready:
        name = heapq.heappop(ready)
        result.append(name)
        for dependent in dependents[name]:
            remaining[dependent].remove(name)
            if not remaining[dependent]:
                heapq.heappush(ready,dependent)
    if len(result) != len(dependencies):
        raise ValueError('cycle')
    return result
