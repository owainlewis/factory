from datetime import datetime, timezone

def newest(issues):
    def instant(row):
        value = datetime.fromisoformat(row['created'].replace('Z', '+00:00'))
        return value.replace(tzinfo=timezone.utc) if value.tzinfo is None else value
    return sorted(issues, key=instant, reverse=True)
