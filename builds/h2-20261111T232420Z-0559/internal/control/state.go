package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/enrico/heatctl/internal/config"
	"github.com/enrico/heatctl/internal/logx"
	"github.com/enrico/heatctl/internal/mqtt"
)

// Reading is one valid sensor reading, already in the configured unit.
type Reading struct {
	Value float64
	At    time.Time
}

// SensorState is the runtime state of one sensor.
type SensorState struct {
	cfg   config.Sensor
	room  *RoomState
	last  *Reading
	stale bool
}

// RoomState is the runtime state of one room.
type RoomState struct {
	cfg         *config.Room
	zone        *ZoneState
	sensors     []*SensorState
	temperature *float64
	target      *float64
	demand      bool
}

// ZoneState is the runtime state of one zone.
type ZoneState struct {
	cfg     *config.Zone
	relay   *RelayState
	rooms   []*RoomState
	desired bool
}

// RelayState is the runtime state of one relay channel.
type RelayState struct {
	cfg  config.Relay
	zone *ZoneState

	reported *bool
	online   *bool
	desired  bool

	lastCommand      *bool
	lastCommandAt    time.Time
	lastSwitch       time.Time
	pendingAt        time.Time
	mismatchSince    time.Time
	lastConfirmAt    time.Time
	lastLeaseRefresh time.Time
}

// onMessage routes one inbound MQTT message. Unknown topics are ignored (I2).
func (c *Controller) onMessage(m mqtt.Message) {
	now := c.now()

	if r, ok := c.relayByStatusTopic[m.Topic]; ok {
		c.onRelayStatus(r, m, now)
		return
	}
	if r, ok := c.relayByOnlineTopic[m.Topic]; ok {
		c.onRelayOnline(r, m, now)
		return
	}
	if sensors, ok := c.sensorByTopic[m.Topic]; ok {
		c.onSensorMessage(sensors, m, now)
	}
}

func (c *Controller) onRelayStatus(r *RelayState, m mqtt.Message, now time.Time) {
	if m.Retained {
		// The age of a retained message is unknown; it is not a status this
		// run can trust (C7).
		c.log.Event("ignored_retained", logx.Fields{"topic": m.Topic, "relay": r.cfg.String()})
		return
	}
	output, err := parseOutput(m.Payload)
	if err != nil {
		c.logIgnoredPayload(m, r.cfg.String(), "", err)
		return
	}
	v := output
	if r.reported == nil || *r.reported != output {
		c.log.Event("relay_reported", logx.Fields{
			"relay": r.cfg.String(), "output": onOff(output), "zone": r.zone.cfg.ID,
		})
	}
	r.reported = &v
	if r.desired == output {
		r.mismatchSince = time.Time{}
	}
	c.publishSnapshot(now)
}

func (c *Controller) onRelayOnline(r *RelayState, m mqtt.Message, now time.Time) {
	if m.Retained {
		// A retained availability message may be stale, either way, until the
		// device reconnects (I2). heatctl waits for a fresh one.
		c.log.Event("ignored_retained", logx.Fields{"topic": m.Topic, "relay": r.cfg.String()})
		return
	}
	v, err := parseOnline(m.Payload)
	if err != nil {
		c.logIgnoredPayload(m, r.cfg.String(), "", err)
		return
	}
	if r.online == nil || *r.online != v {
		c.log.Event("relay_availability", logx.Fields{
			"relay": r.cfg.String(), "online": v, "zone": r.zone.cfg.ID,
		})
	}
	r.online = &v
	if !v {
		// A relay that was reported offline makes its zone degraded (B8); its
		// reported output is no longer meaningful.
		r.reported = nil
	}
	c.publishSnapshot(now)
}

func (c *Controller) onSensorMessage(sensors []*SensorState, m mqtt.Message, now time.Time) {
	accepted := false
	for _, s := range sensors {
		if m.Retained {
			// A retained sensor message delivered on subscribe is not a
			// reading: its age is unknown, so it would be state from before
			// the start (B1, F6.AC7).
			c.log.Event("ignored_retained", logx.Fields{
				"topic": m.Topic, "sensor": s.cfg.ID(), "room": s.room.cfg.ID,
			})
			continue
		}
		celsius, err := parseReading(s.cfg.Kind, m.Payload)
		if err != nil {
			c.logIgnoredPayload(m, "", s.cfg.ID(), err)
			continue
		}
		// R3: readings outside -40..85 °C are ignored, checked before any
		// conversion.
		if celsius < -40 || celsius > 85 {
			c.log.Event("ignored_reading", logx.Fields{
				"topic": m.Topic, "sensor": s.cfg.ID(), "room": s.room.cfg.ID,
				"value_c": celsius, "reason": "out of range (-40 to 85 °C)",
			})
			continue
		}
		value := celsius
		if c.cfg.Unit == config.Fahrenheit {
			value = celsius*9/5 + 32
		}
		s.last = &Reading{Value: value, At: now}
		s.stale = false
		accepted = true
		c.log.Event("sensor_reading", logx.Fields{
			"topic": m.Topic, "sensor": s.cfg.ID(), "room": s.room.cfg.ID,
			"value": value, "unit": string(c.cfg.Unit),
		})
	}
	if !accepted {
		// A malformed payload, a fault, a retained message or an out-of-range
		// reading never changes the room's temperature and does not run a
		// control cycle (F2.AC2, R3).
		return
	}
	c.publishSnapshot(now)
	if !c.started {
		c.maybeStart(now)
		return
	}
	c.evaluate(now)
}

