"""The framework's conformance suite, against real Docker Sandboxes made from the kit image H0 uses.

Slow: each sandbox takes about half a minute to make. Skipped when `sbx` is not installed.
The backend attaches to a sandbox the cage made and never makes or removes one, so the rules
about creating on first use are covered by a factory that attaches, and destroying by `sbx rm`.
"""

from __future__ import annotations

import os
import secrets
import shutil
import subprocess

import pytest
from pydantic_ai.workspaces.conformance import WorkspaceBackendSuite

from sbx_backend import SbxBackend

pytestmark = pytest.mark.skipif(shutil.which('sbx') is None, reason='sbx is not installed')

ENV = {**os.environ, 'SBX_NO_TELEMETRY': '1'}


def make_sandbox(parent) -> tuple[str, str]:
    repo = os.path.realpath(parent.mktemp('repo'))
    name = f'h1-conformance-{secrets.token_hex(3)}'
    made = subprocess.run(
        ['sbx', 'create', '--name', name, '--pull', 'never', 'claude', repo], capture_output=True, text=True, env=ENV
    )
    assert made.returncode == 0, made.stderr
    return name, repo


def remove_sandbox(name: str) -> None:
    subprocess.run(['sbx', 'rm', '-f', name], capture_output=True, env=ENV)


@pytest.fixture(scope='module')
def sandbox(tmp_path_factory):
    name, repo = make_sandbox(tmp_path_factory)
    try:
        yield name, repo
    finally:
        remove_sandbox(name)


@pytest.fixture
def made(tmp_path_factory):
    """Sandboxes a test made for itself, removed afterwards whether or not the test removed them."""
    names: list[str] = []
    yield names, tmp_path_factory
    for name in names:
        remove_sandbox(name)


class TestSbxBackend(WorkspaceBackendSuite):
    @pytest.fixture
    def backend(self, sandbox):
        name, repo = sandbox
        return SbxBackend(name, working_dir=repo)

    @pytest.fixture
    def attach_backend(self, sandbox):
        _, repo = sandbox
        return lambda ref: SbxBackend(ref.id, working_dir=repo)

    @pytest.fixture
    def destructive_backend(self, made):
        names, factory = made

        def build() -> SbxBackend:
            name, repo = make_sandbox(factory)
            names.append(name)
            return SbxBackend(name, working_dir=repo)

        return build

    @pytest.fixture
    def destroy_environment(self):
        async def destroy(backend: SbxBackend) -> None:
            remove_sandbox(backend.name)

        return destroy
