"""One session: a prompt, an agent, a sandbox, and what is kept of it.

A session ends in one of four ways, and always leaves its record:

    success      the agent declared it was done
    timeout      the run's time was up; the tools' commands in the sandbox were stopped
    limit        a usage limit of the framework stopped it
    error        anything else; the error is in the record

Kept, in the run's archive: `NAME.messages.json`, every message of the conversation, tool calls
and results and thinking included; `NAME.session.json`, how it ended and what it cost.
"""

from __future__ import annotations

import json
import time
from collections.abc import AsyncIterable
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import anyio
from pydantic_ai import RunContext
from pydantic_ai.exceptions import UsageLimitExceeded
from pydantic_ai.messages import AgentStreamEvent, ModelMessage, ModelMessagesTypeAdapter, ModelResponse
from pydantic_ai.usage import RunUsage
from pydantic_ai.workspaces import WorkspaceBackend

from agent import NO_LIMIT, make_agent


@dataclass
class Progress:
    """What a running session has done so far, for a session that does not finish."""

    messages: list[ModelMessage] = field(default_factory=list)
    usage: RunUsage = field(default_factory=RunUsage)

    async def watch(self, ctx: RunContext[Any], events: AsyncIterable[AgentStreamEvent]) -> None:
        async for _ in events:
            pass
        self.messages = list(ctx.messages)
        self.usage = ctx.usage


def cost_usd(messages: list[ModelMessage]) -> float | None:
    """The framework's price for the responses, `None` if it has no price for the model."""
    try:
        return round(sum(float(m.cost().total_price) for m in messages if isinstance(m, ModelResponse)), 6)
    except LookupError:
        return None


def usage_summary(usage: RunUsage) -> dict[str, int]:
    return {
        'input_tokens': usage.input_tokens,
        'cache_write_tokens': usage.cache_write_tokens,
        'cache_read_tokens': usage.cache_read_tokens,
        'output_tokens': usage.output_tokens,
    }


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
    progress = Progress()
    started = time.monotonic()
    status, error, messages, usage = 'success', None, [], RunUsage()
    try:
        with anyio.fail_after(max(deadline - time.time(), 0.001)):
            result = await make_agent(run=run_id, session=name).run(
                prompt, workspace=backend, usage_limits=NO_LIMIT, event_stream_handler=progress.watch
            )
        messages, usage = result.all_messages(), result.usage
    except TimeoutError:
        status = 'timeout'
    except UsageLimitExceeded as exc:
        status, error = 'limit', str(exc)
    except Exception as exc:  # noqa: BLE001 - a session that fails still leaves its record
        status, error = 'error', f'{type(exc).__name__}: {exc}'
    if status != 'success':
        messages, usage = progress.messages, progress.usage

    (archive / f'{name}.messages.json').write_bytes(ModelMessagesTypeAdapter.dump_json(messages, indent=1))
    record = {
        'name': name,
        'status': status,
        'is_error': status != 'success',
        'error': error,
        'requests': usage.requests,
        'duration_ms': round((time.monotonic() - started) * 1000),
        'cost_usd': cost_usd(messages),
        'usage': usage_summary(usage),
    }
    (archive / f'{name}.session.json').write_text(json.dumps(record, indent=1))
    return record
