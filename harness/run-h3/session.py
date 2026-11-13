"""One session: a prompt, an agent, a sandbox, and what is kept of it.

A session ends in one of four ways, and always leaves its record:

    success      the agent declared it was done
    timeout      the run's time was up; the tools' commands in the sandbox were stopped
    limit        a usage limit of the framework stopped it
    error        anything else; the error is in the record

A transient error of the model service, such as a stream that breaks halfway, does not end a
session: it is retried from the last point the step log saved, up to three times. Claude Code does
the same, which is why none of H0's builds died of one. Every retry is in the session's record.

Kept, in the run's archive: `steps/NAME/`, written as the session goes, so a session still running
can be followed with `tail -f steps/NAME/events.jsonl`, and one that stops leaves what it did;
`NAME.messages.json`, every message of the conversation, tool calls and results included; and
`NAME.session.json`, how it ended and what it cost.
"""

from __future__ import annotations

import json
import time
from collections.abc import AsyncIterable
from pathlib import Path
from typing import Any

import anyio
from pydantic_ai import RunContext
from pydantic_ai.exceptions import (
    ModelConnectionError,
    ModelRateLimitError,
    ModelRequestTimeout,
    ModelServerError,
    ModelUnavailableError,
    UsageLimitExceeded,
)
from pydantic_ai.messages import AgentStreamEvent, ModelMessage, ModelMessagesTypeAdapter, ModelResponse
from pydantic_ai.workspaces import WorkspaceBackend
from pydantic_ai_harness.step_persistence import FileStepStore

from agent import NO_LIMIT, make_agent

TRANSIENT = (ModelRateLimitError, ModelUnavailableError, ModelServerError, ModelConnectionError, ModelRequestTimeout)
"""What the framework calls an error worth trying again. A rejected request, a bad key or an exhausted
quota is not: trying again cannot change it."""

RETRY_AFTER = (5.0, 15.0, 45.0)
"""Seconds to wait before each retry, so three retries at most."""

KEPT_SNAPSHOTS = 5
"""Every snapshot holds the whole conversation so far, so a long session would write one per tool
batch. The newest few stay, and the newest complete one holds all of it; the event log stays whole."""


def step_store(archive: Path) -> FileStepStore:
    return FileStepStore(archive / 'steps', max_snapshots_per_run=KEPT_SNAPSHOTS)


async def drain(ctx: RunContext[Any], events: AsyncIterable[AgentStreamEvent]) -> None:
    """Asks the framework to stream each response, which a long response needs; nothing is done with the events."""
    async for _ in events:
        pass


def cost_usd(messages: list[ModelMessage]) -> float | None:
    """The framework's price for the responses, `None` if it has no price for the model."""
    try:
        return round(sum(float(m.cost().total_price) for m in messages if isinstance(m, ModelResponse)), 6)
    except LookupError:
        return None


def usage_of(messages: list[ModelMessage]) -> dict[str, int]:
    """What the model was asked for and gave, from the responses; the same whether or not the session finished."""
    usages = [m.usage for m in messages if isinstance(m, ModelResponse)]
    return {
        'requests': len(usages),
        'input_tokens': sum(u.input_tokens for u in usages),
        'cache_write_tokens': sum(u.cache_write_tokens for u in usages),
        'cache_read_tokens': sum(u.cache_read_tokens for u in usages),
        'output_tokens': sum(u.output_tokens for u in usages),
    }


def resumable(messages: list[ModelMessage]) -> list[ModelMessage]:
    """The history to carry on from: without the empty response that the failed request left behind."""
    if messages and isinstance(messages[-1], ModelResponse) and not messages[-1].parts:
        return messages[:-1]
    return messages


async def run_session(
    *,
    archive: Path,
    run_id: str,
    name: str,
    prompt: str,
    backend: WorkspaceBackend,
    deadline: float,
) -> dict[str, Any]:
    """Run one session to its end and write its record. `deadline` is a `time.time()` value."""
    store = step_store(archive)
    started = time.monotonic()
    status, error, messages = 'success', None, []
    history: list[ModelMessage] | None = None
    retries: list[str] = []
    step_id = name
    try:
        while True:
            try:
                with anyio.fail_after(max(deadline - time.time(), 0.001)):
                    result = await make_agent(run=run_id, name=name, step_id=step_id, store=store).run(
                        prompt if history is None else None,
                        message_history=history,
                        conversation_id=name,
                        workspace=backend,
                        usage_limits=NO_LIMIT,
                        event_stream_handler=drain,
                    )
                messages = result.all_messages()
                break
            except TRANSIENT as exc:
                retries.append(f'{type(exc).__name__}: {exc}')
                if len(retries) > len(RETRY_AFTER):
                    raise
                snapshot = await store.latest_snapshot(run_id=step_id, include_interrupted=True)
                history = resumable(snapshot.messages) if snapshot else history
                step_id = f'{name}-retry-{len(retries)}'
                await anyio.sleep(RETRY_AFTER[len(retries) - 1])
    except TimeoutError:
        status = 'timeout'
    except UsageLimitExceeded as exc:
        status, error = 'limit', str(exc)
    except Exception as exc:  # noqa: BLE001 - a session that fails still leaves its record
        status, error = 'error', f'{type(exc).__name__}: {exc}'
    if status != 'success':
        snapshot = await store.latest_snapshot(run_id=step_id, include_interrupted=True)
        messages = resumable(snapshot.messages) if snapshot else (history or [])

    (archive / f'{name}.messages.json').write_bytes(ModelMessagesTypeAdapter.dump_json(messages, indent=1))
    usage = usage_of(messages)
    record = {
        'name': name,
        'status': status,
        'is_error': status != 'success',
        'error': error,
        'retries': retries,
        'requests': usage.pop('requests'),
        'duration_ms': round((time.monotonic() - started) * 1000),
        'cost_usd': cost_usd(messages),
        'usage': usage,
    }
    (archive / f'{name}.session.json').write_text(json.dumps(record, indent=1))
    return record
