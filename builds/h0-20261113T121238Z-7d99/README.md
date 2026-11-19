# heatctl

`heatctl` controls the heating of a home. It reads room temperatures from
sensors, compares them with the targets you configure, and switches the relays
that heat each zone of the house. Everything goes through your MQTT broker:

- relays: Shelly Gen2+ devices (Plus 1, Pro 4PM, …);
- sensors: Shelly temperature channels (H&T Gen3, Plus Add-on probes, …) and
  Zigbee sensors published by Zigbee2MQTT.

It is a single binary with no dependencies, made to run on a Raspberry Pi as a
systemd service. It has no web UI, needs no internet, and writes nothing to
disk: every start decides from scratch from the configuration, the clock and
what the devices report.

Contents: [Install](#install) · [Prepare the devices](#prepare-the-devices) ·
[Configure](#configure) · [Commands](#commands) ·
[Run as a systemd service](#run-as-a-systemd-service) ·
[How it behaves](#how-it-behaves) · [Logs](#logs) ·
[Troubleshooting](#troubleshooting)

## Install

You need Go 1.26 or later on the machine you build on (it can be your laptop;
the Pi does not need Go).

```sh
git clone <this repository> heatctl
cd heatctl
make pi                          # → dist/heatctl-linux-arm64 (64-bit Raspberry Pi OS)
```

Without `make`:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath \
  -ldflags "-s -w -X main.version=1.0.0" -o dist/heatctl-linux-arm64 ./cmd/heatctl
```

For 32-bit Raspberry Pi OS use `GOARCH=arm GOARM=7`. To build for the machine
you are on, run `make build` (→ `./heatctl`).

Copy the binary to the Pi and install it:

```sh
scp dist/heatctl-linux-arm64 pi@raspberrypi.local:/tmp/heatctl
ssh pi@raspberrypi.local
sudo install -m 0755 /tmp/heatctl /usr/local/bin/heatctl
heatctl version
```

Run the tests with `make test` (unit tests plus end-to-end tests against an
in-process MQTT broker and a simulated Shelly relay).

## Prepare the devices

`heatctl` never changes a device's configuration; set the devices up once:

- **Shelly relays and Shelly sensors** (Gen2 or later): in the device's web UI,
  *Settings → MQTT*: enable MQTT, set the broker address, keep the default
  topic prefix (e.g. `shellyplus1-a8032ab1c2d3`) or set your own, and enable
  **"Generic status update over MQTT"** (status notifications). The topic prefix
  is what you write as `device` in the configuration.
- **Zigbee sensors**: pair them with Zigbee2MQTT on the same broker. Their
  friendly name in Zigbee2MQTT is what you write as `zigbee`.
- Do **not** give the relays an auto-off or auto-on timer of their own and leave
  the relay's "power on default" at *off*: `heatctl` manages the off-timer
  itself (see [Safety](#safety-the-relay-lease)).

You can check that devices publish with any MQTT client, for example
`mosquitto_sub -h 192.168.1.10 -v -t 'shellyplus1-a8032ab1c2d3/#' -t 'zigbee2mqtt/#'`.

## Configure

The configuration is one YAML file, conventionally `/etc/heatctl/config.yaml`.
A complete example is in [`examples/config.yaml`](examples/config.yaml):

```yaml
unit: C                             # required: C or F

mqtt:
  broker: tcp://192.168.1.10:1883   # required
  client_id: heatctl                # optional, default heatctl
  username: ""                      # optional
  password: ""                      # optional
  zigbee2mqtt_topic: zigbee2mqtt    # optional, default zigbee2mqtt: Zigbee2MQTT's base topic

control:                            # every key optional, defaults shown
  cycle_interval: 60s
  hysteresis: 0.5                   # either side of the target; 0.5 for C, 1.0 for F
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

### Keys

| Key | Meaning |
|---|---|
| `unit` | `C` or `F`. **Every** temperature in the file, in the logs and in `status` is in this unit. Sensors report °C; with `F` each reading is converted as it arrives. |
| `mqtt.broker` | Broker URL: `tcp://host:1883` (also `mqtt://`; `ssl://`, `tls://`, `mqtts://` for TLS). |
| `mqtt.client_id` | MQTT client id. Must be unique on the broker. |
| `mqtt.username`, `mqtt.password` | Broker credentials, if the broker needs them. |
| `mqtt.zigbee2mqtt_topic` | Zigbee2MQTT's base topic (`base_topic` in its configuration). |
| `control.cycle_interval` | How often every zone is re-evaluated (a reading also triggers an evaluation). |
| `control.hysteresis` | A room starts asking for heat at *target − hysteresis* and stops at *target + hysteresis*. Default 0.5 with `C`, 1.0 with `F`. |
| `control.stale_after` | A sensor whose last valid reading is older than this no longer counts. |
| `control.min_switch_interval` | A relay is never switched twice within this interval. |
| `control.relay_lease` | Every "on" command tells the relay to switch itself off after this long unless renewed; it bounds how long heating stays on if `heatctl` or the broker fails. |
| `control.startup_grace` | At start, how long to wait for sensor readings before the first decision. |
| `control.confirm_timeout` | If a relay's reported state still differs from the command after this long, the command is sent again. |
| `zones[].id` | Zone name, unique. |
| `zones[].relay.device`, `.switch` | The relay: Shelly topic prefix and switch channel (0, 1, …). |
| `zones[].rooms[].id` | Room name, unique across the whole house. |
| `rooms[].target` | Fixed target temperature. |
| `rooms[].schedule` | Weekly schedule: list of `{ from: "HH:MM", target: N, days: [...] }`. |
| `rooms[].sensors` | List of sensors: `{ device: <shelly prefix>, component: <n> }` or `{ zigbee: <friendly name> }`. |

Durations use Go syntax: `90s`, `5m`, `1h30m`.

### Rules

`heatctl validate` checks all of these and reports every violation with the
line and the key at fault:

- Unknown keys are errors (a typo never silently falls back to a default).
- Each zone has exactly one relay and one or more rooms. Zone ids are unique;
  room ids are unique across the whole house.
- Two zones never share a relay channel (same `device` and `switch`). A sensor
  is listed under at most one room.
- `switch` and `component` are integers, 0 or more.
- A sensor is either `{ device, component }` or `{ zigbee }`, never both.
- A room with sensors has either `target` or `schedule`, not both. A room
  without sensors may have neither; it is then shown but never affects its zone.
- Targets are between 7.0 and 25.0 with `C`, 45.0 and 77.0 with `F`
  (inclusive). `19` and `19.0` are the same.
- A schedule has at least one entry; each entry has `from` (`HH:MM`, 24-hour
  local time, quoted) and `target`. `days` is optional: a non-empty list of
  `mon tue wed thu fri sat sun` without repeats; without `days` the entry
  applies every day.
- No two entries may apply at the same time on the same day (e.g. an every-day
  entry and a `sun` entry both at 22:30).
- `hysteresis` is at least 0; every duration is positive; `relay_lease` is
  longer than `cycle_interval` (and at least 2s).

### How schedules work

At any moment a room uses the schedule entry that **most recently started**,
looking back across days and across the week. With the example above:

- Wednesday 07:00 → 21.0 (weekday 06:30 entry);
- Saturday 07:00 → 18.0 (Friday 22:30 is still in force; the weekend entry starts at 08:00);
- Monday 05:00 → 18.0 (from Sunday 22:30).

A schedule with a single entry, e.g. `sun 10:00 → 20.0`, applies all week.

Schedules use the Pi's local time zone (`timedatectl set-timezone Europe/Rome`
to set it, or `Environment=TZ=Europe/Rome` in the service). Make sure the Pi's
clock is synchronised (Raspberry Pi OS does this with `systemd-timesyncd` by
default).

## Commands

```
heatctl run      [-socket PATH] CONFIG   run the controller in the foreground
heatctl validate CONFIG                  check a configuration file
heatctl status   [-socket PATH] [-json]  show the state of the running controller
heatctl version                          print the version
heatctl help                             show help
```

`CONFIG` can also be passed as `-config PATH`. The status socket path defaults
to `$HEATCTL_SOCKET`, or `/run/heatctl/heatctl.sock`.

### validate

```sh
$ heatctl validate /etc/heatctl/config.yaml
/etc/heatctl/config.yaml: OK (unit C, 2 zones, 3 rooms, 3 sensors)

$ heatctl validate broken.yaml
broken.yaml: line 14: zones[0].rooms[0].schedule[0].from: must be HH:MM local time (00:00 to 23:59), not "25:00"
broken.yaml: line 19: zones[1].rooms[0].id: room id "living" is already used by zones[0].rooms[0] (room ids are unique across the house)
broken.yaml: 2 errors
```

Exit status: 0 if valid, 1 if there is any error, 2 for a usage error.

### run

```sh
heatctl run /etc/heatctl/config.yaml
```

Runs in the foreground until SIGINT (Ctrl-C) or SIGTERM. It writes JSON log
lines to standard output (see [Logs](#logs)). An invalid configuration is
reported on standard error and `run` exits 1 without connecting to the broker.

To try it as a normal user, put the socket somewhere you can write:

```sh
heatctl run -socket /tmp/heatctl.sock ./config.yaml
heatctl status -socket /tmp/heatctl.sock      # in another terminal
```

`-socket ""` disables the status socket. If the socket cannot be created
(e.g. `/run/heatctl` does not exist), `heatctl` logs `status_socket_failed`
and keeps controlling the heating.

On stop (SIGINT or SIGTERM), every relay is switched off and `heatctl` exits
with status 0 within 5 seconds.

### status

```
$ heatctl status
heatctl 1.0.0, up 3h12m5s, broker connected, unit °C, at 2026-11-01T06:31:00Z

zone ground  relay shellyplus1-a8032ab1c2d3 switch:0
  desired on, commanded on, reported on, relay online, degraded no
  ROOM     TEMPERATURE  TARGET  DEMAND  SENSORS
  living   20.25        21.0    yes     2/2 fresh
  hallway  -            -       no      none
    living / shellyhtg3-84fce63a1b2c temperature:0: 20.4, 120s ago, fresh
    living / zigbee:living-display: 20.1, 300s ago, fresh

zone upstairs  relay shellypro4pm-f008d1d8b8b8 switch:2
  desired off, commanded off, reported off, relay online, degraded no
  ...
```

- **desired**: what the zone should be (from the last control cycle);
  **commanded**: what was last sent (differs from desired while a switch waits
  for `min_switch_interval`, marked *switch pending*); **reported**: what the
  relay says (`unknown` until it reports).
- **degraded**: no room in the zone has a temperature (relay kept off), or
  the relay is reported offline.
- Per room: temperature (mean of fresh sensors, `-` if none), current target,
  and whether it asks for heat.

`heatctl status -json` prints the same information as JSON. If no controller
is running, `status` says so on standard error and exits 1. Talking to the
socket of the systemd service needs root or membership of the `heatctl` group
(`sudo heatctl status`).

### version

`heatctl version` prints the version set at build time (`make` uses
`git describe`).

## Run as a systemd service

On the Pi:

```sh
# 1. a system user for the service
sudo useradd --system --no-create-home --shell /usr/sbin/nologin heatctl

# 2. the configuration (readable by the service, which may hold the broker password)
sudo mkdir -p /etc/heatctl
sudo cp config.yaml /etc/heatctl/config.yaml
sudo chown root:heatctl /etc/heatctl/config.yaml
sudo chmod 0640 /etc/heatctl/config.yaml
sudo heatctl validate /etc/heatctl/config.yaml

# 3. the unit (deploy/heatctl.service in this repository)
sudo cp deploy/heatctl.service /etc/systemd/system/heatctl.service
sudo systemctl daemon-reload
sudo systemctl enable --now heatctl

# 4. check
systemctl status heatctl
sudo heatctl status
journalctl -u heatctl -f            # live JSON log
```

Optionally add yourself to the group to run `heatctl status` without sudo:
`sudo usermod -aG heatctl $USER` (log in again afterwards).

The unit ([`deploy/heatctl.service`](deploy/heatctl.service)) runs
`heatctl run /etc/heatctl/config.yaml` as the `heatctl` user, restarts it if it
stops (`Restart=always`), makes the whole filesystem read-only
(`ProtectSystem=strict`) and gives it `/run/heatctl` (`RuntimeDirectory=`) for
the status socket, which is the only file it creates. `systemctl stop heatctl`
sends SIGTERM: all relays go off and the service stops within 5 seconds.

After editing the configuration:

```sh
sudo heatctl validate /etc/heatctl/config.yaml && sudo systemctl restart heatctl
```

A restart is safe: `heatctl` keeps no state and decides every relay afresh.

## How it behaves

**Readings.** A Shelly sensor reading is `tC` from
`<device>/status/temperature:<n>`; it is valid if `tC` is a number and there
is no `errors` field. A Zigbee reading is `temperature` from
`<zigbee2mqtt_topic>/<name>` (other fields are ignored; messages without
`temperature`, such as a battery update, are not readings). Readings outside
−40…85 °C, malformed payloads and retained messages delivered at subscribe time
(their age is unknown) are ignored and logged. A sensor is *fresh* for
`stale_after` after its last valid reading.

**Rooms.** A room's temperature is the mean of its fresh sensors. It starts
asking for heat (*demand*) at or below *target − hysteresis* and stops at or
above *target + hysteresis*; in between it keeps what it had (starting
without demand).

**Zones.** A zone's relay should be on if at least one room in it demands
heat. Rooms without a temperature do not vote. If no room in the zone has a
temperature the zone is **degraded** and its relay is kept off.

**Control cycle.** Every zone is evaluated on every valid reading and every
`cycle_interval`, and each evaluation logs one `control_cycle` line.

**Switching.** A relay is not switched twice within `min_switch_interval`; a
change that comes too soon waits and is dropped if the wish reverts in the
meantime. After a start the first command is never delayed.

**Confirmation.** Relays report their state. If the reported state differs
from the command for longer than `confirm_timeout`, the command is re-sent (at
most once per `confirm_timeout`) and a `relay_mismatch` is logged. A relay
reported offline (`<device>/online` = `false`) marks its zone degraded; when it
comes back the current command is sent again.

### Safety: the relay lease

`heatctl` never sends a plain `on`. It sends `on,<seconds>` with
`relay_lease` (default `on,900`): the relay turns itself on and switches itself
**off** after 15 minutes unless told again. While a zone should heat,
`heatctl` renews the lease every third of `relay_lease` (every 5 minutes by
default), so a relay that should be on stays on without gaps. If `heatctl`
crashes, is killed, or loses the broker, nobody renews the lease and every
relay it turned on is off within `relay_lease`, with no action needed.

### Start, stop and failures

- **Start.** `heatctl` asks every relay for its status and waits until every
  room with sensors has a temperature, or `startup_grace` has passed, whichever
  comes first. No relay command is sent and no control cycle runs before then.
  The first cycle decides every relay from scratch and commands it (a room
  inside the hysteresis band starts without demand).
- **Broker loss.** `heatctl` keeps running, logs `mqtt_disconnected`, and
  retries with a backoff capped at 30 seconds. After reconnecting it
  resubscribes, asks relays for their status, and re-sends every relay's
  current command (`mqtt_connected`). Commands are never queued while
  disconnected.
- **Stop.** On SIGINT/SIGTERM all relays are switched off (regardless of
  `min_switch_interval`) and `heatctl` exits 0 within 5 seconds.
- **Bad input** never stops `heatctl`; it is logged and ignored.

### MQTT topics used

| Direction | Topic | Payload |
|---|---|---|
| subscribe | `<device>/status/switch:<n>` | relay status JSON (`output`) |
| subscribe | `<device>/online` | `true` / `false` |
| subscribe | `<device>/status/temperature:<n>` | Shelly reading JSON (`tC`) |
| subscribe | `<zigbee2mqtt_topic>/<name>` | Zigbee2MQTT JSON (`temperature`) |
| publish | `<device>/command/switch:<n>` | `on,<lease seconds>`, `off`, `status_update` |

Everything is published with QoS 1 and never retained. MQTT 3.1.1.

## Logs

Standard output carries one JSON object per line, each with an `event` and a
`ts` (RFC 3339, UTC). Under systemd they go to the journal
(`journalctl -u heatctl -o cat | jq .`). Logging never affects control: if
standard output is closed, full or broken, control carries on unchanged.

Every control cycle writes exactly one `control_cycle` event:

```json
{"event":"control_cycle","schema":1,"ts":"2026-11-01T06:30:00Z","unit":"C","cycle":42,
 "rooms":[{"id":"living","zone":"ground","readings":[20.4,20.1],"temperature":20.25,"target":21,"demand":true}],
 "zones":[{"id":"ground","desired":"on","reported":"on","degraded":false,"relay_online":"online","reason":"demand"}]}
```

`readings` are the fresh sensors' readings; `temperature` is `null` for a room
with no fresh sensor and `target` is `null` for a room with neither target nor
schedule. `desired` is `on`/`off`; `reported` is `on`, `off` or `unknown`.

Other events:

| Event | When |
|---|---|
| `started`, `startup_complete`, `stopping`, `stopped` | lifecycle |
| `status_socket`, `status_socket_failed` | status socket created / not available |
| `mqtt_connecting`, `mqtt_connected`, `mqtt_disconnected`, `mqtt_subscribe_failed` | broker connection |
| `relay_command`, `relay_command_failed` | a command was sent (with `reason`: `initial`, `switch`, `lease_refresh`, `resend`, `confirm_timeout`, `shutdown`) or could not be |
| `switch_deferred`, `switch_dropped` | a switch waits for `min_switch_interval` / was cancelled because the wish reverted |
| `relay_reported`, `relay_availability`, `relay_mismatch` | relay state, online/offline, reported ≠ commanded beyond `confirm_timeout` |
| `sensor_fresh`, `sensor_stale` | a sensor (re)starts or stops counting |
| `reading_invalid`, `reading_out_of_range`, `reading_ignored`, `payload_invalid`, `status_ignored`, `topic_ignored` | input that was ignored, with the reason |
| `mqtt_publish_failed`, `mqtt_publish_timeout`, `status_request_failed` | publishing problems |

## Troubleshooting

- **`heatctl status` says "not running"**: the service is stopped, or you are
  looking at a different socket (`-socket`, `$HEATCTL_SOCKET`). Permission
  errors mean you need `sudo` or the `heatctl` group.
- **A zone stays degraded**: none of its sensors has reported within
  `stale_after`. Check the sensor topic with `mosquitto_sub` and the `device`,
  `component` or `zigbee` name in the configuration. Battery sensors may report
  only on change; increase `stale_after` if they are quiet for long periods.
- **`relay_mismatch` repeats**: the relay does not report the commanded state.
  Check that MQTT status notifications are enabled on the Shelly and that the
  `device` prefix and `switch` id are right.
- **`reported` stays `unknown`**: the relay never answered `status_update`;
  same checks as above.
- **Nothing happens for two minutes after start**: that is `startup_grace`
  waiting for a reading from every room; it ends early once every room with
  sensors has a temperature.
