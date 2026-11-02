# heatctl — specification

Spec version: 1.0-draft

## 1. Purpose

`heatctl` is a command-line application, written in Go, that controls the heating of a
residential home. It reads room temperatures from sensors and switches relays that turn the
heating on for zones of the house. Setup comes from a YAML configuration file. All sensors
and relays are reached through an MQTT broker.

## 2. Scope and constraints

- C1: Go 1.26 or later. The result is a single binary that runs on a Raspberry Pi
  (linux/arm64) as a long-running process.
- C2: a pure CLI. No HTTP API, no web UI, no network listener. A local socket that only the
  status operation uses is allowed.
- C3: offline. The only network peer is the MQTT broker on the local network. No cloud
  service, no internet access at runtime.
- C4: no prediction or learning. Decisions follow the current readings and the configured
  targets, nothing more.
- C5: `heatctl` never changes the configuration of a device. It only sends the commands
  listed in section 5.
- C6: for sensors and relays we initially use only Shelly devices that support control and
  updates via MQTT messages. We might add other types of sensors in the future (Bluetooth,
  Zigbee, ESPHome, etc.), but they will also be connected, directly or indirectly, to the
  MQTT broker anyway.

- C7: **no saved state.** `heatctl` keeps no state across restarts. It writes nothing to
  disk: no database, no state file, no cache. Its only output is standard output (L1, L4). A local socket for the status operation
  is not state and is allowed.
  On every start it derives what each relay should be from the configuration, the current
  time, and what the devices report, in its first control cycle. It never reads back
  anything it wrote or published before. State held in memory while it runs is fine.

## 3. Concepts

- **House**: everything one `heatctl` instance controls.
- **Zone**: a part of the house with exactly one relay. Turning the relay on heats every
  room in the zone.
- **Room**: belongs to exactly one zone. Has zero or more temperature sensors.
- **Sensor**: one temperature reading channel on a device. A device can provide more than one.
- **Relay**: one switch channel on a device. A device can provide more than one.
- **Target**: the temperature a room should be at, in °C, fixed or from a weekly schedule.

## 4. Configuration

- I3: the configuration is a YAML file. Its path is given on the command line. Its shape is
  fixed by this section; unknown keys are an error.

```yaml
mqtt:
  broker: tcp://192.168.1.10:1883   # required
  client_id: heatctl                # optional, default heatctl
  username: ""                      # optional
  password: ""                      # optional

control:                            # every key optional, defaults shown
  cycle_interval: 60s
  hysteresis: 0.5                   # °C, applied either side of the target
  stale_after: 45m
  min_switch_interval: 5m
  relay_lease: 15m
  startup_grace: 2m
  confirm_timeout: 30s

zones:
  - id: ground
    relay:
      device: shellyplus1-a8032ab1c2d3
      switch: 0
    rooms:
      - id: living
        schedule:
          - { from: "06:30", target: 21.0, days: [mon, tue, wed, thu, fri] }
          - { from: "08:00", target: 21.0, days: [sat, sun] }
          - { from: "22:30", target: 18.0 }   # no days: every day
        sensors:
          - { device: shellyhtg3-84fce63a1b2c, component: 0 }
          - { device: shellyplus1-a8032ab1c2d3, component: 100 }
      - id: hallway                 # no sensors: never votes
  - id: upstairs
    relay:
      device: shellypro4pm-f008d1d8b8b8
      switch: 2
    rooms:
      - id: bedroom
        target: 19.0
        sensors:
          - { device: shellyhtg3-84fce63a9f01, component: 0 }
```

Rules:

- Durations use Go duration syntax. Temperatures are °C, with or without decimals: `19` and
  `19.0` are the same.
- `device` is the device's MQTT topic prefix. `switch` and `component` are channel ids.
- The file is a hierarchy: each zone has exactly one relay and one or more rooms; each room
  has zero or more sensors. A room belongs to the zone it is listed under.
- Zone ids are unique. Room ids are unique across the whole house, not only within a zone.
- Two zones never share a relay channel. A sensor channel is listed under at most one room.
- `sensors` may be omitted or empty.
- A room with sensors has either `target` or `schedule`, not both. A room without sensors may
  have neither.
- Targets are between 7.0 and 25.0, inclusive. A schedule has at least one entry, and every
  entry has a `from` and a `target`. `from` is `HH:MM` local time.
- `days` is optional. When present, it is a non-empty list of `mon`, `tue`, `wed`, `thu`,
  `fri`, `sat`, `sun`, with no repeats, and the entry applies only on those days. When
  omitted, the entry applies every day.
- No two entries may apply at the same time on the same day. An entry for every day and an
  entry for `sun` both at 22:30 is an error.
- `hysteresis` is at least 0. `relay_lease` is longer than `cycle_interval`. Every duration
  is positive.

## 5. Devices and MQTT

- I1: sensors and relays are reached only through the MQTT broker in the configuration.
  MQTT 3.1.1, QoS 0 or 1.
- I2: the topics and payloads below are the devices' own interface. They are fixed.

