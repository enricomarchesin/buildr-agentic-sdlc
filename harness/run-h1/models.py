"""The model, reached the way H0 reaches it: through the operator's proxy, with the build key.

The loop runs on the host, so the key stays here and never enters a sandbox. Every request is
tagged for the proxy's spend log, as H0's are, with `harness=H1` and the run and session.
"""

from __future__ import annotations

import os
import shlex
import subprocess
from pathlib import Path

from anthropic import AsyncAnthropic
from pydantic_ai.models.anthropic import AnthropicModel
from pydantic_ai.providers.anthropic import AnthropicProvider

DEFAULT_MODEL = 'claude-sonnet-5-5'
BETAS = 'interleaved-thinking-2025-05-14,effort-2025-11-24'
"""The two of Claude Code's beta features that change how the model behaves: thinking between tool calls, and
effort. The rest of its list is about Claude Code's own features."""
DEFAULT_KEY_COMMAND = f'/usr/bin/security find-generic-password -a {os.environ.get("USER", "")} -s heatctl-build -w'


def load_env(path: str | None = None) -> None:
    """Read the operator's private settings, `KEY=VALUE` lines, as `build.sh` does; the environment wins."""
    file = Path(path or os.environ.get('HEATCTL_ENV') or Path.home() / '.config/heatctl/build.env')
    if not file.is_file():
        return
    for line in file.read_text().splitlines():
        line = line.strip()
        if line and not line.startswith('#') and '=' in line:
            name, _, value = line.partition('=')
            os.environ.setdefault(name.strip(), value.strip().strip('"\''))


def read_key() -> str:
    command = os.environ.get('HEATCTL_KEY_COMMAND') or DEFAULT_KEY_COMMAND
    return subprocess.run(shlex.split(command), capture_output=True, text=True, check=True).stdout.strip()


def build_model(*, run: str, session: str, model: str | None = None) -> AnthropicModel:
    """A model whose requests are tagged with the run and session, one client per session."""
    url = os.environ['HEATCTL_MODEL_URL']
    client = AsyncAnthropic(
        api_key=read_key(),
        base_url=url,
        default_headers={
            'x-litellm-tags': f'heatctl,harness=H1,run={run},session={session}',
            'anthropic-beta': BETAS,
        },
        max_retries=2,
    )
    return AnthropicModel(
        model or os.environ.get('HEATCTL_MODEL', DEFAULT_MODEL), provider=AnthropicProvider(anthropic_client=client)
    )
