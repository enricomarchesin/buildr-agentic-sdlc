// Package control holds heatctl's decision logic. A Controller is not safe
// for concurrent use: one goroutine owns it and feeds it events.
package control

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"heatctl/internal/config"
)

type Publisher interface {
	// Publish sends a non-retained message; it must not block.
	Publish(topic, payload string)
}

type Logger interface {
	Emit(v any)
	Log(event string, kv ...any)
}

const eps = 1e-9

type sensor struct {
	cfg      config.Sensor
	room     *room
	topic    string
	value    float64 // in configured unit
	at       time.Time
	valid    bool
	wasFresh bool
}

type room struct {
	cfg     config.Room
	zone    *zone
	sensors []*sensor
	demand  bool
	// last evaluated values, for status
	temp   *float64
	target *float64
}

type zone struct {
	cfg                   config.Zone
	rooms                 []*room
	cmdTopic, statusTopic string

	desired    bool
	hasDesired bool
	degraded   bool

	commanded  bool // a command has been sent since start
	applied    bool // state last commanded
	lastSwitch time.Time
	lastCmd    time.Time

	reported *bool
	online   *bool
}

type Controller struct {
	cfg   *config.Config
	pub   Publisher
	log   Logger
	zones []*zone
	rooms []*room

	sensorTopics map[string]*sensor
	statusTopics map[string]*zone
	onlineTopics map[string][]*zone

	start     time.Time
	started   bool
	lastCycle time.Time
}

func New(cfg *config.Config, pub Publisher, log Logger, now time.Time) *Controller {
	c := &Controller{
		cfg: cfg, pub: pub, log: log, start: now,
		sensorTopics: map[string]*sensor{},
		statusTopics: map[string]*zone{},
		onlineTopics: map[string][]*zone{},
	}
	for _, zc := range cfg.Zones {
		z := &zone{cfg: zc}
		z.cmdTopic = fmt.Sprintf("%s/command/switch:%d", zc.Relay.Device, zc.Relay.Switch)
		z.statusTopic = fmt.Sprintf("%s/status/switch:%d", zc.Relay.Device, zc.Relay.Switch)
		c.statusTopics[z.statusTopic] = z
		c.onlineTopics[zc.Relay.Device+"/online"] = append(c.onlineTopics[zc.Relay.Device+"/online"], z)
		for _, rc := range zc.Rooms {
			r := &room{cfg: rc, zone: z}
			for _, sc := range rc.Sensors {
				s := &sensor{cfg: sc, room: r}
				if sc.IsZigbee() {
					s.topic = cfg.MQTT.Zigbee2MQTTTopic + "/" + sc.Zigbee
				} else {
					s.topic = fmt.Sprintf("%s/status/temperature:%d", sc.Device, sc.Component)
				}
				c.sensorTopics[s.topic] = s
				r.sensors = append(r.sensors, s)
			}
			z.rooms = append(z.rooms, r)
			c.rooms = append(c.rooms, r)
		}
		c.zones = append(c.zones, z)
	}
	return c
}

// Topics lists every topic to subscribe to.
func (c *Controller) Topics() []string {
	var t []string
	for k := range c.sensorTopics {
		t = append(t, k)
	}
	for k := range c.statusTopics {
		t = append(t, k)
	}
	for k := range c.onlineTopics {
		t = append(t, k)
	}
	return t
}

// RequestStatus asks every relay to publish its status.
func (c *Controller) RequestStatus() {
	for _, z := range c.zones {
		c.pub.Publish(z.cmdTopic, "status_update")
	}
}

// Connected is called after every (re)connection and resubscription.
func (c *Controller) Connected(now time.Time) {
	c.RequestStatus()
	if c.started {
		for _, z := range c.zones {
			c.resend(z, now, "reconnect")
		}
	}
}

func (c *Controller) ignored(topic string, payload []byte, reason string) {
	p := string(payload)
	if len(p) > 200 {
		p = p[:200]
	}
	c.log.Log("payload_ignored", "topic", topic, "reason", reason, "payload", p)
}

// HandleMessage processes one incoming MQTT message.
func (c *Controller) HandleMessage(now time.Time, topic string, payload []byte, retained bool) {
	if s, ok := c.sensorTopics[topic]; ok {
		c.handleReading(now, s, topic, payload, retained)
		return
	}
	if z, ok := c.statusTopics[topic]; ok {
		var m map[string]json.RawMessage
		var out bool
		if json.Unmarshal(payload, &m) != nil || json.Unmarshal(m["output"], &out) != nil {
			c.ignored(topic, payload, "no boolean output field")
			return
		}
		z.reported = &out
		return
	}
	if zs, ok := c.onlineTopics[topic]; ok {
		var on bool
		switch strings.TrimSpace(string(payload)) {
		case "true":
			on = true
		case "false":
		default:
			c.ignored(topic, payload, "online payload is not true or false")
			return
		}
		for _, z := range zs {
			was := z.online
			z.online = &on
			if was == nil || *was != on {
				c.log.Log("relay_online", "zone", z.cfg.ID, "online", on)
			}
			if on && was != nil && !*was {
				z.reported = nil
				c.pub.Publish(z.cmdTopic, "status_update")
				if c.started {
					c.resend(z, now, "relay_online")
				}
			}
		}
		return
	}
	c.log.Log("topic_ignored", "topic", topic)
}

