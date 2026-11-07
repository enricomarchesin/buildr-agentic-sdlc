"""A session ends in one of four ways and always leaves its record, with a stand-in model and no sandbox."""

from __future__ import annotations

import json
import time
from collections.abc import AsyncIterator

import anyio
import pytest
from pydantic_ai import Agent
from pydantic_ai.exceptions import ModelServerError
from pydantic_ai.messages import ModelMessage, ToolReturnPart
from pydantic_ai.models.function import AgentInfo, DeltaToolCall, FunctionModel
from pydantic_ai.models.test import TestModel
from pydantic_ai.workspaces import LocalWorkspaceBackend
from pydantic_ai_harness.coder import Coder
from pydantic_ai_harness.step_persistence import StepPersistence

import session


def patch_agent(monkeypatch, model):
    """The session's agent with a stand-in model; the tools and the step log are the real ones."""

    def make(*, run, name, step_id, store, **_):
        capabilities = [
            Coder(sub_agents=False, unrestricted_filesystem=True),
            StepPersistence(store=store, agent_name=name, run_id=step_id),
        ]
        return Agent(model, capabilities=capabilities)

    monkeypatch.setattr(session, 'make_agent', make)


async def run(tmp_path, deadline_in=60.0):
    return await session.run_session(
        archive=tmp_path, run_id='r', name='build', prompt='go', backend=LocalWorkspaceBackend(tmp_path),
        deadline=time.time() + deadline_in,
    )


def events(tmp_path) -> list[str]:
    lines = (tmp_path / 'steps' / 'build' / 'events.jsonl').read_text().splitlines()
    return [json.loads(line)['kind'] for line in lines]


async def test_a_session_that_finishes_leaves_its_messages_its_steps_and_its_record(tmp_path, monkeypatch):
    patch_agent(monkeypatch, TestModel(custom_output_text='done', call_tools=[]))
    record = await run(tmp_path)
    assert record['status'] == 'success' and not record['is_error']
    assert record['requests'] == 1
    assert json.loads((tmp_path / 'build.session.json').read_text())['name'] == 'build'
    messages = json.loads((tmp_path / 'build.messages.json').read_text())
    assert [m['kind'] for m in messages] == ['request', 'response']
    assert events(tmp_path)[0] == 'run_started' and events(tmp_path)[-1] == 'run_completed'


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
    assert events(tmp_path)[-1] == 'run_failed'


async def test_a_session_that_times_out_after_working_keeps_what_it_did(tmp_path, monkeypatch):
    async def work_then_stall(messages: list[ModelMessage], info: AgentInfo) -> AsyncIterator[str | dict[int, DeltaToolCall]]:
        if any(isinstance(p, ToolReturnPart) for m in messages for p in m.parts):
            await anyio.sleep(30)
        yield {0: DeltaToolCall(name='shell', json_args=json.dumps({'command': 'echo made-by-the-agent'}))}

    patch_agent(monkeypatch, FunctionModel(stream_function=work_then_stall))
    record = await run(tmp_path, deadline_in=3.0)
    assert record['status'] == 'timeout'
    assert record['requests'] == 1, 'the request in flight when time ran out left no response, so it is not counted'
    kept = (tmp_path / 'build.messages.json').read_text()
    assert 'made-by-the-agent' in kept, 'the tool call and its result survive the timeout'
    assert 'tool_call_completed' in events(tmp_path)


async def test_a_session_that_fails_leaves_the_error_in_its_record(tmp_path, monkeypatch):
    async def broken(messages: list[ModelMessage], info: AgentInfo) -> AsyncIterator[str]:
        raise RuntimeError('the model went away')
        yield ''

    patch_agent(monkeypatch, FunctionModel(stream_function=broken))
    record = await run(tmp_path)
    assert record['status'] == 'error'
    assert 'the model went away' in record['error']
    assert record['retries'] == [], 'an error that is not the service failing is not retried'


@pytest.fixture
def no_wait(monkeypatch):
    monkeypatch.setattr(session, 'RETRY_AFTER', (0.0, 0.0, 0.0))


def script(*steps):
    """A stand-in model that does what each step says, one per request: a command to run, an error, or text."""
    calls = iter(steps)

    async def stream(messages: list[ModelMessage], info: AgentInfo) -> AsyncIterator[str | dict[int, DeltaToolCall]]:
        step = next(calls)
        if isinstance(step, Exception):
            raise step
        if step.startswith('$'):
            yield {0: DeltaToolCall(name='shell', json_args=json.dumps({'command': step[1:]}))}
        else:
            yield step

    return FunctionModel(stream_function=stream)


def broken_stream() -> ModelServerError:
    return ModelServerError('claude-sonnet-5-5', 'The stream ended before completion.')


async def test_a_transient_error_after_work_resumes_from_where_it_stopped(tmp_path, monkeypatch, no_wait):
    patch_agent(monkeypatch, script('$echo x >> work.log', broken_stream(), 'done'))
    record = await run(tmp_path)
    assert record['status'] == 'success'
    assert len(record['retries']) == 1 and 'stream ended' in record['retries'][0]
    assert (tmp_path / 'work.log').read_text() == 'x\n', 'the work done before the error is not done again'
    kept = json.loads((tmp_path / 'build.messages.json').read_text())
    assert [m['kind'] for m in kept] == ['request', 'response', 'request', 'response']
    assert (tmp_path / 'steps' / 'build-retry-1').is_dir(), 'the retry is its own run in the step log'


async def test_a_transient_error_on_the_first_request_is_retried_from_the_start(tmp_path, monkeypatch, no_wait):
    patch_agent(monkeypatch, script(broken_stream(), 'done'))
    record = await run(tmp_path)
    assert record['status'] == 'success' and len(record['retries']) == 1


async def test_an_error_that_does_not_go_away_ends_the_session_after_three_retries(tmp_path, monkeypatch, no_wait):
    patch_agent(monkeypatch, script('$echo x >> work.log', *[broken_stream()] * 4))
    record = await run(tmp_path)
    assert record['status'] == 'error' and 'ModelServerError' in record['error']
    assert len(record['retries']) == 4, 'the first failure and three retries'
    assert 'echo x' in (tmp_path / 'build.messages.json').read_text(), 'what the session did is kept'
