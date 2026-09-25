def export_csv(issues):
    return 'id,title,status\n' + ''.join(f"{r['id']},{r['title']},{r['status']}\n" for r in issues)