func (c *Controller) handleReading(now time.Time, s *sensor, topic string, payload []byte, retained bool) {
	if retained {
		return // age unknown: state from before the start (C7)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(payload, &m); err != nil || m == nil {
		c.ignored(topic, payload, "malformed JSON")
		return
	}
	field := "temperature"
	if !s.cfg.IsZigbee() {
		field = "tC"
		if e, ok := m["errors"]; ok && string(e) != "null" {
			c.ignored(topic, payload, "device reports errors")
			return
		}
	}
	var v *float64
	if err := json.Unmarshal(m[field], &v); err != nil || v == nil {
		c.ignored(topic, payload, field+" is not a number")
		return
	}
	if *v < -40 || *v > 85 || math.IsNaN(*v) {
		c.ignored(topic, payload, "reading outside -40 to 85 C")
		return
	}
	val := *v
	if c.cfg.Unit == "F" {
		val = val*9/5 + 32
	}
	s.value, s.at, s.valid = val, now, true
	if !c.started {
		c.maybeStart(now)
		return
	}
	c.Cycle(now)
}

func (s *sensor) fresh(now time.Time, stale time.Duration) bool {
	return s.valid && now.Sub(s.at) <= stale
}

func (r *room) temperature(now time.Time, stale time.Duration) (float64, []float64, bool) {
	var sum float64
	var rs []float64
	for _, s := range r.sensors {
		if s.fresh(now, stale) {
			sum += s.value
			rs = append(rs, s.value)
		}
	}
	if len(rs) == 0 {
		return 0, nil, false
	}
	return sum / float64(len(rs)), rs, true
}

func (r *room) targetAt(now time.Time) (float64, bool) {
	if r.cfg.Target != nil {
		return *r.cfg.Target, true
	}
	if len(r.cfg.Schedule) > 0 {
		return ScheduleTarget(r.cfg.Schedule, now)
	}
	return 0, false
}

func (c *Controller) maybeStart(now time.Time) {
	if c.started {
		return
	}
	ready := now.Sub(c.start) >= c.cfg.Control.StartupGrace
	if !ready {
		ready = true
		for _, r := range c.rooms {
			if len(r.sensors) == 0 {
				continue
			}
			if _, _, ok := r.temperature(now, c.cfg.Control.StaleAfter); !ok {
				ready = false
				break
			}
		}
	}
	if ready {
		c.started = true
		c.log.Log("started")
		c.Cycle(now)
	}
}

// Tick is called about once a second.
func (c *Controller) Tick(now time.Time) {
	if !c.started {
		c.maybeStart(now)
		return
	}
	if now.Sub(c.lastCycle) >= c.cfg.Control.CycleInterval {
		c.Cycle(now)
		return
	}
	c.actuate(now)
}

func round(x float64) float64 { return math.Round(x*1000) / 1000 }

func roundp(x *float64) *float64 {
	if x == nil {
		return nil
	}
	v := round(*x)
	return &v
}

type roomEvent struct {
	ID          string    `json:"id"`
	Readings    []float64 `json:"readings"`
	Temperature *float64  `json:"temperature"`
	Target      *float64  `json:"target"`
	Demand      bool      `json:"demand"`
}

type zoneEvent struct {
	ID       string `json:"id"`
	Desired  string `json:"desired"`
	Reported string `json:"reported"`
	Degraded bool   `json:"degraded"`
}

type cycleEvent struct {
	Event  string      `json:"event"`
	Schema int         `json:"schema"`
	TS     string      `json:"ts"`
	Unit   string      `json:"unit"`
	Rooms  []roomEvent `json:"rooms"`
	Zones  []zoneEvent `json:"zones"`
}

func onoff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func reportedStr(z *zone) string {
	if z.reported == nil || (z.online != nil && !*z.online) {
		return "unknown"
	}
	return onoff(*z.reported)
}

