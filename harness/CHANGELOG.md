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
  runs again: at most 3 fix rounds. Medium and low issues are recorded, never fixed.
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
- No own agent, no history store.

## Changes

| Version | Axis | What changed | Forcing constraint | Before -> after | Numbers | LOC owned | ADR | Episode | Status |
|---|---|---|---|---|---|---|---|---|---|
| H0 | baseline | nothing: see The baseline | the loop runs unattended from minute one; we want a working agentic SDLC before optimizing any part of it | nothing -> the whole spec reaches green from an empty repo, inside a cage; we own only what surrounds the vendor loop | sandbox overhead per run: | | 001 | 1 | in progress |

A row is added when its version starts. Episode 0 adds no version: it derives the spec.

## Notes

- Rows stay `in progress` until the numbers are filled in. A version with an empty numbers column
  is not published.
- If a delta is contaminated by feature difficulty, write that in the row rather than
  smoothing it.
- An ADR may cover more than one version only when the versions are on the same axis and
  sequential; this is noted in the ADR.
