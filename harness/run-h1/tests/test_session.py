"""A session ends in one of four ways and always leaves its record, with a stand-in model and no sandbox."""

from __future__ import annotations

import json
import time
from collections.abc import AsyncIterator

import anyio
import pytest
from pydantic_ai import Agent
from pydantic_ai.models.function import AgentInfo, FunctionModel
from pydantic_ai.messages import ModelMessage
from pydantic_ai.models.test import TestModel
from pydantic_ai.workspaces import LocalWorkspaceBackend

import session


def patch_agent(monkeypatch, model):
    monkeypatch.setattr(session, 'make_agent', lambda **_: Agent(model))


async def run(tmp_path, deadline_in=60.0):
    return await session.run_session(
        archive=tmp_path, run_id='r', name='build', prompt='go', backend=LocalWorkspaceBackend(tmp_path),
        deadline=time.time() + deadline_in,
    )


async def test_a_session_that_finishes_leaves_its_messages_and_its_record(tmp_path, monkeypatch):
    patch_agent(monkeypatch, TestModel(custom_output_text='done'))
    record = await run(tmp_path)
    assert record['status'] == 'success' and not record['is_error']
    assert record['requests'] == 1
    assert json.loads((tmp_path / 'build.session.json').read_text())['name'] == 'build'
    messages = json.loads((tmp_path / 'build.messages.json').read_text())
    assert [m['kind'] for m in messages] == ['request', 'response']


async def test_a_session_that_runs_out_of_time_says_so_and_still_leaves_its_record(tmp_path, monkeypatch):
    async def slow(messages: list[ModelMessage], info: AgentInfo) -> AsyncIterator[str]:
        await anyio.sleep(30)
        yield 'never'

    patch_agent(monkeypatch, FunctionModel(stream_function=slow))
    started = time.monotonic()
    record = await run(tmp_path, deadline_in=0.3)
    assert time.monotonic() - started < 5
    assert record['status'] == 'timeout' and record['is_error']
    assert (tmp_path / 'build.messages.json').exists()


async def test_a_session_that_fails_leaves_the_error_in_its_record(tmp_path, monkeypatch):
    async def broken(messages: list[ModelMessage], info: AgentInfo) -> AsyncIterator[str]:
        raise RuntimeError('the model went away')
        yield ''

    patch_agent(monkeypatch, FunctionModel(stream_function=broken))
    record = await run(tmp_path)
    assert record['status'] == 'error'
    assert 'the model went away' in record['error']
