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


def test_without_a_setting_the_coder_has_the_frameworks_own_instructions(monkeypatch):
    from agent import instructions_source, make_coder
    from pydantic_ai_harness.coder._instructions import coder_instructions

    monkeypatch.delenv('HEATCTL_INSTRUCTIONS_FILE', raising=False)
    assert instructions_source() == {'source': 'framework default'}
    assert 'Leave only the requested change' in coder_instructions(repo_context=True, delegate_tool=None)
    make_coder()  # builds, and leaves the framework's function in place
    from pydantic_ai_harness.coder import _capability

    assert _capability.coder_instructions is coder_instructions


def test_a_file_replaces_the_instructions_and_is_named_in_the_record(monkeypatch, tmp_path):
    from agent import instructions_source, make_coder
    from pydantic_ai_harness.coder import _capability
    from pydantic_ai_harness.coder._instructions import coder_instructions

    file = tmp_path / 'other.txt'
    file.write_text('Do the task.')
    monkeypatch.setenv('HEATCTL_INSTRUCTIONS_FILE', str(file))
    record = instructions_source()
    assert record['source'] == 'other.txt' and len(record['sha256']) == 64
    make_coder()
    assert _capability.coder_instructions is coder_instructions, 'the framework function is put back after the coder is made'
