# Process fit

Two questions about what you're building, answered before choosing any tool. The answers
decide which process fits it: how much the agent can do on its own, and how many barriers
sit between the agent and the real world.

Nothing is scored, and the process is not measured here. That is the job of the evals and
the changelog numbers. These questions choose the process; the evals check whether it works.

Linked from every ADR. Each ADR names which answer forced its gate.

## 1. Can a machine check it, and how fast?

Whether a machine can tell that the result is right (the oracle), and how long it takes to
find out.

- **Yes, in seconds or minutes.** Checks decide. The agent can iterate on its own.
- **Yes, but only slowly**, for example days in the real environment. Fewer changes in
  flight, and a soak or staging step before a change counts.
- **Partly.** A machine can check some acceptance criteria, a person has to judge others.
  Gates separate the two, and what needs a person goes to a human checkpoint.
- **No.** The result needs interpretation: a forecast, a report, a UX. Gates become
  backtests, shadow mode, and review of outputs instead of code.

## 2. What's the worst that can happen if it's wrong?

How much damage a wrong change can do once it is out (the blast radius), and also what the
agent can damage while it builds.

- **Nothing outside the repo.** Ordinary checks are enough: build, check, ship.
- **Your machine, accounts or credentials.** A sandbox, no credentials beyond what the run
  needs, restricted network.
- **Something physical, money, or a person.** Hard limits enforced outside the agent, a soak
  or staging step, rollback, and a human approving anything that goes live.

Three products where a machine can check the result equally well, with different worst cases:

| Product | Worst case if a build is wrong | What the process needs because of it |
|---|---|---|
| A personal blog | a broken page, fixed in the next commit | nothing extra: build, check, publish |
| heatctl | the house goes cold, or the boiler short-cycles and wears out | relay timers no build can touch, a soak in the house before a build is accepted, rollback to the previous build |
| A bank transfer service | money goes to the wrong account | a human approves every release, strict separation from real accounts |

## What the answers produce

| Machine check | Worst case | Process consequences |
|---|---|---|
| yes, fast | small | fastest loop, commit on green, minimal gates |
| yes, fast | large | same loop, plus isolation, staging, rollback, a human release gate |
| yes, slow | any | fewer changes in flight, a soak before a change counts |
| no | any | dataset contracts, backtests, shadow mode, output review |

## Applied to this fixture

| | heatctl | for contrast: a forecast of next week's heating |
|---|---|---|
| Can a machine check it, and how fast? | yes: it heats or it doesn't. Seconds for the agent's own tests, minutes for the hidden checks, two to three days for a soak in the house | no: is the forecast useful? The answer arrives over weeks, with the season |
| Worst case if it's wrong | a cold house or a worn boiler; while building, the host machine and the API key | bad data poisons the model silently |
| Process gets | agent-written tests, held-out black-box checks, a soak in the house, rollback, hard limits outside every build, a sandbox | dataset contracts, backtest gate, shadow mode, review of outputs |

The same engineer, the same house, a materially different process. That contrast is
the argument of the series: the shape of the gates follows the answers about the product.

## Using it on your own product

Answer the two questions in one line each, in writing, before choosing tools. The answers
slot into the table above. If two products get the same answers, they can share a process;
if any answer differs, they cannot, and copying someone else's harness is the wrong move.
