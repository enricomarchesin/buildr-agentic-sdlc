# 001 The baseline: a vendor coding harness in a sandbox, with a review loop

Version: H0
Axis: baseline
Date: 2026-11-04
Status: accepted

## Context

`process-fit.md`, question 1: a machine can check 33 of the 35 acceptance criteria, in
minutes, by playing the devices on a broker; a person judges the other two. Question 2: the
worst case is physical, a boiler left on or switched too often, so the build runs caged, the
relays carry hard limits of their own, a person approves each build that goes into the house,
and it is accepted only after it has run there, a soak of two to three days.

## Symptom

None. H0 is the baseline: there is nothing before it to attribute against. Every later
version needs a symptom from H0's runs to exist.

## Alternatives rejected

- **The vendor harness alone, no review.** It declares done after its own tests, and in the
  trial builds before the spec froze those tests never ran the program against a broker.
  A review that has to test every clause, and a fix round for what it finds, is the least
  that makes "done" mean the spec, without writing an agent of our own.
- **An agent loop of our own from the start.** It would be a choice made before any failure
  showed what it should fix, with nothing to measure it against. The baseline is the vendor's
  loop, so that each later version owns only what a failure forced.
- **No sandbox, the build on the operator's machine.** The agent installs what it likes and
  runs what it writes. Question 2 asks for a cage before anything else.

## Decision

Claude Code with Sonnet 5.5 builds the whole spec from an empty repository, in a Docker
Sandbox, one microVM per run, and stops when it declares done. A fresh session of the same
harness and model then reviews the build against the spec, testing every clause, and lists
its issues as high, medium or low; while any is high, another session fixes the high ones,
and the review runs again, at most 5 fix rounds. The review runs in a second sandbox where
the repository is read-only. The held-out checks grade the result, three builds per version.

- Files: `harness/run/build.sh`, `prompt.txt`, `review-prompt.txt`, `fix-prompt.txt`,
  `allowlist.txt`, `denylist.txt`, as at commit `e31eb62`, which ran the counted builds.
- Network: what the agent installs from, and the model proxy; never this repository, the
  project's site, the house, or the general web. No web search, no MCP servers. Checked
  before every build, which does not start if a check fails (`harness/README.md`, Network).
- Model access: a LiteLLM proxy on the operator's network, with one budgeted build key that
  stays outside the sandbox.
- Grading model: DeepSeek V4.1 Flash 1017, through the same proxy with a key of its own, for
  the whole series. Sonnet 5.5 cross-checks it: on the first counted build, graded three
  times by each, they gave the same outcome in all 111 runs. A build's outcome for an
  expectation is what most of its three gradings gave (`eval/README.md`).
- Spec: version 1.0, frozen before the counted builds.

## Consequences

- What we now own: 410 non-blank lines in `harness/run/` at `e31eb62`, 265 of them shell in
  `build.sh`; the prompts; the two network lists. The script was later split into `build.sh`,
  `cage.sh`, `loop.sh` and `meta.py`, with no change in what a build does. No process runs
  between builds.
- What regressed: nothing; there is nothing before it.
- What is now possible: a build of the whole spec, reviewed and graded, unattended, in about
  20 minutes and under $2.
- What is still not possible: a build that learns from an earlier one, or a decision about
  the house without a person.
- Numbers, three builds, from the verification set: F1.AC2 1/3; F3.AC4, F4.AC2, F4.AC4,
  F5.AC1, F5.AC3, F6.AC1, F6.AC6, F6.AC7 and C5 3/3. Every build stopped after its first
  review, with no high issue: no fix round, no retry, no manual intervention. Cost per build
  $1.21 to $1.65, sandbox overhead 33 to 35 s. Tests written unprompted: 3, 6 and 10 test
  functions. Cost per accepted change $4.12: the three builds' cost, with one accepted. Build 3
  passed its soak in the house, 6 to 10 November 2026, and is the first accepted build
  (`fixture/DEPLOYED.md`); its source is in `builds/h0-20261104T165650Z-c300/`.

## If your product differs

If a machine can check less of your product, the review is the only check on the rest. Read
the clauses it says it confirmed, not only the issues it raised: a reviewer can mark a clause
confirmed without having tested it.

If the worst a wrong build can do is break your own machine, keep the sandbox and the network
rule anyway. The soak is the part you can drop: it is here because a wrong build heats a real
house.

## Links

- Spec: `spec/SPEC.md`, version 1.0
- Expectations touched: none; they froze with the spec, before the counted builds
- Post:
- Deep dive:
