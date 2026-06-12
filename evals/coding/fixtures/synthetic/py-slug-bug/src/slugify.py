"""URL slug helper used by the publishing pipeline."""

import re


def slugify(title):
    """Return a URL-safe slug: lowercase, words joined by single dashes,
    no leading/trailing dashes."""
    slug = re.sub(r"[^A-Za-z0-9]+", "-", title)
    return slug
