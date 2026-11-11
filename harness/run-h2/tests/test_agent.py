"""H2 must ask Claude what Claude Code asks it, as H1 did, because the model thinks without limit if it is not told
otherwise; and every other builder the same: reasoning at medium effort, the same output limit."""

from __future__ import annotations

import pytest

from models import BETAS, CLAUDE_SETTINGS as SETTINGS, FAMILIES, OTHER_SETTINGS, settings_for


def test_the_output_limit_is_claude_codes():
    assert SETTINGS['max_tokens'] == 128_000


def test_thinking_is_adaptive_with_its_text_left_out():
    assert SETTINGS['extra_body'] == {'thinking': {'type': 'adaptive', 'display': 'omitted'}}


def test_effort_is_medium():
    assert SETTINGS['anthropic_effort'] == 'medium'


def test_the_betas_that_change_the_models_behaviour_are_declared():
    assert set(BETAS.split(',')) == {'interleaved-thinking-2025-05-14', 'effort-2025-11-24'}


def test_claude_gets_claude_codes_settings_and_the_others_reasoning_at_medium():
    assert settings_for('claude-sonnet-5-5') is SETTINGS
    for model in FAMILIES:
        assert settings_for(model) is OTHER_SETTINGS
    assert OTHER_SETTINGS == {'max_tokens': 128_000, 'openai_reasoning_effort': 'medium'}


def test_a_model_h2_does_not_compare_is_refused():
    with pytest.raises(ValueError, match='not one of the builders'):
        settings_for('gpt-6.1-sol')


def test_each_builder_is_reached_in_its_own_format(monkeypatch):
    from pydantic_ai.models.anthropic import AnthropicModel
    from pydantic_ai.models.openai import OpenAIChatModel

    import models

    monkeypatch.setenv('HEATCTL_MODEL_URL', 'http://proxy.invalid:4000')
    monkeypatch.setattr(models, 'read_key', lambda: 'test-key')
    claude = models.build_model(run='r', session='s', model='claude-sonnet-5-5')
    assert isinstance(claude, AnthropicModel)
    deepseek = models.build_model(run='r', session='s', model='deepseek-v4.1-flash-1017')
    assert isinstance(deepseek, OpenAIChatModel)
    assert str(deepseek.client.base_url) == 'http://proxy.invalid:4000/v1/'
    assert deepseek.client.default_headers['x-litellm-tags'] == 'heatctl,harness=H2,run=r,session=s'
    # DeepSeek's profile sends its reasoning back in the field DeepSeek's API asks for
    assert deepseek.profile.get('openai_chat_thinking_field') == 'reasoning_content'
    assert deepseek.profile.get('openai_chat_send_back_thinking_parts') == 'field'
    assert deepseek.profile.get('supports_thinking') is True
    glm = models.build_model(run='r', session='s', model='glm-5.3')
    assert glm.profile.get('openai_chat_thinking_field') == 'reasoning_content'
    for model in FAMILIES:
        assert isinstance(models.build_model(run='r', session='s', model=model), OpenAIChatModel)


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
