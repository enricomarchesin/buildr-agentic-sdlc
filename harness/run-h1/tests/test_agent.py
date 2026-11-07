"""H1 must ask the model what Claude Code asks it, because the model thinks without limit if it is not told otherwise."""

from __future__ import annotations

from agent import SETTINGS
from models import BETAS


def test_the_output_limit_is_claude_codes():
    assert SETTINGS['max_tokens'] == 128_000


def test_thinking_is_adaptive_with_its_text_left_out():
    assert SETTINGS['extra_body'] == {'thinking': {'type': 'adaptive', 'display': 'omitted'}}


def test_effort_is_medium():
    assert SETTINGS['anthropic_effort'] == 'medium'


def test_the_betas_that_change_the_models_behaviour_are_declared():
    assert set(BETAS.split(',')) == {'interleaved-thinking-2025-05-14', 'effort-2025-11-24'}
