# Spec changes

Every change to `SPEC.md`, with the reason. Newest first.

Recording rule: a spec change that exists because the implementation drifted is the metric
we call spec drift, not a fix. Write it down as drift.

| Date | Feature | Spec version | What changed | Why | Drift? |
|---|---|---|---|---|---|
| 2026-10-31 | section 4, B4, F4 | 1.0-draft | default `hysteresis` 0.3 → 0.5 °C, either side of the target. F4.AC1 reading 19.6 → 19.4; F4.AC2 readings 20.2 and 20.3 → 20.4 and 20.5 | author: 0.5 is the common thermostat default. Before the freeze, so no version bump | no |
