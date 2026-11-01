# fixture/

The heating app. Nobody writes it by hand: every build produces it from an empty repo and
the complete spec in `spec/`. This directory holds what runs in the house and how, not the
source of any one build. Builds are archived per harness version and run id.

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

## Logging

Every build must write one structured event per control cycle to its application log, as a
JSON line, as required by clauses L1 and L3 in `spec/SPEC.md`. The history starts the day the first build is
deployed.

```json
{"event":"control_cycle","schema":1,"ts":"2026-11-01T06:30:00Z",
 "rooms":[{"id":"living","readings":[20.4,20.1],"temperature":20.25,"target":21.0,"demand":true}],
 "zones":[{"id":"ground","desired":"on","reported":"on","degraded":false}]}
```

- Fields are pinned in L1: per room its readings, temperature, target and demand; per zone
  its desired and reported relay state and whether it is degraded.
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
