"""A local model's history is summarised before its window fills, by a model that does not think; the coder's own
clearing of old tool results is off for it. The other builders are as in H2."""

from __future__ import annotations

from models import CONTEXT_WINDOWS, LOCAL_MODELS, SUMMARY_MODELS


def _capabilities(monkeypatch, model):
    """The capabilities `make_agent` hands the framework for a model, without any request."""
    import agent
    import models
    from pydantic_ai_harness.step_persistence import InMemoryStepStore

    seen = {}

    def fake_agent(built_model, **kwargs):
        seen.update(kwargs, model=built_model)
        return object()

    monkeypatch.setenv('HEATCTL_MODEL_URL', 'http://proxy.invalid:4000')
    monkeypatch.setattr(models, 'read_key', lambda: 'test-key')
    monkeypatch.setattr(agent, 'Agent', fake_agent)
    agent.make_agent(run='r', name='build', step_id='build', store=InMemoryStepStore(), model=model)
    return seen


def _contains(capability, kind):
    found = []

    def visit(node):
        found.append(node)
        return node

    capability.visit_and_replace(visit)
    return any(isinstance(node, kind) for node in found)


def test_a_local_model_summarises_its_history_and_its_coder_does_not_clear_tool_results(monkeypatch):
    from pydantic_ai_harness.coder import Coder
    from pydantic_ai_harness.compaction import ClearToolResults, ReportContextUsage, SummarizingCompaction
    from pydantic_ai_harness.step_persistence import StepPersistence

    for model in LOCAL_MODELS:
        coder, step, summary, usage = _capabilities(monkeypatch, model)['capabilities']
        assert isinstance(coder, Coder) and not _contains(coder, ClearToolResults)
        # the step record goes before the summary, so what is saved is the summarised history
        assert isinstance(step, StepPersistence) and isinstance(summary, SummarizingCompaction)
        assert isinstance(usage, ReportContextUsage)
        assert (summary.max_fraction, summary.keep_messages) == (0.9, 15)
        assert summary.context_window == CONTEXT_WINDOWS[model] == usage.context_window
        assert summary.model.model_name == SUMMARY_MODELS[model]
        assert summary.model_settings == {
            'max_tokens': 2000,
            'extra_body': {'chat_template_kwargs': {'enable_thinking': False}},
        }
        assert '{messages}' in summary.summary_prompt and 'Ruled out' in summary.summary_prompt


def test_the_first_local_model_is_summarised_by_the_alias_that_does_not_think():
    assert SUMMARY_MODELS['Qwen3.8-27B-ISTA-f16kv:thinking-medium'] == 'Qwen3.8-27B-ISTA-f16kv:reasoning'
    assert SUMMARY_MODELS['halogen-qwen3.8-flash-next'] == 'halogen-qwen3.8-flash-next'
    assert CONTEXT_WINDOWS['Qwen3.8-27B-ISTA-f16kv:thinking-medium'] == 147_456
    assert CONTEXT_WINDOWS['halogen-qwen3.8-flash-next'] == 262_144
    assert SUMMARY_MODELS['ds4-deepseek-v4-flash'] == 'ds4-deepseek-v4-flash'
    assert CONTEXT_WINDOWS['ds4-deepseek-v4-flash'] == 126_000


def test_the_other_builders_keep_the_coders_own_clearing_and_have_no_summary(monkeypatch):
    from pydantic_ai_harness.compaction import ClearToolResults

    caps = _capabilities(monkeypatch, 'deepseek-v4.1-flash-1017')['capabilities']
    assert len(caps) == 2 and _contains(caps[0], ClearToolResults)


def test_a_setting_replaces_the_context_window_for_a_run_that_tries_compaction(monkeypatch):
    import models

    monkeypatch.setenv('HEATCTL_CONTEXT_WINDOW', '8000')
    assert models.context_window('halogen-qwen3.8-flash-next') == 8000
    monkeypatch.delenv('HEATCTL_CONTEXT_WINDOW')
    assert models.context_window('halogen-qwen3.8-flash-next') == 262_144


def test_the_profile_of_a_local_model_carries_its_window(monkeypatch):
    import models

    monkeypatch.setenv('HEATCTL_MODEL_URL', 'http://proxy.invalid:4000')
    monkeypatch.setattr(models, 'read_key', lambda: 'test-key')
    built = models.build_model(run='r', session='s', model='Qwen3.8-27B-ISTA-f16kv:thinking-medium')
    assert built.profile.get('context_window') == 147_456


def test_the_record_says_how_the_history_is_kept_in_the_window():
    import agent

    record = agent.compaction_record('halogen-qwen3.8-flash-next')
    assert record['source'] == 'summarising' and record['at'] == 0.9 and record['keep_messages'] == 15
    assert len(record['prompt_sha256']) == 64
    assert agent.compaction_record('glm-5.3')['source'].startswith('coder default')
