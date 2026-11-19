package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// build validates the decoded values and produces the configuration. It
// reports every problem it finds rather than stopping at the first.
func (d *decoder) build() *Config {
	c := &Config{Path: d.path}
	r := &d.root

	// unit
	if !r.unitSet {
		d.addPath("unit", "is required (C or F)")
	} else {
		switch Unit(r.unit) {
		case Celsius, Fahrenheit:
			c.Unit = Unit(r.unit)
		default:
			d.addPath("unit", fmt.Sprintf("must be \"C\" or \"F\", got %q", r.unit))
			c.Unit = Celsius
		}
	}

	d.buildMQTT(c)
	d.buildControl(c)
	c.Zones = d.buildZones(c.Unit)
	return c
}

func (d *decoder) buildMQTT(c *Config) {
	m := &d.root.mqtt
	c.MQTT.ClientID = "heatctl"
	c.MQTT.Zigbee2MQTTTopic = "zigbee2mqtt"

	switch {
	case !m.brokerSet:
		d.addPath("mqtt.broker", "is required")
	case strings.TrimSpace(m.broker) == "":
		d.addPath("mqtt.broker", "must not be empty")
	default:
		c.MQTT.Broker = m.broker
		validateBroker(d, m.broker)
	}

	if m.clientIDSet {
		c.MQTT.ClientID = m.clientID
		if strings.TrimSpace(c.MQTT.ClientID) == "" {
			d.addPath("mqtt.client_id", "must not be empty")
		}
	}
	c.MQTT.Username = m.username
	c.MQTT.Password = m.password

	if m.zigbeeSet {
		c.MQTT.Zigbee2MQTTTopic = m.zigbee
		switch {
		case strings.TrimSpace(c.MQTT.Zigbee2MQTTTopic) == "":
			d.addPath("mqtt.zigbee2mqtt_topic", "must not be empty")
		case strings.ContainsAny(c.MQTT.Zigbee2MQTTTopic, "+#"):
			d.addPath("mqtt.zigbee2mqtt_topic", "must not contain the MQTT wildcards + or #")
		}
	}
}

func validateBroker(d *decoder, broker string) {
	if !strings.Contains(broker, "://") {
		d.addPath("mqtt.broker", fmt.Sprintf("must include a scheme, for example tcp://192.168.1.10:1883 (got %q)", broker))
		return
	}
	u, err := url.Parse(broker)
	if err != nil {
		d.addPath("mqtt.broker", fmt.Sprintf("is not a valid URL: %v", err))
		return
	}
	switch u.Scheme {
	case "tcp", "ssl", "tls", "ws", "wss":
	case "":
		d.addPath("mqtt.broker", fmt.Sprintf("must include a scheme, for example tcp://192.168.1.10:1883 (got %q)", broker))
		return
	default:
		d.addPath("mqtt.broker", fmt.Sprintf("unsupported scheme %q (use tcp, ssl, tls, ws or wss)", u.Scheme))
		return
	}
	if u.Host == "" {
		d.addPath("mqtt.broker", fmt.Sprintf("must include a host, got %q", broker))
	}
}

func (d *decoder) buildControl(c *Config) {
	ctl := Control{
		CycleInterval:     DefaultCycleInterval,
		Hysteresis:        DefaultHysteresis(c.Unit),
		StaleAfter:        DefaultStaleAfter,
		MinSwitchInterval: DefaultMinSwitchInterval,
		RelayLease:        DefaultRelayLease,
		StartupGrace:      DefaultStartupGrace,
		ConfirmTimeout:    DefaultConfirmTimeout,
	}
	rc := &d.root.control
	if rc.cycle != nil {
		ctl.CycleInterval = *rc.cycle
	}
	if rc.stale != nil {
		ctl.StaleAfter = *rc.stale
	}
	if rc.minSwitch != nil {
		ctl.MinSwitchInterval = *rc.minSwitch
	}
	if rc.lease != nil {
		ctl.RelayLease = *rc.lease
	}
	if rc.grace != nil {
		ctl.StartupGrace = *rc.grace
	}
	if rc.confirm != nil {
		ctl.ConfirmTimeout = *rc.confirm
	}
	if rc.hysteresis != nil {
		ctl.Hysteresis = *rc.hysteresis
	}

	for _, v := range []struct {
		key string
		val time.Duration
	}{
		{"cycle_interval", ctl.CycleInterval},
		{"stale_after", ctl.StaleAfter},
		{"min_switch_interval", ctl.MinSwitchInterval},
		{"relay_lease", ctl.RelayLease},
		{"startup_grace", ctl.StartupGrace},
		{"confirm_timeout", ctl.ConfirmTimeout},
	} {
		if v.val <= 0 {
			d.addPath("control."+v.key, fmt.Sprintf("must be greater than zero, got %s", v.val))
		}
	}
	if ctl.Hysteresis < 0 {
		d.addPath("control.hysteresis", fmt.Sprintf("must be 0 or more, got %g", ctl.Hysteresis))
	}
	if !(ctl.RelayLease > ctl.CycleInterval) {
		d.addPath("control.relay_lease", fmt.Sprintf("must be longer than control.cycle_interval (%s is not longer than %s)", ctl.RelayLease, ctl.CycleInterval))
	}
	c.Control = ctl
}

