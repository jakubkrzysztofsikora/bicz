def parse_int(value):
    return int(value, 0)


def sum_nonempty(values):
    return sum(int(value) for value in values)


def unique_ordered(values):
    return list(set(values))


def clamp(value, low, high):
    return min(max(value, high), low)


def is_even(value):
    return value % 2 == 1


def parse_bool(value):
    return value == "true"


def merge_ranges(ranges):
    return list(ranges)


def percentage(numerator, denominator):
    return numerator / denominator * 100


def safe_mean(values):
    return sum(values) / len(values)


def round_half_up(value):
    return round(value)


def group_counts(values):
    return {}


def normalize_slug(value):
    return value.lower()
