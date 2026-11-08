# Changelog

One row per harness version. Unit of record: what changed, what forced it, numbers before
and after, LOC owned, the ADR, the episode that published it.

Version = row. Episode = what gets published. One episode may publish several versions.

## One axis per version

- `control` — who owns the loop, and which gates sit in it.
- `substrate` — where inference runs: which model, served from where.
- `baseline` — H0 only. The starting point: there is nothing before it to attribute against.
- `compound` — both changed. Allowed, but the row and the ADR must state what cannot be
  attributed.

## Columns

- **Forcing constraint** — the failure or limit that made this version necessary. Not a
  preference.
- **Before -> after** — one line, measurable.
- **Numbers** — the five core numbers on the verification set, k builds each: pass count
  out of k per expectation, cost per accepted change, retries, manual interventions, tests
  written unprompted. Add a
  situational number only when the row's claim needs it.
- **LOC owned** — harness code we maintain, excluding fixture and evals.
- **Status** — in progress, published.

## The baseline

H0 is the starting point. Nothing comes before it, so its row has no change to record. What
it is:

- A thin wrapper around a standard coding harness, Claude Code, with Sonnet.
- The whole spec and an empty repo go in. Claude Code builds the app, writing whatever tests
  it decides to, unprompted, until it declares done.
- A fresh Claude Code session, same model, reviews the build against the spec and lists the
  issues by severity. While any is high, another session fixes the high ones and the review
  runs again: at most 5 fix rounds. Medium and low issues are recorded, never fixed.
- Our held-out checks grade the build. Their results are never fed back.
- The best-graded build is deployed in the house and, once it has heated as specified for
  two to three days, is accepted.
- It runs in a Docker Sandbox, one microVM per build: repo mounted read-write, network to
  what it installs from and the model proxy but never to this repository, the project's site
  or the house, no web search, no credentials except a budgeted build key for the model
  proxy, which stays outside the sandbox, no route to the sandbox daemon. The sandbox is
  adopted, not derived.
- Claude Code reaches the model through a LiteLLM proxy on the operator's network, which
  logs every call and its cost.
- No agent of our own. The vendor's stream of each session is archived; nothing else is
  kept as a record.

## Changes

| Version | Axis | What changed | Forcing constraint | Before -> after | Numbers | LOC owned | ADR | Episode | Status |
|---|---|---|---|---|---|---|---|---|---|
| H0 | baseline | nothing: see The baseline | the loop runs unattended from minute one; we want a working agentic SDLC before optimizing any part of it | nothing -> the whole spec reaches green from an empty repo, inside a cage; we own only what surrounds the vendor loop | verification set, 3 builds: F1.AC2 1/3; F3.AC4, F4.AC2, F4.AC4, F5.AC1, F5.AC3, F6.AC1, F6.AC6, F6.AC7, C5 3/3. Cost per accepted change: $4.12, one of 3 builds accepted after its soak. Retries: 0, every build stopped after its first review. Manual interventions: 0. Tests written unprompted: 3, 6 and 10 test functions. Cost per build $1.21 to $1.65; sandbox overhead per run 33 to 35 s | 410 | 001 | 1 | published |
| H1 | control | an agent loop of our own, on a Python agent framework (Pydantic AI) with its coding tools and every session's messages kept, in place of the wrapper and the vendor harness. Its quality against H0 at the same model, Sonnet, is reported as a finding, regressions included | the vendor harness carries a lot before it starts: its system prompt, tool definitions and instructions go into every request, and the loop is not ours to change. "Say hi." costs 23,478 tokens of prompt on H0, 9.4% of a 250k window and 15.7% of a 150k one | the vendor loop in the sandbox -> our loop on the host, the same cage, model, prompts and checks; first request 23,478 -> 3,529 tokens; builds that pass every machine expectation 1 of 3 -> 0 of 3 | verification set, 3 builds: F3.AC4, F4.AC2, F4.AC4, F5.AC1, F5.AC3, F6.AC1, F6.AC6, F6.AC7, C5 3/3; F1.AC2 0/3 (the same cases two of H0's three builds failed). Cost per accepted change: none, 0 of 3 accepted. Cost per build $1.54, $4.32 and $1.66; time 24, 83 and 36 minutes. Retries: no model error retried; fix rounds 0, 3 and 0. Manual interventions: 0. Tests written unprompted: 0, 0 and 0 test functions. Sandbox overhead per run 30 to 34 s. Peak context 59k to 67k in builds, up to 119k in reviews | 1,086 (758 Python, 227 shell, 14 config, 87 of prompts and lists copied from H0); 354 lines of tests | 002 | 2 | in progress |

A row is added when its version starts. Episode 0 adds no version: it derives the spec.

## Notes

- LOC owned is counted by one rule from H1 on: the non-blank lines of the version's own files, tests, the
  README and the lockfile left out. By that rule `run-h0/` is 485 lines today (288 shell, 110 Python, 87 of
  prompts and lists), and H1 is 1,086 (758 Python, 227 shell, 14 config, the same 87). The 410 in H0's row is
  its count at commit `e31eb62`, before the script was split into four files, and is left as it was.
- Corrected 7 November 2026: H1's forcing constraint first read "a black box, stdout only", and
  H0 "no history store". Both overstated: H0 archives the full stream of every session. The
  constraint is now the weight the vendor harness carries before it starts, measured on H0.
- Rows stay `in progress` until the numbers are filled in. A version with an empty numbers column
  is not published.
- If a delta is contaminated by feature difficulty, write that in the row rather than
  smoothing it.
- An ADR may cover more than one version only when the versions are on the same axis and
  sequential; this is noted in the ADR.
