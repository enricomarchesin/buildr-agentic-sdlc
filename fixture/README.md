# fixture/

The heating app. Nobody writes it by hand: every build produces it from an empty repo and
the complete spec in `spec/`. This directory holds what runs in the house and how, not the
source of any one build. Builds are archived per harness version and run id; the source of
every accepted build is in `builds/`, as the harness left it.

## Deployment

- The house runs one build at a time. The first is a Claude Code build from episode 1.
- A build is deployed only after it passes the held-out checks. Its first two to three days
  in the house are the soak: it must heat as specified, not crash, and need no manual
  intervention. A build that passes its soak is accepted and stays. A failed soak rolls back
  to the previous accepted build; before the first one, to the house's own controls.
- A later build replaces the accepted one the same way: the same checks, then its own soak.
  `DEPLOYED.md` records which build runs, from which harness version and run id, since
  when, and when it was accepted.
- Hard limits live outside every build: an automatic off timer configured by hand on each
  Shelly relay, and the boiler's own safety. `heatctl` never changes device configuration.

## Zigbee sensors

The spec reads a Zigbee sensor's temperature from what Zigbee2MQTT publishes, and counts
every payload with a numeric `temperature` as a new reading. That holds only if Zigbee2MQTT
is set up for it, so the house needs three things outside every build. All three were found
in the first build's soak, in November 2026; the spec is unchanged.

- **Zigbee2MQTT publishes only what it just received**: `advanced.cache_state: false`. Its
  default, `true`, sends a device's whole last-known state with every message, so a message
  that carries only battery, light level or presence also carries the last temperature, and
  a controller cannot tell it from a new one. In the soak, a room read 22.2 °C for 16 hours
  with the heating off while it had fallen to about 19.
- **Each sensor reports its temperature at most every 5 minutes**: in Zigbee2MQTT's
  Reporting settings, `msTemperatureMeasurement`, maximum interval 300 s, minimum 30 s,
  reportable change 10 (0.10 °C). A battery sensor must be woken, with its button, for the
  setting to reach it. Without it, a steady room reported once an hour, longer than
  `stale_after`. Check that it holds: one sensor in the soak lost it every time it rejoined
  the network, and Zigbee2MQTT did not send it again. For such a sensor, send the setting
  again on each rejoin: an automation on Zigbee2MQTT's `bridge/event` for its
  `device_announce`, publishing the same `device/reporting/configure` request a few seconds
  later, while the sensor is awake (`DEPLOYED.md`).
- **Each sensor's Zigbee parent is a router that is always powered**, never a bulb switched
  at the wall. A battery sensor whose parent loses power is cut off until it rejoins; in the
  soak, three sensors went silent together for hours that way. Zigbee2MQTT's network map shows
  the parent; to move a sensor, re-pair it with joining allowed only through the chosen router.

With `cache_state` off, most messages carry no temperature. A build ignores them, as the spec
requires, and may log each one.

## Before a soak

The first soak spent its first two days testing the house, not the build. Before a build goes
in, check the house, and check that each setting holds, not only that it was sent:

- **Each sensor's parent**, in a fresh network map: an always-powered router.
- **An hour of reports from each sensor**, in Zigbee2MQTT's log or on the broker: a
  temperature at least every 5 minutes from a steady room. Zigbee2MQTT shows the reporting it
  configured, not what the sensor keeps: in the first soak, one sensor went back to hourly
  every time it rejoined the network, and Zigbee2MQTT did not send the setting again. Look for
  its rejoins (`device_announce` in Zigbee2MQTT's log) as well as its reports.
- **`cache_state` off**, and a message without a temperature carrying none.
- **Each sensor where the room is**: away from the radiator, lamps, screens and sun, at about
  sitting height. In the first soak, a studio sensor read about a degree warm until it was moved.
- **The relays' own off timers**, read back from each relay.

## Logging

Every build must write one structured event per control cycle to its application log, as a
JSON line, as required by clauses L1 and L3 in `spec/SPEC.md`. The history starts the day the first build is
deployed.

```json
{"event":"control_cycle","schema":1,"ts":"2026-11-01T06:30:00Z","unit":"C",
 "rooms":[{"id":"living","readings":[20.4,20.1],"temperature":20.25,"target":21.0,"demand":true}],
 "zones":[{"id":"ground","desired":"on","reported":"on","degraded":false}]}
```

- Fields are pinned in L1: the unit of every temperature in the event; per room its
  readings, temperature, target and demand; per zone its desired and reported relay state
  and whether it is degraded.
- Never read by the control path. A logging failure must not stop or change control.
- History is kept for at least a full heating season. The app only logs; keeping the history
  is the system's job, not the build's.
- Optional forwarding: rsyslog on the Pi sends the events to a log host on the LAN, which
  keeps the long history. Without forwarding, local rotation must be set to keep a season,
  because default rotation would delete the history the logging exists for.
- A held-out check verifies the format on every build, so a build that drops or reshapes it
  fails.
- Changing the fields is a spec change: bump `schema` and the spec version, and note the
  date here, because the history shifts at that point.

### Forwarding

Use TCP with a disk-assisted queue, so events are buffered on the Pi while the log host is
down instead of being dropped. UDP loses messages silently.

```
# /etc/rsyslog.d/60-heatctl-forward.conf on the Pi
if $programname == 'heatctl' then {
  action(type="omfwd" target="loghost.lan" port="514" protocol="tcp"
         queue.type="LinkedList" queue.filename="heatctl_fwd"
         queue.maxDiskSpace="200m" queue.saveOnShutdown="on"
         action.resumeRetryCount="-1")
}
```

On the log host, write only the message body to the file, so it stays one JSON object per
line. The log host stays on the LAN: the control path remains offline.
