"""Parse a comma-separated list of ports and inclusive port ranges."""


def parse_ports(value: str) -> list[int]:
    ports = set()
    for part in value.split(","):
        bounds = part.strip().split("-")
        if len(bounds) > 2 or any(
            not bound.isascii() or not bound.isdecimal() for bound in bounds
        ):
            raise ValueError("expected a port or range")
        start = int(bounds[0])
        end = int(bounds[-1])
        if not 1 <= start <= end <= 65535:
            raise ValueError("ports must be in 1..65535 and ranges must be ascending")
        ports.update(range(start, end + 1))
    return sorted(ports)
