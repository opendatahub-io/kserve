"""Evaluate pytest marker expressions against a set of selected markers."""

from __future__ import annotations

import re

_TOKEN_RE = re.compile(r"[a-zA-Z_]\w*|[()]")
_KEYWORDS = {"and", "or", "not"}


def extract_positive_markers(expr: str) -> set[str]:
    """Parse a pytest marker expression and return non-negated marker names.

    Handles: simple names, ``and``, ``or``, ``not``, and parentheses.
    """
    tokens = _TOKEN_RE.findall(expr)
    positive: set[str] = set()
    negate_next = False
    for token in tokens:
        if token == "not":
            negate_next = True
        elif token in _KEYWORDS or token in ("(", ")"):
            continue
        else:
            if not negate_next:
                positive.add(token)
            negate_next = False
    return positive


def expression_matches(expr: str, selected_markers: set[str]) -> bool:
    """Conservatively route when any positive marker may be affected.

    ``selected_markers`` is a union across affected tests, not one test's
    complete marker assignment.  Applying boolean ``and``/``not`` semantics
    to that union could therefore suppress a job that contains an affected
    test.  Negative and missing conjunctive markers never exclude a job here.
    """
    positive = extract_positive_markers(expr)
    return bool(positive & selected_markers)