func (c *Controller) logIgnoredPayload(m mqtt.Message, relay, sensor string, err error) {
	fields := logx.Fields{
		"topic":  m.Topic,
		"reason": err.Error(),
	}
	if relay != "" {
		fields["relay"] = relay
	}
	if sensor != "" {
		fields["sensor"] = sensor
	}
	c.log.Event("ignored_payload", fields)
}

// evaluate is one control cycle: it refreshes freshness, room temperatures,
// targets and demands, decides every zone, switches the relays, writes the L1
// event and updates the status snapshot.
func (c *Controller) evaluate(now time.Time) {
	ct := c.cfg.Control

	for _, s := range c.sensors {
		if s.last == nil {
			continue
		}
		stale := now.Sub(s.last.At) > ct.StaleAfter
		switch {
		case stale && !s.stale:
			s.stale = true
			c.log.Event("sensor_stale", logx.Fields{
				"topic":  s.cfg.Topic(c.cfg.MQTT.Zigbee2MQTTTopic),
				"sensor": s.cfg.ID(), "room": s.room.cfg.ID,
				"age_s":       int(now.Sub(s.last.At).Seconds()),
				"stale_after": ct.StaleAfter.String(),
			})
		case !stale && s.stale:
			s.stale = false
			c.log.Event("sensor_fresh", logx.Fields{"sensor": s.cfg.ID(), "room": s.room.cfg.ID})
		}
	}

	for _, room := range c.rooms {
		room.temperature = c.roomTemperature(room, now)
		if t, ok := room.cfg.TargetAt(now, c.loc); ok {
			target := t
			room.target = &target
		} else {
			room.target = nil
		}
		room.demand = c.nextDemand(room, room.temperature, room.target)
	}

	for _, z := range c.zones {
		z.desired = false
		for _, room := range z.rooms {
			if room.temperature != nil && room.demand {
				z.desired = true
				break
			}
		}
	}

	for _, r := range c.relays {
		c.applyRelay(r, now)
	}

	c.cycleSeq++
	c.logControlCycle(now, ct)
	c.publishSnapshot(now)
}

// nextDemand applies the hysteresis of B4.
func (c *Controller) nextDemand(room *RoomState, temperature, target *float64) bool {
	if temperature == nil || target == nil {
		// A room with no temperature does not vote, and a room with neither
		// target nor schedule never demands heat.
		return false
	}
	h := c.cfg.Control.Hysteresis
	if *temperature <= *target-h {
		return true
	}
	if *temperature >= *target+h {
		return false
	}
	return room.demand
}

func (c *Controller) zoneDegraded(z *ZoneState, now time.Time) bool {
	anyTemperature := false
	for _, room := range z.rooms {
		if c.roomTemperature(room, now) != nil {
			anyTemperature = true
			break
		}
	}
	if !anyTemperature {
		return true
	}
	if z.relay.online != nil && !*z.relay.online {
		return true
	}
	return false
}

// applyRelay drives one relay towards its zone's desired state, honouring
// min_switch_interval (B7), confirmation (B8) and the relay lease (B9).
func (c *Controller) applyRelay(r *RelayState, now time.Time) {
	ct := c.cfg.Control
	desired := r.zone.desired
	r.desired = desired

	if !r.pendingAt.IsZero() && !now.Before(r.pendingAt) {
		r.pendingAt = time.Time{}
	}

	changed := r.lastCommand == nil || *r.lastCommand != desired
	if changed {
		if !r.lastSwitch.IsZero() && now.Sub(r.lastSwitch) < ct.MinSwitchInterval {
			if r.pendingAt.IsZero() {
				r.pendingAt = r.lastSwitch.Add(ct.MinSwitchInterval)
				c.log.Event("relay_switch_pending", logx.Fields{
					"relay": r.cfg.String(), "zone": r.zone.cfg.ID, "desired": onOff(desired),
					"reason": "min_switch_interval", "not_before": r.pendingAt.UTC().Format(time.RFC3339),
				})
			}
			return
		}
		reason := "desired_change"
		if r.lastCommand == nil {
			reason = "startup"
		}
		c.commandRelay(r, desired, reason, now)
		return
	}

	// The last command already matches the desired state. Watch the reported
	// state and keep the lease alive. A state the relay has not reported at
	// all is not confirmation either (B8).
	confirmed := r.reported != nil && *r.reported == desired
	if confirmed {
		r.mismatchSince = time.Time{}
	} else {
		due := r.mismatchSince.Add(ct.ConfirmTimeout)
		if r.lastConfirmAt.After(r.mismatchSince) {
			due = r.lastConfirmAt.Add(ct.ConfirmTimeout)
		}
		if r.mismatchSince.IsZero() {
			r.mismatchSince = now
			due = now.Add(ct.ConfirmTimeout)
		}
		if !now.Before(due) {
			reported := "unknown"
			if r.reported != nil {
				reported = onOff(*r.reported)
			}
			c.log.Event("relay_mismatch", logx.Fields{
				"relay": r.cfg.String(), "zone": r.zone.cfg.ID,
				"desired": onOff(desired), "reported": reported,
				"confirm_timeout": ct.ConfirmTimeout.String(),
			})
			c.commandRelay(r, desired, "confirm", now)
			return
		}
	}

	if desired && !r.lastLeaseRefresh.IsZero() && now.Sub(r.lastLeaseRefresh) >= c.refreshInterval() {
		c.commandRelay(r, desired, "lease_refresh", now)
	}
}

// commandRelay publishes on,<lease> or off. A state change also restarts the
// min_switch_interval; a confirmation or lease refresh does not (B7, B8).
func (c *Controller) commandRelay(r *RelayState, on bool, reason string, now time.Time) {
	payload := "off"
	if on {
		payload = fmt.Sprintf("on,%d", c.leaseSeconds())
	}
	topic := r.cfg.CommandTopic()
	if err := c.client.Publish(topic, []byte(payload), mqtt.QoSCommand); err != nil {
		c.log.Event("relay_command_failed", logx.Fields{
			"relay": r.cfg.String(), "zone": r.zone.cfg.ID,
			"command": payload, "reason": reason, "error": err.Error(),
		})
		// The deadline that brought us here is in the past now. Move the
		// matching one forward, or a broker that is away would be retried in a
		// tight loop for as long as it is gone.
		switch reason {
		case "confirm":
			r.mismatchSince = now
			r.lastConfirmAt = now
		case "lease_refresh":
			r.lastLeaseRefresh = now
		}
		return
	}

	v := on
	switched := r.lastCommand == nil || *r.lastCommand != on
	r.lastCommand = &v
	r.lastCommandAt = now
	if switched {
		r.lastSwitch = now
		r.pendingAt = time.Time{}
	}
	switch reason {
	case "confirm":
		r.lastConfirmAt = now
	case "lease_refresh":
	default:
		r.lastConfirmAt = time.Time{}
	}
	if on {
		r.lastLeaseRefresh = now
	} else {
		r.lastLeaseRefresh = time.Time{}
	}
	if r.reported != nil && *r.reported == on {
		r.mismatchSince = time.Time{}
	} else {
		r.mismatchSince = now
	}
	c.log.Event("relay_command", logx.Fields{
		"relay": r.cfg.String(), "device": r.cfg.Device, "switch": r.cfg.Switch,
		"zone": r.zone.cfg.ID, "command": payload, "reason": reason,
		"desired": onOff(on),
	})
}

// resendDesired re-sends a relay's desired state after a reconnect (R2). It is
// not a switch, so it does not restart min_switch_interval.
func (c *Controller) resendDesired(r *RelayState, now time.Time) {
	desired := r.zone.desired
	payload := "off"
	if desired {
		payload = fmt.Sprintf("on,%d", c.leaseSeconds())
	}
	if err := c.client.Publish(r.cfg.CommandTopic(), []byte(payload), mqtt.QoSCommand); err != nil {
		c.log.Event("relay_command_failed", logx.Fields{
			"relay": r.cfg.String(), "zone": r.zone.cfg.ID,
			"command": payload, "reason": "reconnect", "error": err.Error(),
		})
		return
	}
	v := desired
	r.desired = desired
	r.lastCommand = &v
	r.lastCommandAt = now
	r.pendingAt = time.Time{}
	r.mismatchSince = time.Time{}
	if desired {
		r.lastLeaseRefresh = now
	} else {
		r.lastLeaseRefresh = time.Time{}
	}
	c.log.Event("relay_command", logx.Fields{
		"relay": r.cfg.String(), "device": r.cfg.Device, "switch": r.cfg.Switch,
		"zone": r.zone.cfg.ID, "command": payload, "reason": "reconnect",
		"desired": onOff(desired),
	})
}

func (c *Controller) requestStatus(r *RelayState) {
	if err := c.client.Publish(r.cfg.CommandTopic(), []byte("status_update"), mqtt.QoSCommand); err != nil {
		c.log.Event("relay_status_request_failed", logx.Fields{
			"relay": r.cfg.String(), "error": err.Error(),
		})
		return
	}
	c.log.Event("relay_status_request", logx.Fields{"relay": r.cfg.String(), "zone": r.zone.cfg.ID})
}

func (c *Controller) logControlCycle(now time.Time, ct config.Control) {
	ev := CycleEvent{
		Event:  "control_cycle",
		Schema: 1,
		TS:     now.UTC().Format(time.RFC3339),
		Unit:   string(c.cfg.Unit),
		Seq:    c.cycleSeq,
		Rooms:  make([]CycleRoom, 0, len(c.rooms)),
		Zones:  make([]CycleZone, 0, len(c.zones)),
	}
	for _, room := range c.rooms {
		cr := CycleRoom{
			ID:       room.cfg.ID,
			Zone:     room.cfg.ZoneID,
			Readings: []float64{},
			Demand:   room.demand,
		}
		if room.temperature != nil {
			t := *room.temperature
			cr.Temperature = &t
		}
		if room.target != nil {
			t := *room.target
			cr.Target = &t
		}
		for _, s := range room.sensors {
			if s.last == nil || now.Sub(s.last.At) > ct.StaleAfter {
				continue
			}
			cr.Readings = append(cr.Readings, s.last.Value)
		}
		ev.Rooms = append(ev.Rooms, cr)
	}
	for _, z := range c.zones {
		ev.Zones = append(ev.Zones, CycleZone{
			ID:       z.cfg.ID,
			Relay:    z.cfg.Relay.String(),
			Desired:  onOff(z.desired),
			Reported: reportedString(z.relay.reported),
			Degraded: c.zoneDegraded(z, now),
		})
	}
	c.log.Event(ev.Event, cycleFields(ev))
}

func cycleFields(ev CycleEvent) logx.Fields {
	return logx.Fields{
		"event":  ev.Event,
		"schema": ev.Schema,
		"ts":     ev.TS,
		"unit":   ev.Unit,
		"seq":    ev.Seq,
		"rooms":  ev.Rooms,
		"zones":  ev.Zones,
	}
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func reportedString(v *bool) string {
	if v == nil {
		return "unknown"
	}
	return onOff(*v)
}

// parseOutput reads the "output" field of a relay status message.
func parseOutput(payload []byte) (bool, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(payload, &obj); err != nil {
		return false, fmt.Errorf("invalid JSON: %v", err)
	}
	raw, ok := obj["output"]
	if !ok {
		return false, fmt.Errorf("missing output field")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, fmt.Errorf("output is null")
	}
	var out bool
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, fmt.Errorf("output is not a boolean")
	}
	return out, nil
}

// parseOnline reads a Shelly availability payload.
func parseOnline(payload []byte) (bool, error) {
	switch string(bytes.TrimSpace(payload)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, fmt.Errorf("availability must be true or false")
}

type shellyReading struct {
	TC     *float64        `json:"tC"`
	Errors json.RawMessage `json:"errors"`
}

type zigbeeReading struct {
	Temperature *float64 `json:"temperature"`
}

// parseReading extracts a temperature in °C from a sensor payload.
func parseReading(kind config.SensorKind, payload []byte) (float64, error) {
	if kind == config.ZigbeeSensor {
		var r zigbeeReading
		if err := json.Unmarshal(payload, &r); err != nil {
			return 0, fmt.Errorf("invalid JSON: %v", err)
		}
		if r.Temperature == nil {
			return 0, fmt.Errorf("no numeric temperature field")
		}
		return *r.Temperature, nil
	}
	var r shellyReading
	if err := json.Unmarshal(payload, &r); err != nil {
		return 0, fmt.Errorf("invalid JSON: %v", err)
	}
	if r.Errors != nil {
		return 0, fmt.Errorf("sensor reports errors")
	}
	if r.TC == nil {
		return 0, fmt.Errorf("no numeric tC field")
	}
	return *r.TC, nil
}
