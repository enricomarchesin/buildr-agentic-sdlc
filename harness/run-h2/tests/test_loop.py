"""The stop rules and the review's guard on the repository, with sessions and sandboxes replaced."""

from __future__ import annotations

import json
import subprocess
from pathlib import Path

import pytest

import loop
from loop import FIX_ROUNDS, Run, Sandboxes

HERE = Path(__file__).resolve().parent.parent
HIGH = json.dumps({'issues': [{'severity': 'high', 'clause': 'B1'}]})
CLEAN = json.dumps({'issues': [{'severity': 'low', 'clause': 'B2'}]})


@pytest.fixture
def run(tmp_path) -> Run:
    repo, archive = tmp_path / 'repo', tmp_path / 'archive'
    repo.mkdir(), archive.mkdir()
    for args in (['init', '-q'], ['add', 'SPEC.md']):
        (repo / 'SPEC.md').write_text('spec\n')
        subprocess.run(['git', '-C', str(repo), *args], check=True)
    subprocess.run(['git', '-C', str(repo), '-c', 'user.name=t', '-c', 'user.email=t@t', 'commit', '-qm', 'spec'], check=True)
    return Run(archive, repo, HERE, 'build-sb', 'review-sb')


class Script:
    """Stands in for the sessions and the review sandbox: reviews are read from a list, in order."""

    def __init__(self, reviews: list[str | None], on_review=None, timeout_after: str | None = None):
        self.reviews, self.calls, self.on_review, self.timeout_after = list(reviews), [], on_review, timeout_after
        self.run: Run | None = None

    async def session(self, name: str, prompt: str, sandbox: str) -> None:
        self.calls.append((name, sandbox, prompt))
        if name.startswith('review-') and self.on_review:
            self.on_review(self.run)
        if name == self.timeout_after:
            self.run.timed_out.write_text('timeout\n')

    async def read_review(self, sandbox: str) -> str | None:
        return self.reviews.pop(0)

    async def clear_review(self, sandbox: str) -> None:
        pass

    @property
    def names(self) -> list[str]:
        return [name for name, _, _ in self.calls]

    def sandboxes(self) -> Sandboxes:
        return Sandboxes(read_review=self.read_review, clear_review=self.clear_review)


async def go(run: Run, script: Script) -> str:
    script.run = run
    return await loop.rounds(run, script.session, script.sandboxes())


async def test_it_stops_at_the_first_review_without_a_high_issue(run):
    script = Script([CLEAN])
    assert await go(run, script) == 'no high issues'
    assert script.names == ['review-1']


async def test_a_high_issue_goes_to_a_fix_session_in_the_build_sandbox_and_is_reviewed_again(run):
    script = Script([HIGH, CLEAN])
    assert await go(run, script) == 'no high issues'
    assert script.names == ['review-1', 'fix-1', 'review-2']
    assert [sandbox for _, sandbox, _ in script.calls] == ['review-sb', 'build-sb', 'review-sb']
    assert HIGH in script.calls[1][2], 'the fix session is given the review'
    assert HIGH not in script.calls[2][2], 'a review sees no earlier review'


async def test_it_gives_up_after_the_fix_rounds(run):
    script = Script([HIGH] * (FIX_ROUNDS + 1))
    assert await go(run, script) == f'{FIX_ROUNDS} fix rounds done'
    assert script.names.count('fix-1') == 1 and sum(n.startswith('fix-') for n in script.names) == FIX_ROUNDS
    assert sum(n.startswith('review-') for n in script.names) == FIX_ROUNDS + 1


@pytest.mark.parametrize('result', [None, 'not json', '{"no": "issues"}'])
async def test_an_unreadable_review_stops_the_loop(run, result):
    script = Script([result])
    assert await go(run, script) == 'review 1 wrote no readable result'
    assert script.names == ['review-1']


async def test_the_time_being_up_stops_the_loop_before_a_fix(run):
    script = Script([HIGH], timeout_after='review-1')
    assert await go(run, script) == 'time is up'
    assert script.names == ['review-1']


