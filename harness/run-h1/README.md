# run-h1

H1: the same run as `../run-h0/`, with the loop ours. A Python agent loop on a pinned framework
(Pydantic AI, with its coding tools) runs on the host and reaches into the sandbox only for its
tools. Everything else is H0's: the model, the spec, the prompts, the sandbox image, the network
rules, the review and fix rounds, the held-out checks that grade a build.

```
build.sh        the run, top to bottom; the same steps as H0's
cage.sh         the sandboxes and their network rules: H0's, but see below
loop.py         the build session, then review and fix rounds until no issue is high
session.py      one session: a prompt, an agent, a sandbox, and what is kept of it (its step log is the framework's)
agent.py        the agent: the framework's coder, our model, the limits
models.py       the model, through the operator's proxy, tagged for its spend log
reviews.py      reading a review's JSON
meta.py         the record of the run, `meta.json`
cli_backend.py  a workspace for the framework that runs each command through a sandbox's tool
sbx_backend.py  that, for Docker Sandboxes
say_hi.py       what the loop carries before it does any work, in tokens
prompt.txt, review-prompt.txt, fix-prompt.txt, allowlist.txt, denylist.txt   H0's, byte for byte
tests/          the harness's own tests
```

## What differs from H0

| | H0 | H1 |
|---|---|---|
| The agent | Claude Code, in the sandbox | the framework's coder, on the host |
| The model key | a placeholder in the sandbox, swapped on the way out | on the host; no sandbox has it |
| The sandbox's route to the model proxy | allowed, one host and port | refused, and checked |
| A transient error of the model service | retried by the vendor harness: 21 times in H0's archives | retried up to three times, from the last point the step log saved; each retry is in the session's record |
| Delegates | the vendor's `Task` tool | none |
| Request limit | the vendor's | none: the run's time is the only limit, as in H0 |
| What a session leaves | the vendor's output stream | every message, a step log written as the session goes (`tail -f steps/NAME/events.jsonl`), and a record |

Shared with H0 and tested to stay so: the three prompts and the two network lists are the same
bytes (`tests/test_prompts.py`), and a prompt is built the way H0's shell built it.

## Running

```
HEATCTL_MODEL_URL=http://host:port harness/run-h1/build.sh [SPEC]
```

The settings are H0's (`build.sh`'s header). `HEATCTL_CHECK_ONLY=1` prepares both sandboxes and
checks their network rules without a session or any model spend. Python runs from `uv.lock`.

## Tests

```
uv run pytest --ignore=tests/test_sbx_backend.py    # seconds, no sandbox, no model
uv run pytest                                       # also the sandbox backend, against real sandboxes
```

The backend tests run the framework's own conformance suite and take a few minutes.
