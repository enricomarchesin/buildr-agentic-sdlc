"""How much does the loop carry before it does any work?

Sends "Say hi." through the same model, proxy and tools an H1 session uses, in an empty directory
on the host, and prints what the first request cost in tokens. H0's counterpart is a Claude Code
session of one turn; see `harness/README.md`.

    uv run python say_hi.py [--cache]
"""

from __future__ import annotations

import sys
import tempfile
import time

from pydantic_ai import Agent
from pydantic_ai.capabilities import LocalWorkspace
from pydantic_ai_harness.coder import Coder

from models import build_model, load_env

WINDOWS = (250_000, 150_000, 128_000, 64_000, 32_000)


def main() -> None:
    load_env()
    settings = {'anthropic_cache_messages': True} if '--cache' in sys.argv else {}
    with tempfile.TemporaryDirectory() as empty:
        agent = Agent(
            build_model(run=f'say-hi-{int(time.time())}', session='say-hi'),
            capabilities=[LocalWorkspace(empty), Coder()],
            model_settings=settings,
        )
        result = agent.run_sync('Say hi.')
    usage = result.usage
    prompt = usage.input_tokens
    print(f'reply: {result.output!r}')
    print(f'requests: {usage.requests}   input: {prompt}   cache write: {usage.cache_write_tokens}   '
          f'cache read: {usage.cache_read_tokens}   output: {usage.output_tokens}')
    for window in WINDOWS:
        print(f'  {window:>7,} window: {100 * prompt / window:5.1f}%')


if __name__ == '__main__':
    main()
