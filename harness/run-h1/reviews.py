"""Reading a review: the reviewer's JSON, which the loop and the record both depend on.

A review is `{"issues": [{"severity", "clause", "status", ...}], "confirmed": [...], "unconfirmed": [...]}`
(`review-prompt.txt`). A reviewer is not trusted to keep its own lists consistent, so a clause
named in an issue counts as unconfirmed whatever the lists say.
"""

from __future__ import annotations

import json
import re
from pathlib import Path
from typing import Any

SEVERITIES = ('high', 'medium', 'low')
NOT_TESTED = ('not tested', 'not confirmed')
CLAUSE_IN_SPEC = re.compile(r'^- ((?:[A-Z][0-9]+|F[0-9]+\.AC[0-9]+))(?=:)', re.M)
CLAUSE_IN_TEXT = re.compile(r'[A-Z][0-9]+(?:\.AC[0-9]+)?')


def spec_clauses(spec_text: str) -> set[str]:
    """Every clause and criterion id of the spec, to find what a review left on neither list."""
    return set(CLAUSE_IN_SPEC.findall(spec_text))


def load(path: Path) -> dict[str, Any] | None:
    try:
        review = json.loads(path.read_text())
        review['issues']
        return review
    except (OSError, ValueError, KeyError, TypeError):
        return None


def severity_count(issues: list[dict[str, Any]], severity: str) -> int:
    return sum(1 for issue in issues if str(issue.get('severity', '')).lower() == severity)


def high_count(path: Path) -> int:
    """The review's high issues, -1 if it cannot be read."""
    review = load(path)
    return -1 if review is None else severity_count(review['issues'], 'high')


def summarize(name: str, path: Path, clauses: set[str]) -> dict[str, Any]:
    review = load(path)
    if review is None:
        return {'name': name, 'unreadable': True}
    issues = review['issues']
    in_issues: set[str] = set()
    for issue in issues:
        in_issues |= set(CLAUSE_IN_TEXT.findall(str(issue.get('clause', '')))) & clauses
    listed = set(review.get('confirmed', []))
    confirmed = listed - in_issues
    unconfirmed = set(review.get('unconfirmed', [])) | in_issues
    return {
        'name': name,
        **{severity: severity_count(issues, severity) for severity in SEVERITIES},
        'not_tested': sum(1 for i in issues if str(i.get('status', '')).lower() in NOT_TESTED),
        'confirmed': len(confirmed & clauses),
        'unconfirmed': len(unconfirmed & clauses),
        'confirmed_despite_an_issue': sorted(listed & in_issues),
        'on_neither_list': sorted(clauses - confirmed - unconfirmed),
    }
