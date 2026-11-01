# NNN <decision, in plain words>

Version: H<N>
Axis: baseline (H0 only) | control | substrate | compound
Date:
Status: proposed | accepted | superseded by NNN

## Context

What the product needs at this point, in terms of process fit (`process-fit.md`):
can a machine check it and how fast, and what's the worst that can happen if it's wrong. Two or three sentences. No history, no narrative.

## Symptom

The concrete failure that made this change necessary. What broke, on which feature, with
which number. If there is no symptom, this is not a decision, it is a preference, and it does
not get an ADR.

## Alternatives rejected

Two or three, each with the reason it lost. One of them should be "do nothing" or "keep the
current version", because that is always an option.

## Decision

What changed, in one paragraph. Then the specifics: gate order, file layout, config, the
number that moved. Enough for someone to reproduce the state.

## Consequences

- What we now own: LOC, dependencies, processes to supervise, things that can break at 2am.
- What regressed, if anything. State it plainly, with the number.
- What is now possible that was not before.
- What is still not possible.
- Numbers: before -> after, using the columns from `CHANGELOG.md`.

## If your product differs

The transferable part. One or two sentences: if a machine can check less of your product, or only
more slowly, or your worst case is smaller, what would you choose instead. This is the section
that makes the ADR useful to someone else and keeps the series about approach rather than
about this fixture.

## Links

- Spec: `spec/SPEC.md`, version
- Expectations touched: `eval/expectations/ENNN-<slug>.md`
- Post: <url>
- Deep dive: <url>
