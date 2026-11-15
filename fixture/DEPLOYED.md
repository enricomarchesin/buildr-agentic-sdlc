# fixture/DEPLOYED.md

Which build runs in the house, since when, and how its soak went. The rules are in
`README.md`, Deployment: one build at a time, accepted after its soak, rolled back if it
fails.

## Now

| | |
|---|---|
| Build | `h0-20261113T121238Z-7d99`, H0 with Claude Code and Opus 5.5, spec 1.0; chosen by the code comparison, which it won in every pair |
| Running since | 15 November 2026, 17:59:09 UTC |
| Soak | from 15 November 2026, in progress |
| Accepted | not yet |
| Before it | `h2-20261111T232420Z-0559`, accepted 15 November 2026 after its soak; kept on the Pi as `/usr/local/bin/heatctl.0559`, to roll back to. `h0-20261104T165650Z-c300` remains `/usr/local/bin/heatctl.c300` |

## The house it runs in

| | |
|---|---|
| Controller host | a Raspberry Pi, running `heatctl` as a systemd service, logging to journald, forwarded by rsyslog |
| Broker | Mosquitto, on the Home Assistant host |
| Relays | two Shelly 1 Mini Gen3, firmware 2.0.3, one per zone; `auto_off` set by hand to 1800 s on each, the hard limit |
| Sensors | Zigbee through Zigbee2MQTT 2.14.3: two Shelly BLU H&T Display ZB (studio, bedroom), one Sonoff SNZB-02D (bathroom, since 8 November) |
| Zones | `day`: studio and bedroom; `bath`: bathroom |
| Targets | 19.0, the studio 21.5 on weekdays from 09:00 to 13:00 and 14:30 to 19:30 (21.0 until 12 November); the bedroom 17.0 from 09:00 to 21:00 every day (from 08:00 between 10 and 12 November); hysteresis 0.3 since 8 November |

## The soak of `c300`

The build did what the spec says throughout. Every problem in the soak was the house's.

| When (UTC) | What happened |
|---|---|
| 6 Nov 22:57 to 8 Nov 16:37 | The sensors went silent for hours at a time, so rooms lost their temperature and zones were held off (F4.AC4). Their Zigbee parent was a bulb switched off at the wall, Zigbee2MQTT re-sent cached temperatures with every message, and the sensors reported once an hour. Fixed in the house, as `README.md`, Zigbee sensors, describes |
| 8 Nov 16:36:56 | The day relay switched on by hand; `heatctl` switched it off in the same second, a mismatch it corrects. Stopped at 16:37:31 so the heating could run by hand, bounded by the relay's own timer; started again at 18:56:38. A choice, to heat a room the house had left blind, not an intervention the build needed |
| 8 Nov 17:01 to 21:08 | The house repaired: the sensors re-paired through an always-powered router, temperature reporting every 5 minutes at most, `cache_state` off, the bathroom's presence sensor replaced by a thermometer, hysteresis 0.3 |
| 8 Nov 21:08 to 10 Nov 18:04 | No restart, no mismatch, no stale studio or bedroom sensor. Seven heating runs of 17 to 32 minutes, each on at the target less 0.3 and off at the target plus 0.3; five from the bedroom, two from the studio's weekday schedule. Every schedule entry applied on time |
| 9 Nov 17:38 and 18:21 | The studio's sensor moved twice: where it was, it read warm |
| 9 Nov 20:30 to 10 Nov 16:58 | The bathroom's sensor did not keep its 5-minute reporting, set again on 10 November: twelve times its reading went stale, the bath zone held off for about 14 minutes each time. No heat was needed there |

## The soak of `0559`

No wrong decision, no restart the build caused, and no error event outside a deliberate test.

| When (UTC) | What happened |
|---|---|
| 12 Nov 14:43 | Deployed. Both relays reported off, `startup_grace` waited out, all three rooms read by 14:47; the unit runs it with one drop-in for its socket path, `HEATCTL_SOCKET=/run/heatctl/heatctl.sock` |
| 13 Nov, 14 Nov, 15 Nov | Five heating runs of 22 to 40 minutes, each on at the target less 0.3 and off at the target plus 0.3: two from the studio's weekday schedule at 21.5, three from the bedroom's 19.0 overnight and early in the morning, with the studio on the same relay following the bedroom (B5) |
| 13 Nov 22:41 to 22:45 | The broker stopped on purpose for about four minutes, no zone heating. `heatctl` kept running, logged the disconnect, tried to send each relay's state every 30 s and logged each failure (B8), and on reconnecting resubscribed and sent both relays their state within seconds (R2, F6.AC1) |
| 12 to 15 Nov | The bath relay dropped off the network for a second fifteen times. `heatctl` reported it `unknown`, held the zone off for that moment, and cleared it on the relay's own report; no heating was affected. `c300` sends the state again instead; both are within the spec |
| 15 Nov 09:55 | Restarted by hand to load a Sunday override for the studio, a schedule change, not a fault; in control two minutes later, after `startup_grace` |

No sensor went stale: the longest gap between readings was 10 minutes, inside `stale_after`.

## The soak of `7d99`

In progress from 15 November 2026, 17:59:09 UTC. The first control cycle at 18:00:57 UTC
found all three rooms, left both relays off and reported no zone degraded. The day's Sunday
afternoon override and its scheduled cleanup were removed before the soak started.

## Known in the house

- The bathroom's Sonoff SNZB-02D loses its temperature reporting setting each time it rejoins
  the network, and falls back to about one report an hour when the room is steady, so the bath
  zone is held off for about a quarter of an hour, roughly once an hour. Zigbee2MQTT still lists
  the setting and does not send it again. The setting held for 22.5 hours and for 5 hours, each
  time until a rejoin. The other two sensors rejoin as often and keep theirs.
  **Fixed 11 November 2026**: a Home Assistant automation sends the setting again a few seconds
  after each rejoin, while the sensor is still awake. Tested on a rejoin forced by taking the
  battery out: the setting accepted 4 seconds after it, then a steady room reported every 5
  minutes. Why the sensors rejoin, often several within minutes, is not known: the router they
  share, or the coordinator, rather than the bathroom's signal.
