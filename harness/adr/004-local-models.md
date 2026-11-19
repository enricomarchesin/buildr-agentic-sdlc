# 004 Local models: the same loop, with the model on the operator's own machines

Version: H3
Axis: substrate
Date: 2026-11-14
Status: accepted

## Context

`process-fit.md` is unchanged: a machine can check 33 of the 35 acceptance criteria by playing the
devices on a broker, and the worst case is physical, so the cage, the relays' hard limits and the
soak stay as ADR 001 left them. ADR 003 held the loop still and changed the model, among cloud
models, and chose a build rather than a model. This version keeps that method and moves the model
off the cloud.

## Symptom

Every build so far ran on a model in someone else's datacenter, paid by the token: $131.69 for
H2's 32 builds at list price. ADR 003 left the model question open, and one part of it is whether
the series needs a paid cloud model at all. The expectation, written before the first build, was
that models small enough to run at home would fail at decomposition: no plan that adds up to a
program.

## Alternatives rejected

- **Change the loop for the local models.** One variable per version. The loop changes only where
  a local model cannot run it otherwise, and each such change is listed below.
- **Talk to the model servers directly.** Every model is reached through the operator's proxy by
  name, as in H2, so the harness does not know where a model runs and every request is logged.
- **Count the builds of the one-line arm that ran on the framework's default instructions.** Five
  builds got the coder's 3,458-character default instead of the one line, by a setup mistake. They
  answer another question; they were set aside with their records and the arm was built again.

## Decision

H2's loop (`harness/run-h3/`), with local models as the builders.

- **The builders**: Qwen 3.8 27B, a dense model quantised to about 3.4 bits a weight, served by
  llama.cpp on one RTX 3090 with a 147,456-token window; Qwen 3.8 Flash Next on an AMD Strix Halo
  with 128 GB of unified memory and a 262,144-token window; and DeepSeek V4 Flash, quantised to fit
  the same Strix Halo, with a 126,000-token window. The DeepSeek comes from the grader's family,
  which the local builders were to exclude; it was let in as an exception, and its results are read
  with that in mind.
- **What the local models forced on the loop**: the loop's two leading system messages go out as
  one, since the first model's chat template accepts a system message only first; the output limit
  is 32,768 tokens, the most the servers give; no reasoning effort is sent, since one server refuses
  it and the other takes thinking from the model's own setup; and a local model's history is
  summarised at 90% of its real window, the newest 15 messages kept, the summary written by a model
  that does not think, with the coder's own clearing of old tool results off.
- **The limit**: six hours a build, eight for the DeepSeek, which generates about 15 tokens a second.
- **Two sets of instructions**, as in H2: the one line, "You are a software engineering agent.", or
  Claude Code's system prompt with its first line neutral. Each Qwen model built twice in each; the
  DeepSeek once with Claude Code's prompt and twice with the line; the 27B a third time with the
  line.
- **The method for the house is ADR 003's**: a build is eligible when it passes every machine
  expectation, and only an eligible build goes into the comparison of the code and the soak.
- **The record**: from 18 November, H2 and H3 keep every round of every session in a SQLite step
  log, each message stored once, and commit what a build or fix session left uncommitted at its end.
  Before that, a session kept its last five snapshots, so the rounds before a summary were lost for
  the local models' earlier builds; their final messages, reviews and code are whole.
- **The counted builds** ran on commits `543c891` to `13ba276`. In between, the commits add the third
  model, keep the sandbox tool's own messages out of a command's output, and change the record as
  above.

## Consequences

- **What we now own:** 1,315 non-blank lines in `harness/run-h3/` (958 Python, 229 shell, 18
  config, 87 of the prompts and lists copied from H0, 23 of the summary prompt), and 577 lines of
  tests. The difference from H2 is the local models' profile and settings, the summary, and the
  record.
- **The expectation was wrong.** Every local build produced a complete controller that installs from
  its README. Every failure is code that addressed a clause and got it wrong, or read it another
  way; none is a clause left out. Eleven builds fail between one and four of the 37 checks. The
  twelfth fails 34, from one bit: a will-retain flag set where the will flag belongs in its own MQTT
  client, which its own test broker read the same way.
- **None is eligible.** The commonest failures: F8.AC2, a closed standard output kills the
  controller through SIGPIPE although it ignores the write error; and F1.AC2, a configuration error
  that does not name its key, or a relay channel of 1.5 accepted.
- **The reviews are the weak point.** A review that tests against the build's own test double
  confirms what the build believes; a review's misreading became the next fix session's bug; a
  requirement reported broken shipped when labelled medium; and a review that tested F8.AC2 with
  writes that fail, not with a closed pipe, confirmed it.
- **The cost moves from money to time and electricity**: about €3.93 for the 12 builds at €0.26 a
  kWh, from rough power figures, the machines' price not counted; up to eight hours a build.
- **What is still not possible:** a claim about which model is better, local or not; two or three
  builds per model and arm show differences of kind, not a rate.
- **Numbers**, from `CHANGELOG.md`: verification set, 12 builds: F1.AC2 4/12; C5 8/12; F5.AC1 9/12;
  F6.AC1 10/12; the other six 11/12. Builds that pass every machine expectation: 0 of 12. None
  accepted; the build in the house stays.

## If your product differs

If you move your loop to a model you serve yourself, read its window, output limit and chat template
off the server before the first build, and expect to change the loop where they differ: here, one
system message and a summary before the window fills. Read the first request your loop actually
sends, too: one set of instructions here was not the one meant.

If your builds come close and fail in small places, look at what your review tests against before
you change the model.

## Links

- Spec: `spec/SPEC.md`, version 1.0
- Expectations touched: none changed for this version; checks corrected along the way are logged with the held-out checks, which stay private
- Post:
- Deep dive: https://buildr.zone/episodes/04-where-the-model-runs/
