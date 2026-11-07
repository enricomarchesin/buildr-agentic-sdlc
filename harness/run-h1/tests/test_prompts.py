"""H1 must hand the agent the prompts H0 handed Claude Code, byte for byte, and build them the same way."""

from __future__ import annotations

import subprocess
from pathlib import Path

import pytest

from loop import Run

HERE = Path(__file__).resolve().parent.parent
H0 = HERE.parent / 'run-h0'
SHARED = ('prompt.txt', 'review-prompt.txt', 'fix-prompt.txt', 'allowlist.txt', 'denylist.txt')


@pytest.mark.skipif(not H0.is_dir(), reason='run-h0 is not here')
@pytest.mark.parametrize('name', SHARED)
def test_the_file_is_the_same_as_h0s(name):
    assert (HERE / name).read_bytes() == (H0 / name).read_bytes()


def run_in(tmp_path) -> Run:
    return Run(tmp_path, tmp_path, HERE, 'build-sb', 'review-sb')


@pytest.mark.parametrize('name', ['prompt.txt', 'review-prompt.txt'])
def test_a_prompt_is_what_cat_gave_h0(name, tmp_path):
    shell = subprocess.run(['sh', '-c', f'printf %s "$(cat {HERE / name})"'], capture_output=True, text=True).stdout
    assert run_in(tmp_path).prompt(name) == shell


def test_the_fix_prompt_is_what_h0_built_from_the_prompt_and_the_review(tmp_path):
    review = tmp_path / 'review-1.json'
    review.write_text('{"issues": []}\n')
    shell = subprocess.run(
        ['sh', '-c', f'printf %s "$(cat {HERE / "fix-prompt.txt"}; echo; cat {review})"'], capture_output=True, text=True
    ).stdout
    ours = run_in(tmp_path).prompt('fix-prompt.txt') + '\n\n' + review.read_text().rstrip('\n')
    assert ours == shell