// Cycle runs one control cycle and writes its L1 event.
func (c *Controller) Cycle(now time.Time) {
	c.lastCycle = now
	h := c.cfg.Control.Hysteresis
	stale := c.cfg.Control.StaleAfter
	ev := cycleEvent{Event: "control_cycle", Schema: 1, TS: now.UTC().Format(time.RFC3339), Unit: c.cfg.Unit,
		Rooms: []roomEvent{}, Zones: []zoneEvent{}}

	for _, z := range c.zones {
		anyTemp, anyDemand := false, false
		for _, r := range z.rooms {
			for _, s := range r.sensors {
				f := s.fresh(now, stale)
				if s.wasFresh && !f {
					c.log.Log("sensor_stale", "room", r.cfg.ID, "topic", s.topic)
				}
				s.wasFresh = f
			}
			t, readings, ok := r.temperature(now, stale)
			tg, hasTarget := r.targetAt(now)
			r.temp, r.target = nil, nil
			if ok {
				r.temp = &t
			}
			if hasTarget {
				r.target = &tg
			}
			if ok && hasTarget {
				switch {
				case t <= tg-h+eps:
					r.demand = true
				case t >= tg+h-eps:
					r.demand = false
				}
			}
			if ok {
				anyTemp = true
				if r.demand && hasTarget {
					anyDemand = true
				}
			}
			if readings == nil {
				readings = []float64{}
			}
			for i := range readings {
				readings[i] = round(readings[i])
			}
			ev.Rooms = append(ev.Rooms, roomEvent{ID: r.cfg.ID, Readings: readings,
				Temperature: roundp(r.temp), Target: roundp(r.target), Demand: ok && hasTarget && r.demand})
		}
		offline := z.online != nil && !*z.online
		z.degraded = !anyTemp || offline
		z.desired = !z.degraded && anyDemand
		z.hasDesired = true
		ev.Zones = append(ev.Zones, zoneEvent{ID: z.cfg.ID, Desired: onoff(z.desired), Reported: reportedStr(z), Degraded: z.degraded})
	}
	c.actuate(now)
	c.log.Emit(ev)
}

func (c *Controller) send(z *zone, state bool, now time.Time, reason string) {
	payload := "off"
	if state {
		secs := int(math.Ceil(c.cfg.Control.RelayLease.Seconds()))
		payload = fmt.Sprintf("on,%d", secs)
	}
	c.pub.Publish(z.cmdTopic, payload)
	z.lastCmd = now
	c.log.Log("relay_command", "zone", z.cfg.ID, "topic", z.cmdTopic, "payload", payload, "reason", reason)
}

func (c *Controller) resend(z *zone, now time.Time, reason string) {
	if z.commanded {
		c.send(z, z.applied, now, reason)
	}
}

func (c *Controller) actuate(now time.Time) {
	if !c.started {
		return
	}
	cc := c.cfg.Control
	for _, z := range c.zones {
		if !z.hasDesired {
			continue
		}
		switch {
		case !z.commanded:
			z.commanded, z.applied, z.lastSwitch = true, z.desired, now
			c.send(z, z.desired, now, "initial")
			continue
		case z.desired != z.applied && now.Sub(z.lastSwitch) >= cc.MinSwitchInterval:
			z.applied, z.lastSwitch = z.desired, now
			c.send(z, z.desired, now, "switch")
			continue
		}
		if z.online != nil && !*z.online {
			continue // commands to an offline relay are lost; resent when it returns
		}
		mismatch := z.reported == nil || *z.reported != z.applied
		if mismatch && now.Sub(z.lastCmd) >= cc.ConfirmTimeout {
			c.log.Log("relay_mismatch", "zone", z.cfg.ID, "desired", onoff(z.applied), "reported", reportedStr(z))
			c.send(z, z.applied, now, "confirm")
		} else if z.applied && now.Sub(z.lastCmd) >= cc.RelayLease/3 {
			c.send(z, true, now, "lease_refresh")
		}
	}
}

// Shutdown turns every relay off regardless of min_switch_interval (R5).
func (c *Controller) Shutdown(publish func(topic, payload string)) {
	for _, z := range c.zones {
		publish(z.cmdTopic, "off")
		c.log.Log("relay_command", "zone", z.cfg.ID, "topic", z.cmdTopic, "payload", "off", "reason", "shutdown")
	}
}

// Status is a snapshot for the status operation.
type Status struct {
	Unit    string       `json:"unit"`
	Started bool         `json:"started"`
	Zones   []ZoneStatus `json:"zones"`
}

type ZoneStatus struct {
	ID       string       `json:"id"`
	Desired  string       `json:"desired"`
	Reported string       `json:"reported"`
	Degraded bool         `json:"degraded"`
	Online   *bool        `json:"online"`
	Rooms    []RoomStatus `json:"rooms"`
}

type RoomStatus struct {
	ID          string   `json:"id"`
	Temperature *float64 `json:"temperature"`
	Target      *float64 `json:"target"`
	Demand      bool     `json:"demand"`
}

func (c *Controller) Snapshot() Status {
	st := Status{Unit: c.cfg.Unit, Started: c.started}
	for _, z := range c.zones {
		zs := ZoneStatus{ID: z.cfg.ID, Desired: "pending", Reported: reportedStr(z), Degraded: z.degraded, Online: z.online}
		if z.hasDesired {
			zs.Desired = onoff(z.desired)
		}
		for _, r := range z.rooms {
			zs.Rooms = append(zs.Rooms, RoomStatus{ID: r.cfg.ID, Temperature: roundp(r.temp), Target: roundp(r.target),
				Demand: r.temp != nil && r.target != nil && r.demand})
		}
		st.Zones = append(st.Zones, zs)
	}
	return st
}