async def test_a_reviewer_that_changes_the_repository_is_recorded_and_undone(run):
    def tamper(run: Run) -> None:
        (run.repo / 'SPEC.md').write_text('changed by the reviewer\n')
        (run.repo / 'new.go').write_text('package x\n')

    script = Script([CLEAN], on_review=tamper)
    await go(run, script)
    assert (run.repo / 'SPEC.md').read_text() == 'spec\n'
    assert not (run.repo / 'new.go').exists()
    assert (run.archive / 'review-changed-repo.txt').read_text() == 'review-1\n'
    patch = (run.archive / 'review-1-changes.patch').read_text()
    assert 'changed by the reviewer' in patch and 'new.go' in patch


async def test_a_reviewer_that_changes_nothing_leaves_no_trace(run):
    await go(run, Script([CLEAN]))
    assert not (run.archive / 'review-changed-repo.txt').exists()


def log(run: Run, fmt: str) -> list[str]:
    done = subprocess.run(['git', '-C', str(run.repo), 'log', f'--format={fmt}'], capture_output=True, text=True, check=True)
    return done.stdout.splitlines()


def status(run: Run) -> str:
    return subprocess.run(['git', '-C', str(run.repo), 'status', '--porcelain'], capture_output=True, text=True, check=True).stdout


def sessions_that_write(run, monkeypatch):
    """`live_session` with the session itself replaced by one that edits the repository."""

    async def write(*, archive, run_id, name, prompt, backend, deadline):
        (run.repo / 'SPEC.md').write_text(f'changed by {name}\n')
        (run.repo / f'{name}.go').write_text('package main\n')
        return {'status': 'success'}

    monkeypatch.setattr(loop, 'run_session', write)
    monkeypatch.setattr(loop, 'SbxBackend', lambda *a, **k: None)
    return loop.live_session(run, 'r', deadline=0.0)


async def test_a_build_or_fix_session_leaves_its_work_committed_by_the_harness(run, monkeypatch):
    session = sessions_that_write(run, monkeypatch)
    await session('build', 'p', run.build_sandbox)
    await session('fix-1', 'p', run.build_sandbox)
    assert log(run, '%s') == ['fix-1: the working tree as the session left it', 'build: the working tree as the session left it', 'spec']
    assert log(run, '%an')[:2] == ['heatctl-harness', 'heatctl-harness']
    assert status(run) == '', 'nothing left uncommitted'


async def test_a_review_session_is_not_committed(run, monkeypatch):
    session = sessions_that_write(run, monkeypatch)
    await session('review-1', 'p', run.review_sandbox)
    assert log(run, '%s') == ['spec']


def test_nothing_to_commit_makes_no_commit(run):
    loop.commit_session(run, 'fix-1')
    assert log(run, '%s') == ['spec']


def test_ignored_files_stay_out_and_the_repositorys_hooks_do_not_run(run):
    (run.repo / '.gitignore').write_text('bin/\n')
    (run.repo / 'bin').mkdir()
    (run.repo / 'bin' / 'heatctl').write_text('binary\n')
    hook = run.repo / '.git' / 'hooks' / 'pre-commit'
    hook.write_text('#!/bin/sh\nexit 1\n')
    hook.chmod(0o755)
    loop.commit_session(run, 'build')
    files = subprocess.run(['git', '-C', str(run.repo), 'show', '--name-only', '--format=', 'HEAD'], capture_output=True, text=True).stdout.split()
    assert files == ['.gitignore'], 'the build output stays out; the failing hook did not stop the commit'


def test_the_tool_state_the_shell_leaves_in_the_repository_is_named_for_removal(run):
    """The directory is the framework's; if it renames it, this fails and the deliverable would carry the new one."""
    from pydantic_ai_harness._workspace import METADATA_DIR

    assert loop.TOOL_STATE == METADATA_DIR
