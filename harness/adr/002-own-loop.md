# 002 An agent loop of our own, on a Python framework, in place of the vendor harness

Version: H1
Axis: control
Date: 2026-11-08
Status: proposed

## Context

`process-fit.md`, question 1: a machine can check 33 of the 35 acceptance criteria, in minutes, by playing
the devices on a broker. Question 2: the worst case is physical, so the cage, the hard limits on the relays
and the soak stay as ADR 001 left them. What changes here is who runs the agent, and what the model is told
and given each time it is asked.

## Symptom

Two measured things, and some preferences.

- **The vendor harness carries a lot before it does anything, and none of it is ours to change.** "Say hi."
  costs 23,478 tokens of prompt on H0, three runs within 11 tokens of each other: 9.4% of a 250k window,
  15.7% of a 150k one. Its system prompt is a small part of that, about 1,500 tokens; the largest part is the
  definitions of its tools, most of which a build never uses. A session ends with 57k to 93k tokens of context, so on a smaller window the fixed part
  matters more than the work.
- **We cannot tell what it does for us until we leave it.** The vendor's loop retries a stream that breaks and
  asks the model for a fixed amount of thinking; neither is visible in its output.

The preferences, named as such: to learn what a loop takes when it is built on a mature framework, to have
tooling for loops less straightforward than this one, and code in a language that is easier to read and test
than shell. The measured part is the reason for the version. The rest only made it worth doing.

## Alternatives rejected

- **Keep the vendor harness.** The baseline. It works: one build in three was accepted. It left the weight
  and the unseen behaviour above as they were.
- **Use the framework's own sandboxes.** Neither fits the cage: one has only an on-or-off network switch
  and mounts nothing, the other is a plain container that shares the host's kernel and reaches the local
  network. Changing the isolation as well would change two variables at once. The framework lets us write a
  workspace backend, so the loop runs on the host and only its tools reach into the sandbox ADR 001 chose.
- **The framework's code mode**, where the model writes one script that calls its tools. It runs in a
  restricted Python with no filesystem, and a build needs a Go toolchain.

## Decision

The loop runs on the host, on a Python agent framework (Pydantic AI, with its coding tools), and the model,
spec, prompts, network lists, review and fix rounds and held-out checks are H0's. Only who runs the agent
changes. Three builds per version.

- **The agent** (`harness/run-h1/agent.py`): the framework's coder with no delegates and free to reach any
  path in its sandbox, as Claude Code could. Its tools are the biggest difference from H0: six (read, write
  and edit a file, list files, search, a shell) against Claude Code's many, each described at length.
- **What the model is asked.** Read off a request of Claude Code and set to match: adaptive thinking with the
  text left out of the response, effort medium, 128,000 output tokens, streaming, and the two beta features
  that change behaviour. Sent unset, the model thought without a limit and spent a whole response on it
  before acting.
- **Retry.** A transient error of the model service (a rate limit, an unavailable service, a dropped stream)
  is retried up to three times, resuming from the last point the session's step log saved.
- **The record.** Each session writes the framework's step log as it goes, so a running session can be
  followed with `tail -f`, and its whole conversation when it ends.
- **The sandbox.** `cage.sh` is H0's, with one difference: the loop holds the build key on the host, so no
  sandbox has a key or a route to the model proxy, and the cage checks that the proxy is refused. A control
  that fails is asked up to six times before it counts, and the denylist is asked once.
- **Prompts and network lists** are H0's bytes, and a test fails if they drift.
- **Pinned:** `pydantic-ai-harness` 0.66.0 on `pydantic-ai-slim` 2.66.0, Python 3.12, in `uv.lock`.
- **The counted builds** ran on commit `8b3ed69`. The code now includes later changes that do not touch what
  the model is asked: the cage's retry of a failing control, a setting that names the GitHub control, and a
  setting that replaces the coder's instructions with a file.

## Consequences

- **What we now own:** 1,086 non-blank lines in `harness/run-h1/` (758 Python, 227 shell, 14 config, 87 of the
  prompts and lists copied from H0), against 485 in `harness/run-h0/` today, and 354 lines of tests where H0
  had none. A framework at a 0.x version, whose API may change between minor releases. No process runs
  between builds.
- **What regressed, plainly.** Three builds, spec 1.0, Sonnet 5.5:
  - **Acceptance:** 0 of 3 builds pass every machine expectation, against 1 of 3 for H0. All three fail
    F1.AC2 (a type error names a line and not the key; a channel of 1.5 is accepted). Two of H0's
    three builds failed the same cases, so three builds cannot tell the two apart.
  - **Cost and time:** $1.54, $4.32 and $1.66 a build and 24, 83 and 36 minutes of sessions, against $1.21
    to $1.65 and 17 to 21 minutes for H0. The $4.32 build took three fix rounds.
  - **Tests:** none left in any repository, against 3, 6 and 10 test functions for H0.
  - **Context:** peak 59k to 67k in builds and 94k to 119k in reviews, against 57k to 93k for H0, because
    nothing clears old tool results.
  - **Found by running it, not by design:** the first attempts failed on things the vendor harness did for
    us, a dropped stream and the thinking settings. Both are fixed in H1 and neither was visible until the
    loop was ours.
- **What is now possible:** the first request is 3,529 tokens, a sixth of H0's, with the tools and
  instructions in one place we can change. The loop's stop rules, retry and record are tested in seconds
  (36 tests). The instructions can be replaced from a file to ask what they do. When they were removed,
  every build left tests (6 of 6, against none of three with them) and the scratch end-to-end check fell
  from three builds to one in six. The tools stayed the framework's six in those runs, and Claude Code's
  own tool descriptions were not there, so the result says what the instructions do and not what Claude
  Code would do.
- **What is still not possible:** a claim that owning the loop makes the code better. On the held-out checks it
  did not, within what three builds show. And a review whose labels follow its own prompt: the prompt calls an
  unmet requirement high and gives `validate` accepting what section 4 rejects as its example, yet of the three
  counted builds only one had its reviewers report the channel-id fault that is that example (first as
  medium, then high after two reviews), and the loop acts on the labels it is given.
- **Numbers**, from `CHANGELOG.md`: verification set, 3 builds: F1.AC2 0/3, the other nine 3/3. Cost per
  accepted change: none accepted. Retries: 0 model errors retried; fix rounds 0, 3 and 0. Manual
  interventions: 0. Tests written unprompted: 0, 0 and 0.

## If your product differs

Before you leave a vendor harness, capture the requests it sends and match them: its thinking, effort,
output limit and retry. Your first runs will be worse for reasons that have nothing to do with owning the
loop, and you will be tempted to put them down to it.

The framework's default instructions are part of your loop. They decide whether the agent leaves tests and
how it checks its work. Read them as you would read a prompt you wrote.

If a wrong build can only break your own machine, the cage and the soak are the parts to drop; the lesson
about requests applies either way.

## Links

- Spec: `spec/SPEC.md`, version 1.0
- Expectations touched: none; F1.AC2 fails as it did in H0, and was not changed
- Post:
- Deep dive:
