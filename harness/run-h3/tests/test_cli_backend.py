"""What the tool's client writes around a command stays out of the command's output.

A local shell stands in for the tool: it prints a notice like `sbx`'s daily update notice before and after the
command, on standard output, as the command's own output goes by.
"""

from __future__ import annotations

import base64
from collections.abc import Mapping, Sequence

import pytest

from cli_backend import CliBackend, unframed

NOTICE = '✨ A new version of the tool is available — 0.54.0\n'


class NoisyBackend(CliBackend):
    provider = 'noisy'
    tool = 'sh'

    def command_line(self, workdir: str, env: Mapping[str, str], inner: Sequence[str]) -> list[str]:
        noisy = f'printf %s "$1"; shift; cd "$1"; shift; "$@"; s=$?; printf %s {NOTICE!r}; exit $s'
        return ['sh', '-c', noisy, 'sh', NOTICE, workdir, *inner]


@pytest.fixture
def backend(tmp_path):
    return NoisyBackend('local', working_dir=str(tmp_path))


async def test_base64_output_survives_the_notice(backend):
    result = await backend.run('printf hello | base64', shell=True)
    assert result.exit_code == 0
    assert base64.b64decode(result.stdout) == b'hello'
    assert result.stderr.count('A new version') == 2


async def test_exit_code_and_stderr_are_the_command_s(backend):
    result = await backend.run('echo out; echo err >&2; exit 3', shell=True)
    assert (result.exit_code, result.stdout) == (3, 'out\n')
    assert result.stderr.endswith('err\n')


async def test_missing_program_exits_127(backend):
    result = await backend.run(['no-such-program-here'])
    assert result.exit_code == 127
    assert result.stdout == ''


def test_unframed_cases():
    m = '<<cmd:x>>'
    assert unframed(f'pre{m}body{m}post', m) == ('body', 'prepost')
    assert unframed(f'pre{m}cut short', m) == ('cut short', 'pre')
    assert unframed('the tool failed', m) == ('', 'the tool failed')
    assert unframed(f'{m}{m}', m) == ('', '')
