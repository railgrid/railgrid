def clamp(value, lower, upper):
    if lower > upper:
        raise ValueError("lower bound must not exceed upper bound")
    return min(upper, max(lower, value))
