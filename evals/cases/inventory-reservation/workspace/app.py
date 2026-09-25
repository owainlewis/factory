def reserve(stock, requests):
    for sku, quantity in requests:
        stock[sku] -= quantity
    return stock
