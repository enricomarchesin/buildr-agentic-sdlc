# heatctl

`heatctl` controls home heating. It reads room temperatures from Shelly and Zigbee2MQTT
sensors and switches Shelly relays, all through one MQTT broker. It is a single Go binary
for a Raspberry Pi (linux/arm64). It keeps no state on disk: after a restart it decides
everything again from the configuration, the clock and what the devices report.

## Install

Requires Go 1.26 or later to build.

```sh
make arm64                      # -> dist/heatctl-linux-arm64 (or `make build` for this machine)
sudo install -m 0755 dist/heatctl-linux-arm64 /usr/local/bin/heatctl
heatctl version
```

## Configure

Copy `examples/heatctl.yaml` to `/etc/heatctl/heatctl.yaml` and edit it.

- `unit`: `C` or `F`; every temperature in the file, in logs and in status uses it.
- `mqtt.broker` (required), `client_id`, `username`, `password`, `zigbee2mqtt_topic`.
- `control` (all optional): `cycle_interval` 60s, `hysteresis` 0.5 (1.0 for F), `stale_after` 45m,
  `min_switch_interval` 5m, `relay_lease` 15m, `startup_grace` 2m, `confirm_timeout` 30s.
- `zones`: each has an `id`, exactly one `relay` (`device` = the Shelly's MQTT topic prefix,
  `switch` = channel) and `rooms`.
- A room has `id`, optional `sensors` and either a fixed `target` or a weekly `schedule`
  (entries `from: "HH:MM"`, `target`, optional `days` of mon..sun). Sensors are
  `{device, component}` for a Shelly or `{zigbee: friendly-name}` for Zigbee2MQTT.
  A room without sensors never votes.

Targets must be 7–25 °C (45–77 °F). Unknown keys are errors. Shelly devices need MQTT enabled
with status notifications on; heatctl never changes device configuration.

## Operations

```sh
heatctl validate /etc/heatctl/heatctl.yaml     # prints every error, exit 1 if any
heatctl run /etc/heatctl/heatctl.yaml          # foreground; JSON-lines events on stdout
heatctl status                                 # zones, relays, rooms (add -json for JSON)
heatctl version
```

`run` exits non-zero without connecting if the configuration is invalid. SIGINT/SIGTERM turn all
relays off and exit 0 within 5 s.

`status` talks to the running controller over a unix socket, default
`$XDG_RUNTIME_DIR/heatctl.sock`, else `/run/heatctl/heatctl.sock`. Override with `-socket PATH` (both
`run` and `status`) or `HEATCTL_SOCKET`. Without a running controller it says so and exits 1.

## How it decides

Room temperature is the mean of sensors with a valid reading newer than `stale_after`.
A room demands heat at or below target − hysteresis and stops at or above target + hysteresis
(in between it keeps its previous demand). A zone's relay is on if any room demands heat;
a zone with no room temperature, or with an offline relay, is *degraded* and off.
Relays are not switched twice within `min_switch_interval` (shutdown excepted). Readings outside
−40..85 °C, null values, Shelly readings with `errors`, and malformed payloads are ignored and logged.
Retained sensor messages are ignored.

After start, nothing is sent to relays until `startup_grace` passes or every room with sensors
has a temperature; then the first cycle decides all relays and commands each one.

**Dead-man.** Every "on" is sent as `on,<relay_lease seconds>` and refreshed every third of the
lease while the relay should stay on. If heatctl dies or loses the broker, the relay turns itself off
within `relay_lease`. A relay whose reported state differs from the desired one for `confirm_timeout`
gets the command again.

## Logging

Standard output, one JSON object per line. Each control cycle writes one `control_cycle` event
(`schema` 1, `unit`, `rooms`, `zones`). Other events: `relay_command`, `relay_mismatch`,
`sensor_stale`, `relay_online`, `mqtt_connected`, `mqtt_disconnected`, `payload_ignored`, and so on.
A failing stdout never affects control. Under systemd read it with `journalctl -u heatctl -f`.

## systemd

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin heatctl
sudo install -d /etc/heatctl
sudo install -m 0640 -g heatctl heatctl.yaml /etc/heatctl/heatctl.yaml
sudo cp examples/heatctl.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now heatctl
sudo -u heatctl heatctl status        # or: HEATCTL_SOCKET=/run/heatctl/heatctl.sock heatctl status
```

The unit makes the filesystem read-only except the runtime directory `/run/heatctl`, which holds
only the status socket. Restarting (`systemctl restart heatctl`) is safe: relays are turned off on
stop and decided afresh on start. If the status socket is not readable by your user, run `status` as
the `heatctl` user (socket mode is 0600).
