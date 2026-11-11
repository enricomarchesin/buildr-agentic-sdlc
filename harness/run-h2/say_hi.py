"""How much does the loop carry before it does any work?

Sends "Say hi." through the same model, proxy and tools an H1 session uses, in an empty directory
on the host, and prints what the first request cost in tokens. H0's counterpart is a Claude Code
session of one turn; see `harness/README.md`.

    uv run python say_hi.py
"""

from __future__ import annotations

import tempfile
import time
from pathlib import Path

from pydantic_ai.workspaces import LocalWorkspaceBackend
from pydantic_ai_harness.step_persistence import InMemoryStepStore

from agent import make_agent
from models import load_env

WINDOWS = (250_000, 150_000, 128_000, 64_000, 32_000)


def main() -> None:
    load_env()
    with tempfile.TemporaryDirectory() as empty:
        agent = make_agent(run=f'say-hi-{int(time.time())}', name='say-hi', store=InMemoryStepStore())
        result = agent.run_sync('Say hi.', workspace=LocalWorkspaceBackend(Path(empty)))
    usage = result.usage
    prompt = usage.input_tokens
    print(f'reply: {result.output!r}')
    print(f'requests: {usage.requests}   input: {prompt}   cache write: {usage.cache_write_tokens}   '
          f'cache read: {usage.cache_read_tokens}   output: {usage.output_tokens}')
    for window in WINDOWS:
        print(f'  {window:>7,} window: {100 * prompt / window:5.1f}%')


if __name__ == '__main__':
    main()
