"""H1's loop: a build session, then review and fix rounds until no issue is high.

    loop.py build  ...    the build session, in the build sandbox
    loop.py rounds ...    review, and fix the high issues, until none is high or the cap

`build.sh` runs the two with the review sandbox made in between. Both share the run's time
budget, a `deadline` kept in the archive, and no session starts once it has passed. The rules are
H0's (`harness/README.md`, Gates): every review is a fresh session that sees no earlier review;
a fix session sees the review before it; medium and low issues are recorded and never fixed.
"""

from __future__ import annotations

import argparse
import os
import json
import shutil
import subprocess
import sys
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from pathlib import Path

import anyio

import reviews
from models import load_env
from sbx_backend import SbxBackend
from session import run_session

FIX_ROUNDS = 5
TOOL_STATE = '.pydantic-ai-harness'
"""Where the framework's shell tool keeps its job logs: in the working directory, which is the repository.
It ignores itself in git, and is of no use once a session is over, so it does not stay in the deliverable."""
REVIEW_FILE = '/tmp/heatctl-review.json'
"""Where the reviewer writes its result, inside the review sandbox (`review-prompt.txt`)."""

EXIT_CODES = {'success': 0, 'timeout': 124}
"""The build session's exit status in the record, as H0's was the vendor harness's."""

Session = Callable[[str, str, str], Awaitable[None]]
"""name, prompt, sandbox"""


@dataclass(frozen=True)
class Run:
    archive: Path
    repo: Path
    here: Path
    build_sandbox: str
    review_sandbox: str

    def prompt(self, file: str) -> str:
        # What `$(cat file)` gave H0: the file without its trailing newlines.
        return (self.here / file).read_text().rstrip('\n')

    @property
    def timed_out(self) -> Path:
        return self.archive / 'timeout'


@dataclass
class Sandboxes:
    """The two things a review needs from its sandbox, which a test replaces."""

    read_review: Callable[[str], Awaitable[str | None]]
    clear_review: Callable[[str], Awaitable[None]]


def git(repo: Path, *args: str, index: Path | None = None) -> str:
    env = {**os.environ, **({'GIT_INDEX_FILE': str(index)} if index else {})}
    done = subprocess.run(['git', '-C', str(repo), *args], capture_output=True, text=True, env=env)
    return done.stdout.strip()


def snapshot(run: Run) -> str:
    """The working tree as a git tree, untracked files included, ignored ones not."""
    index = run.archive / '.snapshot-index'
    git(run.repo, 'add', '-A', '.', index=index)
    tree = git(run.repo, 'write-tree', index=index)
    index.unlink(missing_ok=True)
    return tree


def undo(run: Run, head: str, before: str, after: str) -> None:
    """Put the repository back as it was before a review that changed it."""
    git(run.repo, 'reset', '-q', head)
    for added in git(run.repo, 'diff', '--name-only', '--diff-filter=A', before, after).splitlines():
        (run.repo / added).unlink(missing_ok=True)
    git(run.repo, 'checkout', '-q', before, '--', '.')
    git(run.repo, 'reset', '-q')


async def review(run: Run, session: Session, sandboxes: Sandboxes, n: int) -> None:
    """One review. The repository is read-only to the reviewer; should it change anyway, that
    is recorded, the change kept as a patch, and undone."""
    head, before = git(run.repo, 'rev-parse', 'HEAD'), snapshot(run)
    await sandboxes.clear_review(run.review_sandbox)
    await session(f'review-{n}', run.prompt('review-prompt.txt'), run.review_sandbox)
    result = await sandboxes.read_review(run.review_sandbox)
    if result is not None:
        (run.archive / f'review-{n}.json').write_text(result)
    after = snapshot(run)
    if git(run.repo, 'rev-parse', 'HEAD') != head or after != before:
        with (run.archive / 'review-changed-repo.txt').open('a') as changed:
            changed.write(f'review-{n}\n')
        patch = git(run.repo, 'log', '--format=commit %h %s', f'{head}..HEAD') + '\n' + git(run.repo, 'diff', before, after)
        (run.archive / f'review-{n}-changes.patch').write_text(patch)
        undo(run, head, before, after)


async def rounds(run: Run, session: Session, sandboxes: Sandboxes) -> str:
    """Review, and fix the high issues, until none is high. Returns why it stopped."""
    n = 0
    while True:
        n += 1
        await review(run, session, sandboxes, n)
        result = run.archive / f'review-{n}.json'
        high = reviews.high_count(result)
        if run.timed_out.exists():
            return 'time is up'
        if high < 0:
            return f'review {n} wrote no readable result'
        if high == 0:
            return 'no high issues'
        if n > FIX_ROUNDS:
            return f'{FIX_ROUNDS} fix rounds done'
        await session(f'fix-{n}', run.prompt('fix-prompt.txt') + '\n\n' + result.read_text().rstrip('\n'), run.build_sandbox)


def live_session(run: Run, run_id: str, deadline: float) -> Session:
    """Sessions that call the model and work in the real sandboxes."""

    async def session(name: str, prompt: str, sandbox: str) -> None:
        if run.timed_out.exists():
            return
        with (run.archive / 'sessions.txt').open('a') as names:
            names.write(name + '\n')
        record = await run_session(
            archive=run.archive,
            run_id=run_id,
            name=name,
            prompt=prompt,
            backend=SbxBackend(sandbox, working_dir=str(run.repo)),
            deadline=deadline,
        )
        shutil.rmtree(run.repo / TOOL_STATE, ignore_errors=True)
        if record['status'] == 'timeout':
            run.timed_out.write_text('timeout\n')

    return session


def live_sandboxes(run: Run) -> Sandboxes:
    def backend(sandbox: str) -> SbxBackend:
        return SbxBackend(sandbox, working_dir='/')

    async def read_review(sandbox: str) -> str | None:
        done = await backend(sandbox).run(['cat', REVIEW_FILE])
        return done.stdout if done.exit_code == 0 else None

    async def clear_review(sandbox: str) -> None:
        await backend(sandbox).run(['rm', '-f', REVIEW_FILE])

    return Sandboxes(read_review=read_review, clear_review=clear_review)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('stage', choices=['build', 'rounds'])
    parser.add_argument('--archive', type=Path, required=True)
    parser.add_argument('--repo', type=Path, required=True)
    parser.add_argument('--run', required=True)
    parser.add_argument('--build-sandbox', required=True)
    parser.add_argument('--review-sandbox', required=True)
    parser.add_argument('--deadline', type=float, required=True, help='time.time() value after which no session runs')
    args = parser.parse_args()

    load_env()
    here = Path(__file__).resolve().parent
    run = Run(args.archive, args.repo, here, args.build_sandbox, args.review_sandbox)
    session = live_session(run, args.run, args.deadline)

    async def go() -> int:
        if args.stage == 'rounds':
            stop = await rounds(run, session, live_sandboxes(run))
            (run.archive / 'stop.txt').write_text(stop + '\n')
            return 0
        await session('build', run.prompt('prompt.txt'), run.build_sandbox)
        status = json.loads((run.archive / 'build.session.json').read_text())['status']
        return EXIT_CODES.get(status, 1)

    sys.exit(anyio.run(go))


if __name__ == '__main__':
    main()
