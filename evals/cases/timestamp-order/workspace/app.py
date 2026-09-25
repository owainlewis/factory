def newest(issues):
    return sorted(issues, key=lambda row: row['created'], reverse=True)
