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
from pydantic_ai.usage import UsageLimits
from pydantic_ai_harness.coder import Coder
from pydantic_ai_harness.coder import _capability as coder_module
from pydantic_ai_harness.step_persistence import StepPersistence, StepStore

from models import build_model, model_name, settings_for

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


def make_agent(*, run: str, name: str, step_id: str, store: StepStore, model: str | None = None) -> Agent:
    """One session's agent.

    The coder gets no delegates: the prompts never ask for any, and a delegate multiplies the
    cost of a run. It may reach any path in its workspace, which is a sandbox, as Claude Code
    could. The workspace itself is given when the session runs. Every step of the session is
    recorded in `store` as it happens, under `step_id`: the session's name, or the name of a retry.
    """
    return Agent(
        build_model(run=run, session=name, model=model),
        capabilities=[
            make_coder(),
            StepPersistence(store=store, agent_name=name, run_id=step_id),
        ],
        model_settings=settings_for(model_name(model)),
    )
