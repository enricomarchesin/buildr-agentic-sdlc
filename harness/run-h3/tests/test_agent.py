"""H2 must ask Claude what Claude Code asks it, as H1 did, because the model thinks without limit if it is not told
otherwise; and every other builder the same: reasoning at medium effort, the same output limit."""

from __future__ import annotations

import pytest

from models import (
    BETAS,
    CLAUDE_SETTINGS as SETTINGS,
    CONTEXT_WINDOWS,
    FAMILIES,
    LOCAL_MODELS,
    LOCAL_SETTINGS,
    OTHER_SETTINGS,
    SUMMARY_MODELS,
    settings_for,
)


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
        local = model in LOCAL_MODELS or model in SUMMARY_MODELS.values()
        assert settings_for(model) is (LOCAL_SETTINGS if local else OTHER_SETTINGS)
    assert OTHER_SETTINGS == {'max_tokens': 128_000, 'openai_reasoning_effort': 'medium'}


def test_the_local_models_get_an_output_limit_and_no_reasoning_effort():
    # halogen caps max_tokens at 65,536 and defaults to 8,192 when none is sent; the proxy refuses reasoning_effort for it
    assert LOCAL_SETTINGS == {'max_tokens': 32_768}
    assert set(LOCAL_MODELS) == {'Qwen3.8-27B-ISTA-f16kv:thinking-medium', 'halogen-qwen3.8-flash-next', 'ds4-deepseek-v4-flash'}


def test_a_model_h3_does_not_know_is_refused():
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
    assert deepseek.client.default_headers['x-litellm-tags'] == 'heatctl,harness=H3,run=r,session=s'
    # DeepSeek's profile sends its reasoning back in the field DeepSeek's API asks for
    assert deepseek.profile.get('openai_chat_thinking_field') == 'reasoning_content'
    assert deepseek.profile.get('openai_chat_send_back_thinking_parts') == 'field'
    assert deepseek.profile.get('supports_thinking') is True
    glm = models.build_model(run='r', session='s', model='glm-5.3')
    assert glm.profile.get('openai_chat_thinking_field') == 'reasoning_content'
    for model in FAMILIES:
        assert isinstance(models.build_model(run='r', session='s', model=model), OpenAIChatModel)
    # the local models are reached through the proxy's OpenAI format, thinking read from either field it uses
    for model in LOCAL_MODELS:
        local = models.build_model(run='r', session='s', model=model)
        assert str(local.client.base_url) == 'http://proxy.invalid:4000/v1/'
        assert local.profile.get('context_window') == CONTEXT_WINDOWS[model]
        if model == 'ds4-deepseek-v4-flash':
            # the local DeepSeek: the cloud DeepSeek's profile, its reasoning sent back in the field DeepSeek asks for
            assert local.profile.get('openai_chat_send_back_thinking_parts') == 'field'
            assert local.profile.get('openai_chat_thinking_field') == 'reasoning_content'
            continue
        assert local.profile.get('openai_chat_send_back_thinking_parts', 'auto') == 'auto'
        # the loop's two leading system messages go out as one: the Qwen template accepts a system message only first
        assert local.profile.get('openai_chat_supports_multiple_system_messages') is False


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
