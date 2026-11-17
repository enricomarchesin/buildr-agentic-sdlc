"""The model, reached the way H0 reaches it: through the operator's proxy, with the build key.

The loop runs on the host, so the key stays here and never enters a sandbox. Every request is
tagged for the proxy's spend log, as H0's are, with `harness=H2` and the run and session.

H2 compares models, so a model is reached in one of two ways. Claude goes exactly as in H1: the proxy's
Anthropic format, with the betas and settings Claude Code sends. Every other builder goes through the
proxy's OpenAI-compatible format, which each of their vendors speaks, with its family's profile so the
framework sends back its reasoning the way that family expects. Nothing translates one format into the
other inside the proxy, where a failure would read as the model's.
"""

from __future__ import annotations

import os
import shlex
import subprocess
from pathlib import Path

from anthropic import AsyncAnthropic
from openai import AsyncOpenAI
from pydantic_ai.models import Model
from pydantic_ai.models.anthropic import AnthropicModel, AnthropicModelSettings
from pydantic_ai.models.openai import OpenAIChatModel, OpenAIChatModelSettings
from pydantic_ai.providers.anthropic import AnthropicProvider
from pydantic_ai.providers.deepseek import DeepSeekProvider
from pydantic_ai.providers.litellm import LiteLLMProvider
from pydantic_ai.providers.zai import ZaiProvider

DEFAULT_MODEL = 'claude-sonnet-5-5'
BETAS = 'interleaved-thinking-2025-05-14,effort-2025-11-24'
"""The two of Claude Code's beta features that change how the model behaves: thinking between tool calls, and
effort. The rest of its list is about Claude Code's own features."""
DEFAULT_KEY_COMMAND = f'/usr/bin/security find-generic-password -a {os.environ.get("USER", "")} -s heatctl-build -w'

PROFILES = {
    'gpt-6-luna': lambda: LiteLLMProvider.model_profile('openai/gpt-6-luna'),
    'gemini-4-carbon': lambda: LiteLLMProvider.model_profile('google/gemini-4-carbon'),
    # The maker's own name for this model is `deepseek-flash`, whose model version is DeepSeek-V4.1-Flash-1017.
    'deepseek-v4.1-flash-1017': lambda: DeepSeekProvider.model_profile('deepseek-flash'),
    'glm-5.3': lambda: ZaiProvider.model_profile('glm-5.3'),
    'mimo-v2.6-pro': lambda: LiteLLMProvider.model_profile('mimo-v2.6-pro'),
    # The two families' smaller models, added after the six were built, each with its family's profile.
    'glm-5.3-flash': lambda: ZaiProvider.model_profile('glm-5.3-flash'),
    'mimo-v2.6-flash': lambda: LiteLLMProvider.model_profile('mimo-v2.6-flash'),
    # Tencent's two, added later still. Both answer a tool turn whether their reasoning goes back or not.
    'hy4': lambda: LiteLLMProvider.model_profile('hy4'),
    'hy4-flash': lambda: LiteLLMProvider.model_profile('hy4-flash'),
}
"""The builders other than Claude, as the proxy names them, and the profile the framework uses for each: how
the model takes tool schemas and how its reasoning goes back to it. DeepSeek and GLM get their makers' own,
which send the reasoning back in `reasoning_content`, as DeepSeek requires on every turn with tools; a name
the generic profile reads (`deepseek-v4.1-…` is not `deepseek-v4-…`) would turn DeepSeek's thinking off.
The framework has no profile for Xiaomi or Tencent, so MiMo and Hunyuan get the one OpenAI-compatible endpoints share."""
FAMILIES = tuple(PROFILES)

OUTPUT_LIMIT = 128_000
"""Claude Code's limit for Sonnet, and within what every builder allows: Luna and GLM 128,000, MiMo 131,072,
DeepSeek 384,000, Carbon more."""

CLAUDE_SETTINGS = AnthropicModelSettings(
    max_tokens=OUTPUT_LIMIT,
    anthropic_effort='medium',
    # Thinking adaptive, its text left out of the response: the tokens are still spent and counted. The
    # SDK's type for this setting has no `display`, so it goes in the body as it is.
    extra_body={'thinking': {'type': 'adaptive', 'display': 'omitted'}},
)
"""What Claude Code sends for the same model, read off a request of Claude Code 2.1.309 on 8 November 2026.
Sent unset, the model thinks without a limit and spends the whole response on it before it does anything."""

OTHER_SETTINGS = OpenAIChatModelSettings(max_tokens=OUTPUT_LIMIT, openai_reasoning_effort='medium')
"""The same request for every other builder: reasoning at medium effort, the same output limit. What each
model's service makes of it is read off its logged requests, not assumed."""


def is_claude(model: str) -> bool:
    return model.startswith('claude-')


def model_name(model: str | None = None) -> str:
    return model or os.environ.get('HEATCTL_MODEL', DEFAULT_MODEL)


def settings_for(model: str) -> AnthropicModelSettings | OpenAIChatModelSettings:
    if is_claude(model):
        return CLAUDE_SETTINGS
    if model not in FAMILIES:
        raise ValueError(f'{model} is not one of the builders H2 compares: claude-*, {", ".join(FAMILIES)}')
    return OTHER_SETTINGS


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


def build_model(*, run: str, session: str, model: str | None = None) -> Model:
    """A model whose requests are tagged with the run and session, one client per session."""
    name = model_name(model)
    settings_for(name)  # refuses a model H2 does not compare before any request
    url = os.environ['HEATCTL_MODEL_URL']
    tags = {'x-litellm-tags': f'heatctl,harness=H2,run={run},session={session}'}
    if is_claude(name):
        client = AsyncAnthropic(
            api_key=read_key(), base_url=url, default_headers={**tags, 'anthropic-beta': BETAS}, max_retries=2
        )
        return AnthropicModel(name, provider=AnthropicProvider(anthropic_client=client))
    client = AsyncOpenAI(api_key=read_key(), base_url=url.rstrip('/') + '/v1', default_headers=tags, max_retries=2)
    return OpenAIChatModel(
        name,
        provider=LiteLLMProvider(openai_client=client),
        profile=PROFILES[name](),
    )
