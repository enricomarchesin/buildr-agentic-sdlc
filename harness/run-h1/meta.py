"""The record of a run, for build.sh.

    meta.py DIR RUN SPEC MODEL STATUS    writes DIR/meta.json and prints a summary

The same record as H0's `meta.json`, from H1's own session files. The times come from
DIR/times.txt, one "step seconds" a line, written by build.sh as it goes.
"""

from __future__ import annotations

import hashlib
import json
import subprocess
import sys
from importlib.metadata import version
from pathlib import Path
from typing import Any

import reviews

HERE = Path(__file__).resolve().parent
USAGE_KEYS = ('input_tokens', 'cache_write_tokens', 'cache_read_tokens', 'output_tokens')


def sh(*argv: str) -> str:
    return subprocess.run(argv, capture_output=True, text=True).stdout.strip()


def text(path: Path) -> str | None:
    return path.read_text().strip() if path.exists() else None


def network_hosts(path: Path) -> dict[str, dict[str, int]]:
    try:
        log = json.loads(path.read_text())
    except (OSError, ValueError):
        log = {}
    return {k: {e['host']: e.get('count', 0) for e in log.get(k, [])} for k in ('allowed', 'blocked')}


def network_since_start(d: Path) -> dict[str, dict[str, int]]:
    """Hosts reached during the run, from each sandbox's log at its end less the one at its start."""
    network: dict[str, dict[str, int]] = {'allowed': {}, 'blocked': {}}
    for label in ('build', 'review'):
        before, after = network_hosts(d / f'{label}-network-before.json'), network_hosts(d / f'{label}-network-after.json')
        for kind in after:
            for host, count in after[kind].items():
                if count > before[kind].get(host, 0):
                    network[kind][host] = network[kind].get(host, 0) + count - before[kind].get(host, 0)
    return network


def seconds(d: Path) -> dict[str, float]:
    """Setting up and removing the sandboxes is overhead; the rest of the run is sessions."""
    t = {k: float(v) for k, v in (line.split() for line in (d / 'times.txt').read_text().splitlines() if line.strip())}
    review_setup = t['review-ready'] - t['review-prep']
    return {
        'sessions': round(t['loop-done'] - t['build-ready'] - review_setup, 1),
        'sandbox_overhead': round((t['build-ready'] - t['start']) + review_setup + (t['end'] - t['loop-done']), 1),
    }


def write(d: Path, run: str, spec: Path, model: str, status: str) -> None:
    names = (text(d / 'sessions.txt') or '').split()
    sessions = [json.loads((d / f'{n}.session.json').read_text()) for n in names if (d / f'{n}.session.json').exists()]
    clauses = reviews.spec_clauses(spec.read_text())
    review_summaries = [reviews.summarize(n, d / f'{n}.json', clauses) for n in names if n.startswith('review-')]
    priced = [s['cost_usd'] for s in sessions if s['cost_usd'] is not None]

    spec_bytes = spec.read_bytes()
    spec_version = next(
        (line.split(':', 1)[1].strip() for line in spec_bytes.decode().splitlines() if line.startswith('Spec version:')), None
    )
    sbx_version = sh('sbx', 'version')
    meta: dict[str, Any] = {
        'run_id': run,
        'harness': 'H1',
        'model': model,
        # The harness that ran, and whether its files differed from that commit.
        'harness_commit': sh('git', '-C', str(HERE), 'rev-parse', 'HEAD') or None,
        'harness_modified': bool(sh('git', '-C', str(HERE), 'status', '--porcelain', '--', '.')),
        'from_repo': text(d / 'from.txt'),
        'spec': {'version': spec_version, 'sha256': hashlib.sha256(spec_bytes).hexdigest()},
        'framework': {p: version(p) for p in ('pydantic-ai-slim', 'pydantic-ai-harness')},
        'sbx': sbx_version.splitlines()[0] if sbx_version else None,
        'template': [line for line in sh('sbx', 'template', 'ls').splitlines() if 'claude-code' in line],
        'exit_status': int(status),
        'timed_out': (d / 'timeout').exists(),
        'seconds': seconds(d),
        'result': {
            'num_turns': sum(s['requests'] for s in sessions),
            'total_cost_usd': round(sum(priced), 4),
            'all_sessions_priced': len(priced) == len(sessions),
            **{k: sum(s['usage'][k] for s in sessions) for k in USAGE_KEYS},
        },
        'sessions': sessions,
        'reviews': review_summaries,
        'stop': text(d / 'stop.txt'),
        'reviews_that_changed_the_repo': (text(d / 'review-changed-repo.txt') or '').split(),
        'network': network_since_start(d),
        'commits': int(sh('git', '-C', str(d / 'repo'), 'rev-list', '--count', 'HEAD') or 0),
        'uncommitted': sh('git', '-C', str(d / 'repo'), 'status', '--porcelain').splitlines(),
    }
    (d / 'meta.json').write_text(json.dumps(meta, indent=1))
    print(json.dumps({k: meta[k] for k in ('run_id', 'exit_status', 'timed_out', 'seconds', 'stop')}))
    print('reviews:', json.dumps(review_summaries))
    print('cost:', meta['result']['total_cost_usd'], 'turns:', meta['result']['num_turns'], 'archive:', d)


if __name__ == '__main__':
    if len(sys.argv) != 6:
        sys.exit(__doc__)
    write(Path(sys.argv[1]), sys.argv[2], Path(sys.argv[3]), sys.argv[4], sys.argv[5])
