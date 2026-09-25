def reserve(stock, requests):
    totals = {}
    for sku, quantity in requests:
        if isinstance(quantity, bool) or not isinstance(quantity, int) or quantity <= 0:
            raise ValueError('quantity must be a positive integer')
        if sku not in stock:
            raise KeyError(sku)
        totals[sku] = totals.get(sku, 0) + quantity
    if any(total > stock[sku] for sku, total in totals.items()):
        raise ValueError('insufficient stock')
    for sku, total in totals.items():
        stock[sku] -= total
    return dict(stock)
