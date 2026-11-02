# eval/

Expectations about how the system behaves. An eval asserts; it does not observe and report.
This is the difference between an eval and a log replay: you must state what you expect
before you look at the result.

Not a benchmark. Expectations are ours, on our fixture. Results are reported per expectation,
never as a score across models or tools.

## Layout

Held out: the expectations, the checks and the log of edits to them are not in this
repository, and no build ever sees them. This file is the method; the layout below is the
held-out tree.

```
eval/
  INDEX.md                every criterion: its expectation, layer, M/H, verification set
  expectations/
    E001-<slug>.md
    E002-<slug>.md
  checks/
    e001-<slug>/
      verdict.py          reads the target state off the broker or the log, exit 0 = pass
  CHURN.md                every edit to an expectation, with reason
```

## Expectation file format

```markdown
# E004 <short name>

- Layer: behaviour | regression | process | negative
- Spec: F4.AC2 (spec version 1.0)
- Criterion: M | H
- Check: checks/e004-<slug>/
- Verification set: yes | no
- Frozen: yes

## Expectation
The state the system must reach. One sentence, observable from outside the build. It never
says how the state is observed.

## Scenario
Starting conditions and what a user does, in plain words. No CLI syntax: the operator works
that out from the README.

## Failure mode
What it looks like when this fails. Filled in when it first fails.

## Notes
Anything the check cannot see.
```

## How a check runs

Expectations say what state the system must reach. Checks are how we see it, and they stay
hidden from the agent.

1. **Setup.** A fresh MQTT broker and the build, nothing else. The check plays the devices:
   it publishes sensor readings and listens on actuator topics, as listed in
   section 5 of `spec/SPEC.md`.
2. **Operator.** A pinned grading model performs the scenario. It can read the README and run
   commands in a shell, and nothing else. It cannot read the source, so it cannot work around
   a bad README.
3. **Verdict.** A few lines of code read the target state off the broker or the build's log
   output. The model operates; it never decides pass or fail. That keeps the oracle
   hard.
4. **Outcome.** One of: pass; fail-behaviour, the operator did it and the state is wrong;
   fail-docs, the operator could not work out how from the README; grader error.
5. **Repeats.** Each check runs three times per build. Agreement between the runs is
   recorded, so grader noise is not mistaken for build noise.

The grading model and its version stay the same for the whole series and are named in ADR
001. They never move when the harness changes where inference runs, or that version would change
two things at once.

If a scenario depends on time of day or on durations, the check controls the clock from
outside the build, for example with libfaketime, rather than adding a clock clause to the
spec.

**Validating the checks without a reference app.** Every check must fail on an empty build.
It must also catch a few deliberately broken builds: one that never turns the heating on,
one whose README gives the wrong commands. A check that passes a broken build is fixed and
logged in `CHURN.md`.

## Four layers

| Layer | Question | Examples |
|---|---|---|
| behaviour | does the app do what the spec says | setpoint reached within tolerance, schedule respected |
| regression | does a new build keep what the accepted build does | safety limits hold, behaviour the accepted build passes still passes |
| process | did the harness produce what it promises | plan exists before code, trace per step, tests written by the agent unprompted and run before it declared done |
| negative | does it refuse what is outside the spec | no writes outside the repo, no credentials read, no request outside the network allowlist, out-of-scope feature not implemented |

The process layer evaluates the harness. The other three evaluate the app. Both are needed:
a green app produced by an unauditable process is not the thing being sold here.

## Rules

1. **Traceability.** Every expectation cites a spec clause. No clause, no expectation.
2. **Written before the run.** Expectations come from the spec, never from observed output.
   An expectation written after the fact encodes the implementation and always passes.
3. **Frozen per spec version.** When the spec bumps, expectations are re-derived first.
4. **Every edit is logged in `CHURN.md`** with the spec change that justified it. An edit
   with no spec change is eval churn: a weakened check. That is a finding, not maintenance.
5. **Per-expectation reporting.** Pass/fail plus failure mode. No aggregate score as a
   headline, no cross-model percentages.
6. **Repeat builds.** Every version produces k = 3 builds. Each expectation is reported as a
   pass count out of k, for example 2/3. One build is never reported as a result.
7. **Verification set.** Expectations tagged `Verification set: yes` are reported on every
   version. The rest run on every build and are reported when they fail.
8. **Hand-written and held out.** Expectations and verdicts are written by hand, and the
   agent never sees them or their results. Checks are black-box: they drive the build
   through its README and observe it on the broker, and never import its code.
9. **The agent's tests are observed, not requested.** Whether it wrote tests, what they
   cover, and whether it ran them is recorded in the process layer for every build.
10. **Green but wrong is a result.** If every expectation passes and the review still says no,
   record it. The suite was too weak; that belongs in the failure catalogue.

## What does not belong here

- Rankings of models or harnesses.
- Checks that assert the implementation instead of the behaviour.
- Anything that needs a human to interpret it and is not marked `H`.
- Tuning a check until it passes.
