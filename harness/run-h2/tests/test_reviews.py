from __future__ import annotations

import json

import reviews

SPEC = """\
Spec version: 1.0

- B1: a rule
- B2: another rule
- F1.AC1: a criterion
"""


def write(tmp_path, data) -> object:
    path = tmp_path / 'review.json'
    path.write_text(data if isinstance(data, str) else json.dumps(data))
    return path


def test_the_clauses_of_a_spec():
    assert reviews.spec_clauses(SPEC) == {'B1', 'B2', 'F1.AC1'}


def test_high_issues_are_counted_whatever_the_case(tmp_path):
    path = write(tmp_path, {'issues': [{'severity': 'High'}, {'severity': 'high'}, {'severity': 'low'}]})
    assert reviews.high_count(path) == 2


def test_an_unreadable_review_counts_minus_one(tmp_path):
    assert reviews.high_count(tmp_path / 'missing.json') == -1
    assert reviews.high_count(write(tmp_path, 'not json')) == -1
    assert reviews.high_count(write(tmp_path, {'no': 'issues'})) == -1
    assert reviews.high_count(write(tmp_path, ['a', 'list'])) == -1


def test_a_clause_named_in_an_issue_is_not_confirmed_whatever_the_lists_say(tmp_path):
    path = write(tmp_path, {
        'issues': [{'severity': 'medium', 'clause': 'B2, see also F1.AC1', 'status': 'broken'}],
        'confirmed': ['B1', 'B2'],
        'unconfirmed': [],
    })
    summary = reviews.summarize('review-1', path, reviews.spec_clauses(SPEC))
    assert summary['confirmed'] == 1
    assert summary['unconfirmed'] == 2
    assert summary['confirmed_despite_an_issue'] == ['B2']
    assert summary['on_neither_list'] == []
    assert summary['medium'] == 1


def test_a_clause_on_neither_list_is_found(tmp_path):
    path = write(tmp_path, {'issues': [], 'confirmed': ['B1'], 'unconfirmed': []})
    assert reviews.summarize('review-1', path, reviews.spec_clauses(SPEC))['on_neither_list'] == ['B2', 'F1.AC1']
