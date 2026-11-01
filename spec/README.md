# spec/

The product spec is the driver. Every build takes the whole spec and an empty repo, and the
agent proposes the plan to reach it. This directory is not a task list for a benchmark; it is
what the product must do.

## Layout

```
spec/
  SPEC.md          the only file the agent sees: clauses, devices, features, criteria
  CHANGES.md       every spec change, with the reason and a drift flag
  DEFERRED.md      ideas that did not make the spec
```

The author's notes on the product and on the draft are kept out of this repository, like the
expectations in `eval/`.

The complete spec is derived in the open in episode 0 and fixed before episode 1. Features in
`SPEC.md` are ordered by complexity, simplest first. The order is a hint, not a schedule: how
a build is decomposed is the agent's plan.

## What the agent sees

Only `SPEC.md`. It holds no series vocabulary: no process-fit questions, no gates, no fixture, no mention
of how the build is evaluated. Anything that would change how the agent works if it read it
stays out of it.

## What the spec must contain

The spec is complete before episode 1: every build delivers all of it at once. It must give
the process something to exercise:

- a feature set big enough to need decomposition, so the plan matters
- an action with a real blast radius, so isolation matters: the relays
- a tempting out-of-scope extension, which the negative layer needs: clause C6
- the logging clauses, so the house keeps a history: L1, L3, L4
- the device contract and the README clause, which the hidden checks need: I1, I2, R1

The expectations reported on every version, the verification set, are tagged in `eval/`,
not here. New feature ideas go to `DEFERRED.md`, not into the spec.

## Clauses and criteria

- Clauses carry stable ids (C1, B4, R2, L1, ...). Criteria carry feature ids (F4.AC2).
  `eval/` entries cite them.
- One observable behaviour per criterion. If it cannot be observed, it is not a criterion.
- Each criterion is `M` (machine: code decides) or `H` (human: a person judges it at a
  human checkpoint). The split is recorded on the expectation in `eval/`, not in `SPEC.md`,
  so the agent does not learn which criteria are checked by machine.
- No criterion may mention how the solution is built.
- The spec never mentions tests. Whether the agent writes them is measured, not requested.

## Freeze and change rules

- `SPEC.md` carries one spec version.
- **Spec first:** changing behaviour means bumping the spec, then updating `eval/`, then the
  harness. Never the other way round.
- Every spec change is recorded in `CHANGES.md` with the reason. Spec changes that exist only
  because the implementation drifted are the metric, not a fix.
- A build is not "done" because the code exists; it is done when its acceptance criteria pass
  and any `H` criteria have been reviewed.

## Related

- `eval/README.md`: how criteria become expectations.
- `process-fit.md`: why the M/H split exists.
