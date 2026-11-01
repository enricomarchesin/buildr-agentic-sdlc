# AGENTS.md

Guide for agents and people working on this repository.

## What this is

The material an article series on Agentic SDLC links to. The episodes themselves are
published on https://buildr.zone and are not in this repository.
A heating controller, `heatctl`, is the fixture. The subject is the process that builds it:
a harness that takes one fixed spec and an empty repo and builds the whole app, graded by
hidden checks and a soak in a real house.

No hand-written app code is ever added. Every `heatctl` build is produced by a harness run.
The harness code arrives with H0 in `harness/run/`.

## Layout

```
AGENTS.md               this file
README.md               for readers
process-fit.md          the two questions that choose the process, linked from every ADR
spec/
  SPEC.md               the agent-facing spec for heatctl, the only file a build ever sees
  README.md             how the spec is structured and changed
  CHANGES.md            log of every spec change, with a drift flag
eval/
  README.md             expectation format, how hidden checks run, the four layers, rules
harness/
  README.md             what the harness is, the sandbox network allowlist, gates
  CHANGELOG.md          one row per harness version, added when the version starts
  adr/000-template.md   template for the one ADR per version
fixture/
  README.md             deployment in the house, hard limits, logging and rsyslog forwarding
```

Referenced but not created yet: `spec/DEFERRED.md`, `harness/run/`, `fixture/DEPLOYED.md`.

## Rules that are easy to break

- **Nothing held out comes here.** Expectations, checks, their results and the log of edits
  to them are never added to this repository. A build must never be able to read them.
- **No milestone announced before it ships.** No planned harness version, model, tool or
  target is named here before its version starts.
- **`spec/SPEC.md` stays clean.** A build sees it and nothing else, so it holds no series
  vocabulary: no process fit, no gates, no harness, no evals, no M/H split, no mention of
  tests.
- **Spec first.** A behaviour change goes spec, then evals, then harness. Bump the spec
  version and log the change in `spec/CHANGES.md`.
- **One axis per harness version** (control or substrate; H0 is baseline). A version exists
  only when its numbers are filled in. One ADR per version.
- **No benchmark language.** Results are per expectation and per feature, never a score or a
  ranking across models or tools.

## Writing style

Plain words, short sentences, tables where items compare. Clauses and criteria keep stable
ids (C1, B4, F6.AC5, E001, H3) so other files can cite them.

## Links from the site

The episodes on https://buildr.zone link here, on `main`: to files, and to headings in
`spec/SPEC.md` such as `#6-control-behaviour`. Renaming a file or a `SPEC.md` heading breaks
those links.

## Git

Work happens on `main`.
