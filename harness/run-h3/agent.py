"""The agent of one session: the framework's coding tools and our model, with the limits H0 had.

H0's agent is Claude Code as the vendor ships it. H1's is this, and H2's is H1's with the model
as the variable, so what the agent carries and what it may do are written here and nowhere else.
"""

from __future__ import annotations

import hashlib
import os
from collections.abc import Iterator
from contextlib import contextmanager
from pathlib import Path

from pydantic_ai import Agent
from pydantic_ai.capabilities import AbstractCapability
from pydantic_ai.usage import UsageLimits
from pydantic_ai_harness.coder import Coder
from pydantic_ai_harness.coder import _capability as coder_module
from pydantic_ai_harness.compaction import ClearToolResults, ReportContextUsage, SummarizingCompaction
from pydantic_ai_harness.step_persistence import StepPersistence, StepStore

from models import LOCAL_MODELS, SUMMARY_MODELS, build_model, context_window, model_name, settings_for

NO_LIMIT = UsageLimits(request_limit=None)
"""The framework stops a run at 50 model requests unless told otherwise. H0's only limit is the
run's time, and so is H1's and H2's."""


INSTRUCTIONS_FILE = 'HEATCTL_INSTRUCTIONS_FILE'
"""The name of the setting that replaces the coder's instructions with the text of a file. Unset, the
agent has the framework's, which are part of the loop and what H1 is. Set, a run asks what the
instructions do: the same loop, tools and model with other words. The file's name and hash go in
`meta.json`."""


@contextmanager
def instructions_replaced(text: str) -> Iterator[None]:
    """The coder builds its instructions when it is made, from a function it imports by name."""
    original = coder_module.coder_instructions
    coder_module.coder_instructions = lambda **_: text
    try:
        yield
    finally:
        coder_module.coder_instructions = original


def instructions_source() -> dict[str, str]:
    """Where the coder's instructions come from, for the record of a run."""
    path = os.environ.get(INSTRUCTIONS_FILE)
    if not path:
        return {'source': 'framework default'}
    return {'source': Path(path).name, 'sha256': hashlib.sha256(Path(path).read_bytes()).hexdigest()}


def make_coder() -> Coder:
    path = os.environ.get(INSTRUCTIONS_FILE)
    if not path:
        return Coder(sub_agents=False, unrestricted_filesystem=True)
    with instructions_replaced(Path(path).read_text()):
        return Coder(sub_agents=False, unrestricted_filesystem=True)


SUMMARY_PROMPT_FILE = Path(__file__).with_name('summary-prompt.txt')
COMPACT_AT = 0.9
"""A local model's history is summarised when it passes this share of the model's context window."""
KEEP_MESSAGES = 15
"""The newest messages that stay verbatim when the older ones are summarised."""
SUMMARY_SETTINGS = {'max_tokens': 2000, 'extra_body': {'chat_template_kwargs': {'enable_thinking': False}}}
"""The summary call: at most 2,000 tokens, and without thinking where the server honours the request."""


def without_clearing(coder: Coder) -> Coder:
    """The coder without its own clearing of old tool results. The coder blanks every tool result but the latest few
    once the history passes 70 % of the window, in place; the blank replaces an old result, so a server's prompt cache
    has to read everything after it again. Summarising is what keeps a local model's history in the window."""

    def drop(capability: AbstractCapability[object]) -> AbstractCapability[object] | None:
        return None if isinstance(capability, ClearToolResults) else capability

    cleared = coder.visit_and_replace(drop)
    if not isinstance(cleared, Coder):
        raise RuntimeError('removing the coder\'s clearing of tool results dropped the coding tools')
    return cleared


def compaction(*, run: str, name: str, model: str) -> SummarizingCompaction:
    """Summarise the older history of a local model. The summary comes from the model's summary model (see
    `SUMMARY_MODELS`) and its requests carry their own session tag, `<session>-summary`."""
    return SummarizingCompaction(
        model=build_model(run=run, session=f'{name}-summary', model=SUMMARY_MODELS[model]),
        max_fraction=COMPACT_AT,
        keep_messages=KEEP_MESSAGES,
        summary_prompt=SUMMARY_PROMPT_FILE.read_text(),
        model_settings=SUMMARY_SETTINGS,
        context_window=context_window(model),
    )


def compaction_record(model: str) -> dict[str, object]:
    """How a run's history is kept in the window, for the record of the run."""
    if model not in LOCAL_MODELS:
        return {'source': 'coder default: tool results cleared at 70 % of the framework\'s assumed window'}
    return {
        'source': 'summarising',
        'at': COMPACT_AT,
        'keep_messages': KEEP_MESSAGES,
        'context_window': context_window(model),
        'summary_model': SUMMARY_MODELS[model],
        'summary_settings': SUMMARY_SETTINGS,
        'prompt_sha256': hashlib.sha256(SUMMARY_PROMPT_FILE.read_bytes()).hexdigest(),
    }


def make_agent(*, run: str, name: str, step_id: str, store: StepStore, model: str | None = None) -> Agent:
    """One session's agent.

    The coder gets no delegates: the prompts never ask for any, and a delegate multiplies the
    cost of a run. It may reach any path in its workspace, which is a sandbox, as Claude Code
    could. The workspace itself is given when the session runs. Every step of the session is
    recorded in `store` as it happens, under `step_id`: the session's name, or the name of a retry.

    A local model also summarises its older history before the window fills, and its coder does not
    clear tool results itself. The record of the step goes before the summary in the list: the hooks
    after a run go in reverse, so the saved history is the summarised one.
    """
    name_of_model = model_name(model)
    local = name_of_model in LOCAL_MODELS
    capabilities: list[AbstractCapability[object]] = [
        without_clearing(make_coder()) if local else make_coder(),
        StepPersistence(store=store, agent_name=name, run_id=step_id),
    ]
    if local:
        capabilities += [
            compaction(run=run, name=name, model=name_of_model),
            ReportContextUsage(context_window=context_window(name_of_model)),
        ]
    return Agent(
        build_model(run=run, session=name, model=model),
        capabilities=capabilities,
        model_settings=settings_for(name_of_model),
    )