All devices are Shelly Gen2 or later, with MQTT enabled and status notifications turned on.
`<device>` is the device's topic prefix from the configuration.

**Relays** (for example Shelly Plus 1, Pro 4PM):

| Purpose | Topic | Payload |
|---|---|---|
| command | `<device>/command/switch:<id>` | `on`, `off`, or `on,<seconds>`: on, then back off after that many seconds |
| request status | `<device>/command/switch:<id>` | `status_update` |
| status | `<device>/status/switch:<id>` | JSON; `output` is `true` when on |
| availability | `<device>/online` | `true` or `false`; `false` is the broker's last will |

**Temperature sensors** (for example Shelly H&T Gen3, or a probe on a Shelly Plus Add-on):

| Purpose | Topic | Payload |
|---|---|---|
| reading | `<device>/status/temperature:<id>` | JSON; `tC` is °C, or `null` when the device has no valid value; `errors` is present on a fault |

Battery sensors sleep between reports and may report only on change or every few minutes.
They do not answer status requests.

Example payloads:

```json
{"id":0,"source":"timer","output":true,"temperature":{"tC":41.2,"tF":106.2}}
{"id":0,"tC":20.4,"tF":68.7}
{"id":100,"tC":null,"tF":null,"errors":["read"]}
```

Any other topic is ignored. A payload that cannot be parsed is ignored and logged.

## 6. Control behaviour

- B1: **reading.** A sensor reading is valid if `tC` is a number and `errors` is absent. A
  sensor is fresh if its last valid reading is not older than `stale_after`. A
  retained sensor message delivered when `heatctl` subscribes is not a reading: its age is
  unknown, so it would be state from before the start (C7).
- B2: **room temperature** is the mean of its fresh sensors' last valid readings. A room
  with no fresh sensor has no temperature.
- B3: **target.** A room with `target` always uses it. A room with `schedule` uses the entry
  that most recently started: the latest occurrence, on a day it applies to, at or before the
  current local time. The search goes back across days, and across the week, until it finds
  one. For example, early on Monday the last entry that applied on Sunday is still in force.
- B4: **room demand.** A room demands heat when its temperature is at or below
  target − hysteresis. It stops demanding when its temperature is at or above
  target + hysteresis. In between, it keeps its previous demand; a room with no previous
  demand starts without demand.
- B5: **zone decision.** A zone's relay should be on if at least one room in the zone demands
  heat. Rooms with no temperature do not vote. A zone in which no room has a temperature is
  degraded, and its relay should be off.
- B6: **control cycle.** One control cycle evaluates every zone. A cycle runs on every valid
  reading and every `cycle_interval`.
- B7: **switching.** When a relay's desired state changes, `heatctl` commands it. A relay is
  never switched twice within `min_switch_interval`; a change that comes too soon waits until
  the interval has passed, and is dropped if the desired state has changed back. After a
  start, the interval counts from each relay's first command: the first command is never
  delayed by a switch made before the restart (C7).
- B8: **confirmation.** If a relay's reported state differs from its desired state for longer
  than `confirm_timeout`, `heatctl` sends the command again, at most once per
  `confirm_timeout`, and logs the mismatch. A relay reported offline makes its zone degraded.
- B9: **dead-man.** If `heatctl` stops, crashes, or loses the broker, every relay it turned on
  is off within `relay_lease`, without any action from `heatctl` after the failure.

## 7. Failures, start and stop

- R2: **broker loss.** `heatctl` keeps running, reconnects with backoff capped at 60 s, logs
  each disconnect and reconnect, and after reconnecting resubscribes and re-sends every
  relay's desired state.
- R3: **bad input.** Malformed payloads, unknown topics, and readings outside −40 to 85 °C
  are ignored and logged. None of them stops `heatctl`.
- R4: **start.** On start, `heatctl` knows nothing from earlier runs (C7). It requests the
  status of every relay and sends no relay command until `startup_grace` has passed or every
  room with sensors has a temperature, whichever comes first. Control cycles run during that
  time as at any other, and write their L1 events, but a relay change they decide is not
  sent. The first cycle after it commands every relay to its desired state, decided from
  scratch. A room whose temperature is inside the hysteresis band starts without demand (B4).
- R5: **stop.** On SIGINT or SIGTERM, `heatctl` turns off every relay it controls, regardless
  of `min_switch_interval`, and exits with status 0 within 5 seconds.

## 8. Command line

- X1: `heatctl` provides at least these operations. Names and flags are up to the build and
  are documented in the README.
  - **run**: start the controller in the foreground with a configuration file.
  - **validate**: check a configuration file; print every error found and exit non-zero if
    there is any, exit 0 otherwise.
  - **status**: while the controller runs, show for each zone its desired and reported relay
    state and whether it is degraded, and for each room its temperature, target and demand.
  - **version**: print the version.
- X2: an invalid configuration given to **run** prints the errors and exits non-zero without
  connecting to the broker.

## 9. Logging

- L1: every control cycle writes exactly one structured event to standard output, as one JSON
  object per line, with at least these fields. More fields are allowed.

