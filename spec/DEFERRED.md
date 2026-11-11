# Deferred

Ideas that did not make the spec. Every build is graded against the same fixed spec, so a
new feature waits here instead of going into `SPEC.md`.

An entry is not a promise. An idea leaves this list only through a spec change: a new spec
version, logged in `CHANGES.md`, before any build sees it.

| Id | Date | Idea | Why it waits | What it would touch |
|---|---|---|---|---|
| D1 | 2026-11-02 | One or more outdoor temperature sensors, on the same broker as the rest | the spec is fixed before the first build. What `heatctl` would do with the readings is also open: log them, protect the house from frost, or let the weather change a zone's demand | section 4 for the configuration, section 5 if the sensor is not a Shelly, L1 for the readings, and section 11, which today puts weather data out of scope |
| D2 | 2026-11-07 | Reload the configuration without a restart, so a changed schedule applies at once | the spec is fixed before the first build, and today a change needs a restart: stop turns every relay off (R5), and the next start decides everything afresh after the startup grace (R4). What a reload does with an invalid file, a changed relay or a zone that disappears is open: keep the old configuration, or refuse | section 4 for the rules a reload must hold to, R4 and R5 for what the relays do while it applies, and section 9 if it is logged as an event |
| D3 | 2026-11-11 | Say when a zone has been degraded for a long time, so a person learns of a blind room before it gets cold | the spec is fixed, and it asks for no notification: a degraded zone is in each cycle's log event (L1) and in **status**, which nobody reads unprompted. In the first build's soak a bedroom had no temperature for 26 hours, its zone held off as B5 and F4.AC4 require, and it was found by hand. Open: how long counts as long, and where the warning goes while the broker is the only peer (C3): a topic of `heatctl`'s own, or a log event a log host can alert on | section 5 (a topic of its own, outside the devices' interface, I2), L1, F4, F7 |
