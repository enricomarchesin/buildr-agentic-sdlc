"""The agent of one session: the framework's coding tools and our model, with the limits H0 had.

H0's agent is Claude Code as the vendor ships it. H1's is this, so what the agent carries and
what it may do are written here and nowhere else.
"""

from __future__ import annotations

from pydantic_ai import Agent
from pydantic_ai.usage import UsageLimits
from pydantic_ai_harness.coder import Coder
from pydantic_ai_harness.step_persistence import StepPersistence, StepStore

from models import build_model

MAX_OUTPUT_TOKENS = 32_000
"""Per response. Claude Code's default for the same model, and enough for a file of a thousand lines."""

NO_LIMIT = UsageLimits(request_limit=None)
"""The framework stops a run at 50 model requests unless told otherwise. H0's only limit is the
run's time, and so is H1's."""


def make_agent(*, run: str, name: str, store: StepStore, model: str | None = None) -> Agent:
    """One session's agent.

    The coder gets no delegates: the prompts never ask for any, and a delegate multiplies the
    cost of a run. It may reach any path in its workspace, which is a sandbox, as Claude Code
    could. The workspace itself is given when the session runs. Every step of the session is
    recorded in `store` as it happens, under the session's name.
    """
    return Agent(
        build_model(run=run, session=name, model=model),
        capabilities=[
            Coder(sub_agents=False, unrestricted_filesystem=True),
            StepPersistence(store=store, agent_name=name, run_id=name),
        ],
        model_settings={'max_tokens': MAX_OUTPUT_TOKENS},
    )
