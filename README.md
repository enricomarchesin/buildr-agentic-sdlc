# buildr-agentic-sdlc

Agentic SDLC, in the open: https://buildr.zone

One fixed spec, an empty repo, and an agent that builds `heatctl`, a heating controller for
a real house. Nobody writes its code by hand. Every build comes from a harness run, is graded
by checks the agent never sees, and runs in the house for a few days before it is accepted. The subject is
the process that builds it, not the app.

## What is here

```
spec/        SPEC.md, the only file a build ever sees, and its change log
eval/        how builds are graded; the expectations and checks themselves are held out
harness/     what builds the app: README, changelog, one ADR per version
fixture/     how a build runs in the house, hard limits, logging
process-fit.md   the two questions that choose the process
```

The episodes are on https://buildr.zone; this repository holds the material they link to.
Start with `process-fit.md`, then `spec/SPEC.md`.

## What is not here

The expectations, the checks and their results are held out, so that no build can read them
and they stay hidden from the agent. Results are published per expectation in the episodes,
never as a score or a ranking.
