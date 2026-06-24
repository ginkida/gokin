def tally(items, counts={}):
    """Count occurrences of each item into counts and return it. BUG: the
    default dict is shared across calls, so counts accumulate between
    independent calls."""
    for it in items:
        counts[it] = counts.get(it, 0) + 1
    return counts
