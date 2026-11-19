# heatctl

`heatctl` keeps the rooms of a house at their target temperature. It reads room
temperatures from Shelly and Zigbee sensors over MQTT, decides for every zone
whether its heating relay should be on, and switches Shelly relays through their
own MQTT interface. It is a single Go binary for Go 1.26 or later, meant to run
as a long-lived service on a Raspberry Pi (linux/arm64).

Everything it publishes is derived from the configuration, the current time and
what the devices report right now: `heatctl` keeps no state across restarts and
writes nothing to disk except a Unix socket for the `status` operation. All
decisions and all logs go to standard output.

The behaviour described here is the contract in [`SPEC.md`](SPEC.md); this
README is the operator manual for it.

- [1. What you need](#1-what-you-need)
- [2. Install](#2-install)
- [3. Configure](#3-configure)
- [4. Validate a configuration](#4-validate-a-configuration)
- [5. Run in the foreground](#5-run-in-the-foreground)
- [6. Show the status](#6-show-the-status)
- [7. Print the version](#7-print-the-version)
- [8. Log output](#8-log-output)
- [9. Run as a systemd service](#9-run-as-a-systemd-service)
- [10. Troubleshooting](#10-troubleshooting)
- [11. Files, exit codes and signals](#11-files-exit-codes-and-signals)
- [12. Building and testing](#12-building-and-testing)

## 1. What you need

- A Raspberry Pi or another Linux/Unix host with a local MQTT broker, for
  example Mosquitto. `heatctl` speaks MQTT 3.1.1 to that broker and to nothing
  else; it needs no internet access.
- One Shelly Gen2 or later device per heating zone (Plus 1, Plus 1PM, Pro 4PM,
  …), with MQTT enabled and status notifications turned on.
- Temperature sensors: Shelly H&T Gen3, a probe on a Shelly Plus Add-on, and/or
  Zigbee sensors published by Zigbee2MQTT on the same broker.
- Go 1.26 or later if you build from source.

## 2. Install

### 2.1 Build the binary

On any machine with Go 1.26+ (the Pi itself works too):

```sh
git clone <repository> heatctl
cd heatctl
make build-arm64          # heatctl-linux-arm64 — for the Pi
make build                # ./heatctl — for this machine
```

Without `make`:

```sh
GOOS=linux GOARCH=arm64 go build -trimpath -o heatctl-linux-arm64 ./cmd/heatctl
```

The binary is statically linked and has no runtime dependencies.

### 2.2 Copy it to the Pi

```sh
scp heatctl-linux-arm64 pi@raspberrypi:/tmp/heatctl
ssh pi@raspberrypi 'sudo install -m 0755 /tmp/heatctl /usr/local/bin/heatctl'
```

Check the installation:

```sh
heatctl version
# heatctl 1.0.0
```

### 2.3 Create the service account and directories

The controller only needs to read its configuration and, when it runs as a
service, write its status socket into one runtime directory.

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin heatctl
sudo install -d -o root -g heatctl -m 0750 /etc/heatctl
```

## 3. Configure

### 3.1 Prepare the devices

**Relays.** In the Shelly web UI (or the Shelly app), open *Settings → MQTT* and

- enable MQTT, set the address of your broker and the credentials if it needs
  any,
- allow control over MQTT, and turn status notifications on (the exact labels
  differ slightly between firmware versions),
- note the *device topic prefix* shown there. `heatctl` uses it as `<device>` in
  the configuration, for example `shellyplus1-a8032ab1c2d3`.

Do the same for every Shelly that provides a temperature channel. Shelly devices
publish their status under `<device>/status/...` and accept commands under
`<device>/command/...`; the payloads `heatctl` uses are the device's own, listed
in `SPEC.md` section 5.

**Zigbee sensors** need no preparation beyond Zigbee2MQTT publishing them on the
same broker. Use the sensor's friendly name from Zigbee2MQTT (for example
`living-display`) in the configuration. If Zigbee2MQTT does not use the default
base topic `zigbee2mqtt`, set `mqtt.zigbee2mqtt_topic`.

### 3.2 Write the configuration

Copy the commented example and edit it:

```sh
sudo install -m 0640 -o root -g heatctl deploy/config.example.yaml /etc/heatctl/config.yaml
sudoedit /etc/heatctl/config.yaml
```

A complete configuration:

```yaml
unit: C

mqtt:
  broker: tcp://192.168.1.10:1883
  client_id: heatctl
  # username: heatctl
  # password: secret
  zigbee2mqtt_topic: zigbee2mqtt

control:
  cycle_interval: 60s
  hysteresis: 0.5
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
          - { zigbee: living-display }
      - id: hallway                 # no sensors: never votes
  - id: upstairs
    relay:
      device: shellypro4pm-f008d1d8b8b8
      switch: 2
    rooms:
      - id: bedroom
        target: 19.0
        sensors:
          - { zigbee: bedroom-display }
```

### 3.3 Configuration reference

Top level:

| Key | Required | Default | Meaning |
|---|---|---|---|
| `unit` | yes | — | `C` or `F`. Every temperature in the file, in the logs and in `status` is in this unit. |
| `mqtt.broker` | yes | — | Broker address, `tcp://host:port`. |
| `mqtt.client_id` | no | `heatctl` | MQTT client id. Must be unique on the broker; set it if you run two instances. |
| `mqtt.username` | no | empty | Broker user name. |
| `mqtt.password` | no | empty | Broker password. |
| `mqtt.zigbee2mqtt_topic` | no | `zigbee2mqtt` | Zigbee2MQTT base topic. Zigbee readings arrive on `<base>/<name>`. |
| `control.cycle_interval` | no | `60s` | How often a control cycle runs in addition to running on every reading. |
| `control.hysteresis` | no | `0.5` (`C`), `1.0` (`F`) | Dead band either side of the target. Must be at least 0. |
| `control.stale_after` | no | `45m` | A sensor whose last valid reading is older than this no longer counts. |
| `control.min_switch_interval` | no | `5m` | A relay is never switched twice within this interval. |
| `control.relay_lease` | no | `15m` | Dead-man lease, see below. Must be longer than `cycle_interval`. |
| `control.startup_grace` | no | `2m` | No relay command is sent before this has passed (or every sensor room has a temperature). |
| `control.confirm_timeout` | no | `30s` | A relay that does not report the commanded state within this time is commanded again. |

Durations use Go syntax: `90s`, `5m`, `1h30m`. Every duration must be positive.

Zones, rooms, relays and sensors:

| Key | Required | Meaning |
|---|---|---|
| `zones[].id` | yes | Zone name, unique in the file. |
| `zones[].relay.device` | yes | MQTT topic prefix of the Shelly that has the relay. |
| `zones[].relay.switch` | yes | Switch channel on that device: an integer, 0 or more. |
| `zones[].rooms[].id` | yes | Room name, unique across the whole house. |
| `zones[].rooms[].target` | one of target/schedule | Fixed target temperature. |
| `zones[].rooms[].schedule` | one of target/schedule | Weekly schedule, see below. |
| `zones[].rooms[].sensors` | no | Zero or more sensors; without sensors a room never votes. |
| `sensors[].device`, `sensors[].component` | for a Shelly sensor | Device topic prefix and temperature channel id. |
| `sensors[].zigbee` | for a Zigbee sensor | Friendly name of the sensor in Zigbee2MQTT. |

Rules the file has to obey — `heatctl validate` reports every broken one with
its line number:

- `unit` is required and is `C` or `F`; numbers may be written `19` or `19.0`.
- Targets are between 7.0 and 25.0 with unit `C`, and 45.0 and 77.0 with unit
  `F`, inclusive.
- Every zone has exactly one relay; no two zones share a relay channel.
- Room ids are unique across the whole house; zone ids are unique as well.
- A sensor is listed under at most one room. It is either `{ device, component }`
  or `{ zigbee: name }`, never both.
- A room with sensors has either `target` or `schedule`; a room without sensors
  may have neither.
- A schedule has at least one entry, and every entry has `from` (`HH:MM`, local
  time) and `target`. `days` is optional: `mon`, `tue`, `wed`, `thu`, `fri`,
  `sat`, `sun`, without repeats; without `days` an entry applies every day.
- No two entries may apply at the same time on the same day.
- Unknown keys, duplicate keys and wrong types are errors.

### 3.4 How the controller decides

- A **sensor reading** is valid when it carries a number (`tC` for Shelly,
  `temperature` for Zigbee) and, for a Shelly sensor, no `errors` field.
  Readings outside −40…85 °C are ignored. Retained sensor messages that the
  broker delivers at subscribe time are ignored as well: their age is unknown.
- A **room temperature** is the mean of the room's fresh sensors' last valid
  readings. A room without a fresh sensor has no temperature and does not vote.
- A **target** is the fixed `target` when one is set. A schedule uses the entry
  that most recently started: the latest occurrence at or before the local time,
  searching back across days and weeks. Early on Monday, Sunday's 22:30 entry is
  still in force.
- A **room demands heat** when its temperature is at or below
  `target − hysteresis`, stops demanding at or above `target + hysteresis`, and
  keeps its previous demand in between. Inside the band after a restart it
  starts without demand.
- A **zone's relay is on** when at least one room in the zone demands heat. A
  zone in which no room has a temperature is degraded and its relay is off.
- A **relay is switched** when its desired state changes. A change that comes
  less than `min_switch_interval` after the previous switch waits, and is
  dropped if the desired state changes back first.
- Every relay command that turns a relay on carries `relay_lease` rounded up to
  whole seconds (`on,900` for the default 15m). The relay turns itself off when
  that lease expires, so a crashed `heatctl`, a killed process or a broker that
  went away leaves no relay heating for longer than the lease. While `heatctl`
  runs, it refreshes the lease every half lease, so a relay that should be on
  stays on.
- If a relay does not report the commanded state within `confirm_timeout`,
  `heatctl` sends the command again (at most once per `confirm_timeout`) and
  logs `relay_mismatch`. A relay that reports itself offline makes its zone
  degraded.
- At **start** `heatctl` requests the status of every relay and sends no relay
  command until `startup_grace` has passed or every room with sensors has a
  temperature. Then the first cycle decides every relay from scratch.
- If the **broker goes away**, `heatctl` keeps running, reconnects with backoff
  capped at 60 s, and after reconnecting subscribes again and re-sends every
  relay's desired state.
- On **SIGINT or SIGTERM** it turns off every relay it controls, regardless of
  `min_switch_interval`, and exits 0.

### 3.5 A Fahrenheit configuration

With `unit: F` every temperature in the file is °F; sensor readings arrive in °C
and are converted as they arrive. Targets must then be inside 45…77, and the
default hysteresis is 1.0:

```yaml
unit: F
# ...
control:
  hysteresis: 1.0
zones:
  - id: ground
    relay: { device: shellyplus1-a8032ab1c2d3, switch: 0 }
    rooms:
      - id: living
        target: 68.0
        sensors: [{ device: shellyhtg3-84fce63a1b2c, component: 0 }]
```

## 4. Validate a configuration

```sh
heatctl validate /etc/heatctl/config.yaml
# /etc/heatctl/config.yaml: configuration is valid (unit C, 2 zones, 3 rooms, 3 sensors)
```

The path may also be given as `--config PATH`. Every problem in the file is
printed with its line, the key and what is wrong, and the exit status is 1:

```console
$ heatctl validate /etc/heatctl/config.yaml
heatctl: configuration /etc/heatctl/config.yaml is not valid:
  line 24: control.relay_lease: must be longer than control.cycle_interval (30s is not longer than 1m0s)
  line 49: zones[1].rooms[0].target: must be between 7 and 25 °C, got 99
$ echo $?
1
```

A valid file exits 0. Run `validate` after every edit; `heatctl run` performs
the same check and refuses to start (and refuses to contact the broker) when the
file is not valid.

## 5. Run in the foreground

```sh
heatctl run --config /etc/heatctl/config.yaml
```

`run` stays in the foreground, writes its JSON log lines to standard output and
turns the relays off on Ctrl-C (SIGINT) or SIGTERM before exiting 0. Set
`--socket PATH` — or the environment variable `HEATCTL_SOCKET` — to choose where
the status socket is created; see [section 11](#11-files-exit-codes-and-signals)
for the default. Without a terminal you can follow the log with
`heatctl run --config … | tee` or through the systemd journal, as in section 9.

## 6. Show the status

While a controller runs, `status` asks it over its Unix socket and prints the
zones and rooms:

```console
$ heatctl status
unit C, running control cycles, updated 2026-11-12T07:30:00Z

ZONES
  ID        RELAY                        DESIRED  REPORTED  DEGRADED
  ground    shellyplus1-a8032ab1c2d3/0   on       on        no
  upstairs  shellypro4pm-f008d1d8b8b8/2  off      unknown   yes

ROOMS
  ID       ZONE      TEMPERATURE (°C)  TARGET (°C)  DEMAND
  living   ground    19.40             21.00        on
  hallway  ground    -                 -            off
  bedroom  upstairs  22.10             19.00        off
```

`REPORTED` is `unknown` until the relay has reported its state, `-` means the
room has no fresh temperature or no target, and `DEGRADED` is `yes` when the zone
has no temperature at all or its relay is reported offline.

- `heatctl status --json` prints the same information as JSON, for scripts.
- `heatctl status --socket PATH` uses a socket other than the default.
- When no controller is listening, `status` says
  `heatctl: no heatctl controller is running at <path>: …` and exits 1.

The socket belongs to the user that runs the controller and is readable only by
it. When the controller runs as the `heatctl` service user, ask it as that user:

```sh
sudo -u heatctl heatctl status
```

## 7. Print the version

```sh
heatctl version
# heatctl 1.0.0
```

Binaries built with `make` also carry the commit and build date:
`heatctl 1.0.0 (commit 1a2b3c4, built 2026-11-12T07:00:00Z)`.

## 8. Log output

Standard output is one JSON object per line. Every control cycle writes exactly
one `control_cycle` line with the fields the specification requires:

```json
{"event":"control_cycle","rooms":[{"id":"living","zone":"ground","readings":[19.4],"temperature":19.4,"target":20,"demand":true},{"id":"bedroom","zone":"upstairs","readings":[22],"temperature":22,"target":19,"demand":false},{"id":"hallway","zone":"upstairs","readings":[],"temperature":null,"target":null,"demand":false}],"schema":1,"seq":1,"ts":"2026-11-12T01:01:17Z","unit":"C","zones":[{"id":"ground","relay":"shelly1/0","desired":"on","reported":"off","degraded":false},{"id":"upstairs","relay":"shelly2/1","desired":"off","reported":"unknown","degraded":false}]}
```

`temperature` and `target` are `null` when a room has no fresh sensor or no
target; `ts` is RFC 3339 in UTC; `seq` counts the cycles of this run.

Other events carry an `event` field and their own fields, for example:

```json
{"command":"on,900","desired":"on","device":"shellyplus1-a8032ab1c2d3","event":"relay_command","reason":"desired_change","relay":"shellyplus1-a8032ab1c2d3/0","schema":1,"switch":0,"ts":"2026-11-12T01:01:17Z","zone":"ground"}
```

The events you will see are `startup`, `mqtt_connecting`, `mqtt_connected`,
`mqtt_disconnected`, `mqtt_connect_failed`, `subscriptions_active`,
`startup_wait`, `control_started`, `control_cycle`, `sensor_reading`,
`sensor_stale`, `sensor_fresh`, `ignored_payload`, `ignored_reading`,
`ignored_retained`, `relay_status_request`, `relay_status_request_failed`, `relay_reported`,
`relay_availability`, `relay_command`, `relay_command_failed`,
`relay_switch_pending`, `relay_mismatch`, `shutdown`, and on problems
`status_socket_unavailable`, `subscribe_failed`, `fatal`.

Logging never influences control: lines are queued and dropped if the reader
cannot keep up, and a closed standard output is ignored. Control continues, and
the relays still go off on shutdown.

## 9. Run as a systemd service

The unit in [`deploy/heatctl.service`](deploy/heatctl.service) runs the
controller as the `heatctl` user, restarts it when it dies, and leaves the
filesystem read-only except for `/run/heatctl`, where the status socket is
created.

```sh
sudo install -m 0755 /usr/local/bin/heatctl /usr/local/bin/heatctl   # if not done yet
sudo install -d -m 0755 /usr/local/share/doc/heatctl
sudo install -m 0644 README.md /usr/local/share/doc/heatctl/README.md
sudo install -m 0644 deploy/heatctl.service /etc/systemd/system/heatctl.service
sudo systemctl daemon-reload
sudo systemctl enable --now heatctl
```

Check that it is up:

```sh
systemctl status heatctl
journalctl -u heatctl -n 20 --no-pager
```

The log lines land in the journal because systemd captures standard output:

```console
$ journalctl -u heatctl -f
Nov 12 07:30:00 raspberrypi heatctl[412]: {"event":"mqtt_connected","broker":"tcp://192.168.1.10:1883",…}
Nov 12 07:30:02 raspberrypi heatctl[412]: {"event":"control_cycle","schema":1,…}
```

Ask the running service for its status (the socket is in `/run/heatctl` and
belongs to the service user):

```sh
sudo -u heatctl /usr/local/bin/heatctl status
```

Restart after a configuration change:

```sh
sudo systemctl restart heatctl
```

The unit sets `HEATCTL_SOCKET=/run/heatctl/heatctl.sock`, so `run` and `status`
agree on the socket without extra flags. `RuntimeDirectory=heatctl` makes
systemd create that directory with the service user as owner and remove it on
stop. If your broker needs credentials, put them in the configuration file,
which is readable only by root and the `heatctl` group.

For a user-level service instead (`systemctl --user`), copy the same unit to
`~/.config/systemd/user/heatctl.service`, drop `User=`, `Group=` and
`RuntimeDirectory=` (the socket then defaults to `$XDG_RUNTIME_DIR/heatctl.sock`)
and set `WantedBy=default.target`.

## 10. Troubleshooting

**`status` says no controller is running.** The controller is not running, has
crashed, or uses a different socket. Check `systemctl status heatctl` and start
it with `--socket PATH` (or `HEATCTL_SOCKET`) if you changed the path.

**`another heatctl is already running`.** Another instance owns the socket.
Stop it first; the message names the socket.

**`mqtt_connect_failed` lines repeat.** The broker address, credentials or the
broker itself are wrong. `heatctl` retries with backoff capped at 60 s and does
not exit. Check the broker with `mosquitto_sub -h <host> -t '#' -v` on the Pi.

**A zone shows `DEGRADED yes`.** Either no room in the zone has a fresh reading,
or the relay reports itself offline. With `--json` the `online` field says which
of the two. Check the sensor's topic and that the device publishes (for Zigbee,
that Zigbee2MQTT knows the friendly name), and check the broker's retained
`<device>/online` message after a broker restart.

**A relay never switches.** Its zone has no demanding room. Look at the
`control_cycle` line: `temperature`, `target` and `demand` per room, and
`desired`/`reported` per zone. A relay that reports `unknown` for long, or a
`relay_mismatch` line, means the status messages of the device do not reach
`heatctl` — check that MQTT status notifications are on for that device.

**The relay switches on and off too often.** Raise `hysteresis` or
`min_switch_interval`, or make the sensor report less often.

**Times are off by an hour.** Schedules use the host's local time. Check
`timedatectl` on the Pi; `heatctl` follows the system time zone.

## 11. Files, exit codes and signals

Files:

| Path | Purpose |
|---|---|
| `/etc/heatctl/config.yaml` | configuration, read at start and by `validate` |
| `/etc/systemd/system/heatctl.service` | the service unit |
| `/usr/local/bin/heatctl` | the binary |
| the status socket | created by `run`, removed on exit; not state |

The status socket, in order of precedence:

1. `--socket PATH`,
2. `$HEATCTL_SOCKET`,
3. `$XDG_RUNTIME_DIR/heatctl.sock`,
4. `/run/user/<uid>/heatctl.sock`,
5. `/run/heatctl/heatctl.sock` for root, otherwise
   `$TMPDIR/heatctl-<uid>/heatctl.sock` (usually `/tmp/heatctl-<uid>/…`).

`heatctl` writes nothing else: during `run` no file is created except the
socket, and the socket's directory is created if needed.

Exit codes:

| Code | Meaning |
|---|---|
| 0 | success; `run` also exits 0 after SIGINT/SIGTERM |
| 1 | invalid configuration, runtime error, or `status` without a controller |
| 2 | usage error: unknown operation, missing or repeated arguments, bad flags |

Signals to `run`:

| Signal | Effect |
|---|---|
| SIGINT, SIGTERM | every controlled relay off, then exit 0 within 5 s |
| SIGPIPE | ignored; a closed stdout never stops control |

## 12. Building and testing

```sh
make build        # ./heatctl for this machine
make build-arm64  # ./heatctl-linux-arm64 for the Pi
make check        # go vet ./... and go test ./...
```

The test suite includes an end-to-end suite under `internal/e2e` that starts an
embedded MQTT broker, simulates Shelly relays and both sensor kinds, and runs
the real binary through heating, hysteresis, startup grace, retained messages,
lease expiry, broker loss and restart, shutdown, and closed-stdout scenarios:

```sh
go test ./...
go test ./internal/e2e -v
```