func (d *decoder) buildZones(unit Unit) []Zone {
	r := &d.root
	if !r.zonesSeen {
		d.addPath("zones", "is required (at least one zone)")
		return nil
	}
	if len(r.zones) == 0 {
		if r.zonesShapeOK {
			d.addPath("zones", "at least one zone is required")
		}
		return nil
	}

	minTarget, maxTarget := TargetLimits(unit)
	allDays := Monday | Tuesday | Wednesday | Thursday | Friday | Saturday | Sunday

	zoneIDs := make(map[string]int, len(r.zones))
	relayOwners := make(map[string]string, len(r.zones))
	roomIDs := make(map[string]string)
	sensorOwners := make(map[string]string)

	zones := make([]Zone, 0, len(r.zones))
	for zi := range r.zones {
		rz := &r.zones[zi]
		zb := fmt.Sprintf("zones[%d]", zi)
		zone := Zone{ID: rz.id, Relay: Relay{Device: rz.relay.device, Switch: rz.relay.sw}}

		switch {
		case !rz.idSet || rz.id == "":
			d.addPath(zb+".id", "is required")
		default:
			if first, ok := zoneIDs[rz.id]; ok {
				d.addPath(zb+".id", fmt.Sprintf("duplicate zone id %q, already used by zones[%d] (line %d)", rz.id, first, d.lines[fmt.Sprintf("zones[%d].id", first)]))
			} else {
				zoneIDs[rz.id] = zi
			}
		}

		relayOK := true
		switch {
		case !rz.relay.deviceSet || rz.relay.device == "":
			d.addPath(zb+".relay.device", "is required")
			relayOK = false
		}
		switch {
		case !rz.relay.swSet:
			d.addPath(zb+".relay.switch", "is required")
			relayOK = false
		case rz.relay.sw < 0:
			d.addPath(zb+".relay.switch", fmt.Sprintf("must be 0 or more, got %d", rz.relay.sw))
			relayOK = false
		}
		if relayOK {
			id := zone.Relay.ID()
			if owner, ok := relayOwners[id]; ok {
				d.addPath(zb+".relay", fmt.Sprintf("relay %s is already used by %s", zone.Relay.String(), owner))
			} else {
				relayOwners[id] = zb
			}
		}

		if !rz.roomsSet || (rz.roomsOK && len(rz.rooms) == 0) {
			d.addPath(zb+".rooms", "one or more rooms are required")
		}
		for ri := range rz.rooms {
			rr := &rz.rooms[ri]
			rb := fmt.Sprintf("%s.rooms[%d]", zb, ri)
			room := Room{ZoneID: zone.ID}

			switch {
			case !rr.idSet || rr.id == "":
				d.addPath(rb+".id", "is required")
			default:
				room.ID = rr.id
				if first, ok := roomIDs[rr.id]; ok {
					d.addPath(rb+".id", fmt.Sprintf("duplicate room id %q, already used by %s (line %d)", rr.id, first, d.lines[first+".id"]))
				} else {
					roomIDs[rr.id] = rb
				}
			}

			if rr.target != nil {
				v := *rr.target
				if v < minTarget || v > maxTarget {
					d.addPath(rb+".target", fmt.Sprintf("must be between %g and %g %s, got %g", minTarget, maxTarget, unit.Symbol(), v))
				} else {
					target := v
					room.Target = &target
				}
			}

			if rr.scheduleSet {
				if rr.scheduleOK && len(rr.schedule) == 0 {
					d.addPath(rb+".schedule", "must have at least one entry")
				}
				entries := make([]ScheduleEntry, 0, len(rr.schedule))
				valid := make([]bool, 0, len(rr.schedule))
				for si := range rr.schedule {
					re := &rr.schedule[si]
					sb := fmt.Sprintf("%s.schedule[%d]", rb, si)
					okEntry := true
					if !re.fromSet {
						d.addPath(sb+".from", "is required")
						okEntry = false
					}
					if re.target == nil {
						d.addPath(sb+".target", "is required")
						okEntry = false
					} else if *re.target < minTarget || *re.target > maxTarget {
						d.addPath(sb+".target", fmt.Sprintf("must be between %g and %g %s, got %g", minTarget, maxTarget, unit.Symbol(), *re.target))
						okEntry = false
					}
					entry := ScheduleEntry{From: re.from, Days: re.days}
					if re.target != nil {
						entry.Target = *re.target
					}
					entries = append(entries, entry)
					valid = append(valid, okEntry && re.target != nil)
				}

				// No two entries may apply at the same time on the same day.
				for a := 0; a < len(entries); a++ {
					for b := a + 1; b < len(entries); b++ {
						if !valid[a] || !valid[b] || entries[a].From != entries[b].From {
							continue
						}
						daysA, daysB := entries[a].Days, entries[b].Days
						if daysA == 0 {
							daysA = allDays
						}
						if daysB == 0 {
							daysB = allDays
						}
						if overlap := daysA & daysB; overlap != 0 {
							d.addPath(fmt.Sprintf("%s.schedule[%d].from", rb, b),
								fmt.Sprintf("%s is already taken by %s.schedule[%d] on %s; two entries cannot apply at the same time on the same day",
									entries[b].FromString(), rb, a, strings.Join(DaySet(overlap).Names(), ", ")))
						}
					}
				}
				room.Schedule = entries
			}

			hasSchedule := rr.scheduleSet
			switch {
			case room.Target != nil && hasSchedule:
				d.addPath(rb, "target and schedule cannot both be set")
			case len(rr.sensors) > 0 && rr.target == nil && !hasSchedule:
				d.addPath(rb, "a room with sensors needs either target or schedule")
			}

			for si := range rr.sensors {
				rs := &rr.sensors[si]
				sb := fmt.Sprintf("%s.sensors[%d]", rb, si)
				hasShelly := rs.deviceSet || rs.componentSet
				hasZigbee := rs.zigbeeSet
				switch {
				case hasShelly && hasZigbee:
					d.addPath(sb, "a sensor is either {device, component} or {zigbee}, not both")
				case hasZigbee:
					if rs.zigbee == "" {
						d.addPath(sb+".zigbee", "must not be empty")
						continue
					}
					sensor := Sensor{Kind: ZigbeeSensor, Zigbee: rs.zigbee}
					d.addSensor(&room, sensor, sensorOwners, sb)
				case hasShelly:
					okSensor := true
					if !rs.deviceSet || rs.device == "" {
						d.addPath(sb+".device", "is required")
						okSensor = false
					}
					if !rs.componentSet {
						d.addPath(sb+".component", "is required")
						okSensor = false
					} else if rs.component < 0 {
						d.addPath(sb+".component", fmt.Sprintf("must be 0 or more, got %d", rs.component))
						okSensor = false
					}
					if okSensor {
						sensor := Sensor{Kind: ShellySensor, Device: rs.device, Component: rs.component}
						d.addSensor(&room, sensor, sensorOwners, sb)
					}
				default:
					d.addPath(sb, "a sensor needs either {device, component} or {zigbee}")
				}
			}

			zone.Rooms = append(zone.Rooms, room)
		}
		zones = append(zones, zone)
	}
	return zones
}

func (d *decoder) addSensor(room *Room, sensor Sensor, owners map[string]string, path string) {
	id := sensor.ID()
	if owner, ok := owners[id]; ok {
		d.addPath(path, fmt.Sprintf("sensor %s is already used by %s (line %d)", sensor.String(), owner, d.lines[owner]))
		return
	}
	owners[id] = path
	room.Sensors = append(room.Sensors, sensor)
}
