import csv
import io

def export_csv(issues):
    out = io.StringIO(newline='')
    writer = csv.writer(out)
    writer.writerow(['id', 'title', 'status'])
    for r in issues:
        writer.writerow([r['id'], r['title'], r['status']])
    return out.getvalue()
