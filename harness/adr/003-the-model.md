# 003 The model as the only variable: six builders on one loop

Version: H2
Axis: substrate
Date: 2026-11-11
Status: accepted

## Context

`process-fit.md` is unchanged: a machine can check 33 of the 35 acceptance criteria by playing the
devices on a broker, and the worst case is physical, so the cage, the relays' hard limits and the
soak stay as ADR 001 left them. ADR 002 changed who runs the agent and kept the model. This version
keeps the loop and changes the model.

## Symptom

Every one of H1's builds read F1.AC2 the same way: an error that names a line and not the key, a
relay channel of 1.5 accepted. Two of H0's three builds had done the same. In H1's builds the
reviewers, the same model as the builder, reported the fault and labelled it medium, though the
review prompt calls an unmet requirement high and gives `validate` accepting what section 4
rejects as its example. The loop fixes only what is high, so the fault shipped. With one model,
there is no telling whether that reading and that labelling are the model's or the spec's.

## Alternatives rejected

- **Choose the model the rest of the series runs on.** The comparison is a method for choosing a
  build, whatever made it, not a ranking of models. Two builds per model and setup is enough to see
  differences of kind and not a rate, and one provider's serving of each model is not the model.
- **Change the loop at the same time.** One variable per version.
- **One request format for every model, translated by the proxy.** A translation in the proxy
  would be a variable no one sees. Each model is asked in the format its maker documents.

## Decision

H1's loop, unchanged, with the model as the only variable (`harness/run-h2/`).

- **The builders**: one full model per family, Sonnet 5.5, Gemini 4 Carbon, GLM 5.3, MiMo V2.6 Pro,
  Luna 6 and DeepSeek V4.1 Flash; then the GLM and MiMo families' flash models, GLM 5.3 Flash and
  MiMo V2.6 Flash. Every model is reached through the operator's proxy by name.
- **The same request across makers**: reasoning on, at what each maker calls medium effort or the
  nearest to it, and the same output limit where the model allows it, each read off a logged
  request before any counted build. Claude goes as in H1, byte for byte; the others through the
  framework's OpenAI-compatible model with its maker's profile.
- **Two sets of instructions**, two builds per model in each: the coder's whole system prompt is one
  line, "You are a software engineering agent.", or Claude Code's own system prompt with its first
  line changed so it does not tell every model it is Claude. Sonnet's two one-line builds are H1's,
  from the experiment of ADR 002, reused: for Claude, H2 sends what H1 sent.
- **The same limit for all**: four hours a build, kept when the slow models met it.
- **Priced at each maker's list price**, whatever a provider charges, so a reader can reproduce the
  cost. DeepSeek at its off-peak rate.
- **One grader for everything**: DeepSeek V4.1 Flash grades every build, its own included, three
  times each.
- **Choosing the build for the house**: every build that passes every machine expectation, of any
  model, goes into a comparison of the code, two builds at a time, by a judge model, written before
  the builds; the build in the house competes. The winner soaks. In this version: `0559`, Gemini 4
  Carbon with the one-line instructions, over the other eligible builds and over H0's `c300`.
- **The counted builds** ran on commits `e6027df` to `2a67c61`; the later ones add only the flash
  models' profiles to the harness.

## Consequences

- **What we now own:** 1,131 non-blank lines in `harness/run-h2/` (802 Python, 227 shell, 15 config,
  87 of the prompts and lists copied from H0), and 383 lines of tests. The difference from H1 is
  the models' profiles and the setting that replaces the coder's instructions with a file.
- **What we learned about F1.AC2.** It fails in 16 of 32 builds, in every family's but Gemini's.
  Across the builds the reviewers reported it in about half the failing cases, and labelled it
  medium or low every time but three; those three were fixed and hold, and every other report
  shipped. The model is part of it: Gemini's builds got it right every time. The label decides what
  happens to everyone else's.
- **What broke the builds that failed.** Code that addressed the clause and got it wrong, or read it
  another way; never a clause left out. The widest failure was one line, a Keep Alive written as
  one byte, which failed 32 expectations; the build had never run against a real broker.
- **Price says little about which build works.** Eligible builds cost from $0.72 (MiMo V2.6 Flash)
  to $14.36 (Gemini 4 Carbon).
- **The environment failed in ways that looked like results**: a grading sandbox without a route to
  the proxy, an install rule that turned one README into 37 failures, checks wrong in both
  directions (each logged), a provider's route that cut long answers, and a full disk. Each was
  caught by reading the evidence; 13 builds hit by a provider's route or the machine were scrapped
  and rebuilt.
- **What is still not possible:** a claim about which model is better. And a review whose labels
  follow its prompt: the loop acts on the labels it is given.
- **Numbers**, from `CHANGELOG.md`: verification set, 32 builds: F1.AC2 16/32; F5.AC1 27/32; F6.AC1
  and C5 29/32; the other six 30/32. Builds that pass every machine expectation: 10 of 32. One
  accepted after its soak, `0559`; cost per accepted change $131.69, the cost of all 32 builds.

## If your product differs

If you compare models for your own loop, hold the loop still and change only the model, and read
each model's request off the wire before you trust that it got the settings you meant. Then choose
a build, not a model: what matters in the end is the program that runs.

If your review step labels its findings and your loop acts on the labels, read what it labels
medium. That is where a known fault waits to ship.

## Links

- Spec: `spec/SPEC.md`, version 1.0
- Expectations touched: none changed for this version; checks corrected along the way are logged with the eval method
- Post:
- Deep dive:
