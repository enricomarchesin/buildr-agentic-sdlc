package control

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// HandleMessage processes one MQTT message. retained is the message's retain
// flag as delivered by the broker.
func (c *Controller) HandleMessage(now time.Time, topic string, payload []byte, retained bool) {
	if s, ok := c.sensors[topic]; ok {
		c.handleReading(now, s, payload, retained)
		return
	}
	if r, ok := c.byStatus[topic]; ok {
		c.handleRelayStatus(now, r, topic, payload, retained)
		return
	}
	if rs, ok := c.byOnline[topic]; ok {
		c.handleOnline(now, rs, topic, payload)
		return
	}
	c.log.Event("topic_ignored", "topic", topic)
}

func (c *Controller) ignored(kind, topic string, payload []byte, why string) {
	p := string(payload)
	if len(p) > 512 {
		p = p[:512] + "..."
	}
	c.log.Event(kind, "topic", topic, "payload", p, "reason", why)
}

func (c *Controller) handleReading(now time.Time, s *sensor, payload []byte, retained bool) {
	if retained {
		// A retained message has an unknown age: it is state from before
		// the start, not a reading (B1, C7).
		c.ignored("reading_ignored", s.topic, payload, "retained message")
		return
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(payload, &obj); err != nil || obj == nil {
		c.ignored("payload_invalid", s.topic, payload, "not a JSON object")
		return
	}
	field := "tC"
	if s.cfg.IsZigbee() {
		field = "temperature"
	}
	raw, present := obj[field]
	if !present {
		if s.cfg.IsZigbee() {
			// Zigbee2MQTT also publishes updates without a temperature,
			// such as a battery report. They are not readings.
			return
		}
		c.ignored("reading_invalid", s.topic, payload, "no tC field")
		return
	}
	if !s.cfg.IsZigbee() {
		if _, hasErrors := obj["errors"]; hasErrors {
			c.ignored("reading_invalid", s.topic, payload, "sensor reports errors")
			return
		}
	}
	var celsius float64
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &celsius) != nil {
		c.ignored("reading_invalid", s.topic, payload, field+" is not a number")
		return
	}
	if celsius < minCelsius || celsius > maxCelsius {
		c.ignored("reading_out_of_range", s.topic, payload, "outside -40 to 85 °C")
		return
	}
	s.value = c.cfg.Unit.FromCelsius(celsius)
	s.at = now
	if s.staleLogged || !s.has {
		c.log.Event("sensor_fresh", "room", s.room.cfg.ID, "sensor", s.cfg.String(), "reading", round(s.value))
	}
	s.has = true
	s.staleLogged = false
	if !c.started {
		c.maybeStart(now)
		return
	}
	c.cycle(now) // B6: a cycle on every valid reading
}

func (c *Controller) handleRelayStatus(now time.Time, r *relay, topic string, payload []byte, retained bool) {
	if retained {
		c.ignored("status_ignored", topic, payload, "retained message")
		return
	}
	var st struct {
		Output *bool `json:"output"`
	}
	if err := json.Unmarshal(payload, &st); err != nil {
		c.ignored("payload_invalid", topic, payload, "not a JSON object")
		return
	}
	if st.Output == nil {
		c.ignored("payload_invalid", topic, payload, "no boolean output field")
		return
	}
	prev := r.reported
	r.reported = triOf(*st.Output)
	if prev != r.reported {
		c.log.Event("relay_reported", "zone", r.zone.cfg.ID, "relay", r.cfg.String(),
			"reported", onOff(r.reported), "previous", onOff(prev))
	}
	c.apply(now)
}

func (c *Controller) handleOnline(now time.Time, rs []*relay, topic string, payload []byte) {
	var online tri
	switch strings.TrimSpace(string(payload)) {
	case "true":
		online = yes
	case "false":
		online = no
	default:
		c.ignored("payload_invalid", topic, payload, "expected true or false")
		return
	}
	for _, r := range rs {
		prev := r.online
		r.online = online
		if prev == online {
			continue
		}
		c.log.Event("relay_availability", "zone", r.zone.cfg.ID, "relay", r.cfg.String(),
			"online", onlineString(online), "previous", onlineString(prev))
		if online == yes && prev == no && r.commandedSet {
			// Commands sent while the relay was away are lost.
			r.resend = true
		}
		if online == yes {
			c.requestStatus(r)
		}
	}
	c.apply(now)
}