```json
{"event":"control_cycle","schema":1,"ts":"2026-11-01T06:30:00Z",
 "rooms":[{"id":"living","readings":[20.4,20.1],"temperature":20.25,"target":21.0,"demand":true}],
 "zones":[{"id":"ground","desired":"on","reported":"on","degraded":false}]}
```

  `temperature` is `null` for a room with no fresh sensor; `target` is `null` for a room with
  neither target nor schedule. `desired` is `on` or `off`; `reported` is `on`, `off` or
  `unknown`. `ts` is RFC 3339 in UTC.
- L3: logging never affects control. If standard output cannot be written, control continues
  unchanged.
- L4: other events, such as relay commands, mismatches, stale sensors, disconnects and
  ignored payloads, are also JSON lines with an `event` field. Their other fields are up to
  the build.

## 10. Documentation

- R1: the README lets an operator install, configure and run every operation in section 8
  without reading the source, including running `heatctl` as a systemd service on the Pi.

## 11. Out of scope

- HTTP API, web UI, mobile app.
- Prediction, learning, weather data.
- Configuring devices.
- Controlling anything other than the relays in the configuration.

## 12. Features and acceptance criteria

Numbers in the criteria use the defaults in section 4 unless stated otherwise.

### F1 Configuration

- F1.AC1: a valid configuration passes **validate** with exit 0.
- F1.AC2: each rule in section 4 broken on its own makes **validate** exit non-zero and name
  the offending key.
- F1.AC3: an unknown key is reported as an error.

### F2 Sensor readings

- F2.AC1: a room with readings 19.0 and 20.0 from two fresh sensors has temperature 19.5.
- F2.AC2: a reading with `tC` null or with `errors` does not change the room's temperature.
- F2.AC3: a sensor whose last valid reading is older than `stale_after` no longer counts.

### F3 Targets and schedules

- F3.AC1: a room with schedule 06:30 → 21.0 and 22:30 → 18.0 has target 21.0 at 12:00,
  18.0 at 23:00, and 18.0 at 03:00.
- F3.AC2: a room with a fixed target keeps it at every time of day.
- F3.AC3: with the schedule in the section 4 example, the target is 21.0 on Wednesday at
  07:00, 18.0 on Saturday at 07:00, and 21.0 on Saturday at 09:00.
- F3.AC4: with the same schedule, the target on Monday at 05:00 is 18.0, from Sunday at 22:30.
- F3.AC5: a schedule whose only entry is `sun` 10:00 → 20.0 has target 20.0 at every time
  of the week.

### F4 Zone decision

- F4.AC1: target 20.0, a fresh reading of 19.4: the zone's relay is commanded on within one
  cycle.
- F4.AC2: the same room then reports 20.4: the relay stays on. At 20.5 it is commanded off.
- F4.AC3: one room demands heat and another room in the same zone does not: the relay is on.
- F4.AC4: every sensor in a zone goes stale: the relay is commanded off and the zone shows as
  degraded in **status**.
- F4.AC5: a room without sensors never changes its zone's decision.

### F5 Relay control

- F5.AC1: a desired change less than `min_switch_interval` after the last switch is applied
  only once the interval has passed, and not at all if the desired state changed back.
- F5.AC2: a relay that does not report the commanded state within `confirm_timeout` gets the
  command again.
- F5.AC3: `heatctl` is killed with SIGKILL while a relay is on: the relay is off within
  `relay_lease`.
- F5.AC4: a relay reported offline makes its zone degraded.

### F6 Failures, start and stop

- F6.AC1: the broker restarts: `heatctl` reconnects and re-sends desired states within 60 s
  of the broker being back.
- F6.AC2: a malformed payload on a sensor topic is logged and `heatctl` keeps running.
- F6.AC3: no relay command is sent during `startup_grace` unless every room with sensors
  already has a temperature. Control cycles run and are logged during it.
- F6.AC4: SIGTERM turns every controlled relay off and exits 0 within 5 s.
- F6.AC5: `heatctl` runs normally with the filesystem read-only except for one runtime
  directory, in which it creates nothing but a socket for status.
- F6.AC6: `heatctl` is killed and restarted with a zone's relay on and one room at 19.0
  against a target of 20.0: once the room has reported, the relay is on. With the room at
  20.1 instead, inside the band, the relay is commanded off.
- F6.AC7: started while the broker holds a retained reading for a sensor, `heatctl` ignores
  that reading; the room's temperature comes only from messages published after the start.

### F7 Status

- F7.AC1: **status** shows every zone and every room with the fields listed in X1.
- F7.AC2: **status** with no controller running says so and exits non-zero.

### F8 Logging

- F8.AC1: every control cycle produces exactly one `control_cycle` line with the L1 fields.
- F8.AC2: with standard output closed, relay decisions are unchanged.

### F9 Documentation

- F9.AC1: an operator who has only the README can install `heatctl`, write a configuration,
  validate it, run it, read its status, and set it up as a systemd service.
