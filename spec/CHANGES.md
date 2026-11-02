# Spec changes

Every change to `SPEC.md`, with the reason. Newest first.

Recording rule: a spec change that exists because the implementation drifted is the metric
we call spec drift, not a fix. Write it down as drift.

| Date | Feature | Spec version | What changed | Why | Drift? |
|---|---|---|---|---|---|
| 2026-11-02 | section 4 | 1.0-draft | targets between 7.0 and 25.0 inclusive, was 5.0 to 30.0 with no word on the ends. Temperatures may be written without decimals. Every schedule entry has a `from` and a `target` | author, on gaps found while writing the expectations. Before the freeze, so no version bump | no |
| 2026-11-02 | R4, F6.AC3, F6.AC6 | 1.0-draft | control cycles run and are logged during `startup_grace`, but send no relay command; the first cycle after it commands every relay to its desired state. F6.AC3 says the cycles run; F6.AC6 reads "once the room has reported" instead of "after the first control cycle", which now comes during the grace | author: B6 ran a cycle on every reading while R4 put the first cycle after the grace. Before the freeze, so no version bump | no |
| 2026-10-31 | section 4, B4, F4 | 1.0-draft | default `hysteresis` 0.3 → 0.5 °C, either side of the target. F4.AC1 reading 19.6 → 19.4; F4.AC2 readings 20.2 and 20.3 → 20.4 and 20.5 | author: 0.5 is the common thermostat default. Before the freeze, so no version bump | no |
