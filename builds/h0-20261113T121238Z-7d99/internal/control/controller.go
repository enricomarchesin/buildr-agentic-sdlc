// Package control implements heatctl's control logic.
//
// A Controller is a state machine driven from a single goroutine: every
// method takes the current time and none of them blocks. It keeps all state
// in memory and never persists anything.
package control

import (
	"fmt"
	"math"
	"time"

	"github.com/heatctl/heatctl/internal/config"
	"github.com/heatctl/heatctl/internal/logx"
)

// Publisher sends a non-retained MQTT message.
type Publisher interface {
	Publish(topic, payload string) error
}

// eps absorbs floating point noise in threshold comparisons, e.g. after a
// °C to °F conversion.
const eps = 1e-9

// Reading limits in °C, checked before any conversion.
const (
	minCelsius = -40.0
	maxCelsius = 85.0
)

type tri int8

const (
	unknown tri = iota
	yes
	no
)

func triOf(b bool) tri {
	if b {
		return yes
	}
	return no
}

func onOff(t tri) string {
	switch t {
	case yes:
		return "on"
	case no:
		return "off"
	}
	return "unknown"
}

type sensor struct {
	cfg         config.Sensor
	room        *room
	topic       string
	value       float64 // last valid reading, in the configured unit
	at          time.Time
	has         bool
	staleLogged bool
}

type room struct {
	cfg     config.Room
	zone    *zone
	sensors []*sensor
	demand  bool
}

type zone struct {
	cfg      config.Zone
	rooms    []*room
	relay    *relay
	desired  bool
	degraded bool
}

type relay struct {
	cfg  config.Relay
	zone *zone

	desired bool // from the latest control cycle

	commandedSet bool
	commanded    bool      // last state commanded
	lastSwitch   time.Time // when the commanded state last changed (or first command)
	lastSent     time.Time // when an on/off command was last published
	resend       bool      // re-send the commanded state at the next apply

	reported tri
	online   tri

	mismatchSince time.Time
	lastResend    time.Time
	deferred      bool // a deferred switch has been logged
}

// Controller decides and switches the relays of one house.
type Controller struct {
	cfg *config.Config
	log *logx.Logger
	pub Publisher

	startAt   time.Time
	started   bool
	nextCycle time.Time
	connected bool
	cycles    uint64

	leaseSeconds int
	refreshEvery time.Duration

	zones    []*zone
	rooms    []*room
	sensors  map[string]*sensor
	byStatus map[string]*relay
	byOnline map[string][]*relay
}

// New builds a controller for cfg. now is the start time; startup_grace
// counts from it.
func New(cfg *config.Config, log *logx.Logger, pub Publisher, now time.Time) *Controller {
	c := &Controller{
		cfg:      cfg,
		log:      log,
		pub:      pub,
		startAt:  now,
		sensors:  map[string]*sensor{},
		byStatus: map[string]*relay{},
		byOnline: map[string][]*relay{},
	}
	c.leaseSeconds = int(cfg.Control.RelayLease / time.Second)
	if c.leaseSeconds < 1 {
		c.leaseSeconds = 1
	}
	// Refresh the relay's off-timer well before it runs out, so a relay that
	// should be on never switches itself off while heatctl is running.
	c.refreshEvery = time.Duration(c.leaseSeconds) * time.Second / 3
	if c.refreshEvery < time.Second {
		c.refreshEvery = time.Second
	}
	for _, zc := range cfg.Zones {
		z := &zone{cfg: zc}
		z.relay = &relay{cfg: zc.Relay, zone: z}
		c.byStatus[zc.Relay.StatusTopic()] = z.relay
		c.byOnline[zc.Relay.OnlineTopic()] = append(c.byOnline[zc.Relay.OnlineTopic()], z.relay)
		for _, rc := range zc.Rooms {
			r := &room{cfg: rc, zone: z}
			for _, sc := range rc.Sensors {
				s := &sensor{cfg: sc, room: r, topic: sc.Topic(cfg.MQTT.Zigbee2MQTTBase)}
				r.sensors = append(r.sensors, s)
				c.sensors[s.topic] = s
			}
			z.rooms = append(z.rooms, r)
			c.rooms = append(c.rooms, r)
		}
		c.zones = append(c.zones, z)
	}
	return c
}

// Topics returns every topic heatctl subscribes to for cfg.
func Topics(cfg *config.Config) []string {
	return New(cfg, nil, nil, time.Time{}).topics()
}

func (c *Controller) topics() []string {
	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, z := range c.zones {
		add(z.relay.cfg.StatusTopic())
		add(z.relay.cfg.OnlineTopic())
	}
	for _, r := range c.rooms {
		for _, s := range r.sensors {
			add(s.topic)
		}
	}
	return out
}

// Started reports whether the start phase (R4) is over.
func (c *Controller) Started() bool { return c.started }

// OnConnected is called after every (re)connection, once subscriptions are
// in place.
func (c *Controller) OnConnected(now time.Time) {
	c.connected = true
	for _, z := range c.zones {
		c.requestStatus(z.relay)
	}
	if !c.started {
		c.maybeStart(now)
		return
	}
	for _, z := range c.zones {
		if z.relay.commandedSet {
			z.relay.resend = true
		}
	}
	c.apply(now)
}

// OnDisconnected is called when the broker connection is lost.
func (c *Controller) OnDisconnected(now time.Time) {
	c.connected = false
}

// Tick drives timers: the end of the start phase, periodic cycles, deferred
// switches, lease refreshes and confirmations. Call it often (about every
// half second).
func (c *Controller) Tick(now time.Time) {
	if !c.started {
		c.maybeStart(now)
		return
	}
	if !now.Before(c.nextCycle) {
		c.cycle(now)
		c.nextCycle = c.nextCycle.Add(c.cfg.Control.CycleInterval)
		if !c.nextCycle.After(now) {
			c.nextCycle = now.Add(c.cfg.Control.CycleInterval)
		}
		return // cycle already applied
	}
	c.apply(now)
}

// maybeStart ends the start phase once startup_grace has passed or every
// room with sensors has a temperature.
func (c *Controller) maybeStart(now time.Time) {
	if c.started {
		return
	}
	reason := ""
	if now.Sub(c.startAt) >= c.cfg.Control.StartupGrace {
		reason = "startup_grace_elapsed"
	} else if c.allRoomsHaveTemperature(now) {
		reason = "all_rooms_have_temperature"
	} else {
		return
	}
	c.started = true
	c.log.Event("startup_complete", "reason", reason, "after", now.Sub(c.startAt).Round(time.Millisecond))
	c.cycle(now)
	c.nextCycle = now.Add(c.cfg.Control.CycleInterval)
}

func (c *Controller) allRoomsHaveTemperature(now time.Time) bool {
	for _, r := range c.rooms {
		if len(r.sensors) == 0 {
			continue
		}
		if _, ok := c.temperature(r, now); !ok {
			return false
		}
	}
	return true
}

func (c *Controller) fresh(s *sensor, now time.Time) bool {
	return s.has && now.Sub(s.at) <= c.cfg.Control.StaleAfter
}

// freshReadings returns the last valid readings of the room's fresh sensors.
func (c *Controller) freshReadings(r *room, now time.Time) []float64 {
	var out []float64
	for _, s := range r.sensors {
		if c.fresh(s, now) {
			out = append(out, s.value)
		}
	}
	return out
}

func (c *Controller) temperature(r *room, now time.Time) (float64, bool) {
	vals := c.freshReadings(r, now)
	if len(vals) == 0 {
		return 0, false
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals)), true
}

// target returns the room's target at now, in local time.
func (c *Controller) target(r *room, now time.Time) (float64, bool) {
	return r.cfg.TargetAt(now.In(time.Local))
}

// nextDemand applies the hysteresis rule (B4).
func nextDemand(prev bool, temp, target, h float64) bool {
	switch {
	case temp <= target-h+eps:
		return true
	case temp >= target+h-eps:
		return false
	}
	return prev
}

// cycle evaluates every zone, writes the control_cycle event and applies the
// result to the relays.
func (c *Controller) cycle(now time.Time) {
	c.cycles++
	h := c.cfg.Control.Hysteresis
	ev := cycleEvent{
		Event:  "control_cycle",
		Schema: 1,
		TS:     now.UTC().Format(time.RFC3339),
		Unit:   string(c.cfg.Unit),
		Cycle:  c.cycles,
		Rooms:  []cycleRoom{},
		Zones:  []cycleZone{},
	}
	for _, s := range c.sensors {
		if s.has && !s.staleLogged && !c.fresh(s, now) {
			s.staleLogged = true
			c.log.Event("sensor_stale", "room", s.room.cfg.ID, "sensor", s.cfg.String(),
				"last_reading", round(s.value), "age", now.Sub(s.at).Round(time.Second))
		}
	}
	for _, z := range c.zones {
		anyTemp, anyDemand := false, false
		for _, r := range z.rooms {
			readings := c.freshReadings(r, now)
			temp, hasTemp := c.temperature(r, now)
			target, hasTarget := c.target(r, now)
			cr := cycleRoom{ID: r.cfg.ID, Zone: z.cfg.ID, Readings: roundAll(readings)}
			if hasTarget {
				cr.Target = ptr(round(target))
			}
			if hasTemp && hasTarget {
				r.demand = nextDemand(r.demand, temp, target, h)
				cr.Temperature = ptr(round(temp))
				anyTemp = true
				anyDemand = anyDemand || r.demand
			} else {
				// A room without temperature does not vote and starts
				// afresh when its sensors come back.
				r.demand = false
				if hasTemp {
					cr.Temperature = ptr(round(temp))
				}
			}
			cr.Demand = r.demand
			ev.Rooms = append(ev.Rooms, cr)
		}
		z.desired = anyTemp && anyDemand
		z.degraded = !anyTemp || z.relay.online == no
		z.relay.desired = z.desired
		reason := "no_demand"
		switch {
		case !anyTemp:
			reason = "degraded_no_temperature"
		case z.desired:
			reason = "demand"
		}
		ev.Zones = append(ev.Zones, cycleZone{
			ID:       z.cfg.ID,
			Desired:  onOff(triOf(z.desired)),
			Reported: onOff(z.relay.reported),
			Degraded: z.degraded,
			Online:   onlineString(z.relay.online),
			Reason:   reason,
		})
	}
	c.log.Raw(ev)
	c.apply(now)
}

// apply brings every relay towards its desired state, respecting
// min_switch_interval, refreshing leases and confirming reported states.
func (c *Controller) apply(now time.Time) {
	if !c.started {
		return
	}
	for _, z := range c.zones {
		c.applyRelay(z.relay, now)
	}
}

func (c *Controller) applyRelay(r *relay, now time.Time) {
	ctl := c.cfg.Control
	sent := false
	switch {
	case !r.commandedSet:
		// First command after start: never delayed (B7).
		r.commandedSet = true
		r.lastSwitch = now
		c.command(r, r.desired, now, "initial")
		sent = true
	case r.desired != r.commanded:
		if due := r.lastSwitch.Add(ctl.MinSwitchInterval); !now.Before(due) {
			r.lastSwitch = now
			r.deferred = false
			c.command(r, r.desired, now, "switch")
			sent = true
		} else if !r.deferred {
			r.deferred = true
			c.log.Event("switch_deferred", "zone", r.zone.cfg.ID, "relay", r.cfg.String(),
				"desired", onOff(triOf(r.desired)), "not_before", due.UTC().Format(time.RFC3339))
		}
	case r.deferred:
		r.deferred = false
		c.log.Event("switch_dropped", "zone", r.zone.cfg.ID, "relay", r.cfg.String(),
			"desired", onOff(triOf(r.desired)), "reason", "desired state changed back")
	}
	if !sent && r.resend {
		c.command(r, r.commanded, now, "resend")
		sent = true
	}
	r.resend = false
	if !sent && r.commanded && now.Sub(r.lastSent) >= c.refreshEvery {
		c.command(r, true, now, "lease_refresh")
		sent = true
	}

	// Confirmation (B8).
	if r.reported == triOf(r.commanded) {
		r.mismatchSince = time.Time{}
		return
	}
	if r.mismatchSince.IsZero() {
		r.mismatchSince = now
		return
	}
	if now.Sub(r.mismatchSince) >= ctl.ConfirmTimeout &&
		(r.lastResend.IsZero() || now.Sub(r.lastResend) >= ctl.ConfirmTimeout) {
		r.lastResend = now
		c.log.Event("relay_mismatch", "zone", r.zone.cfg.ID, "relay", r.cfg.String(),
			"commanded", onOff(triOf(r.commanded)), "reported", onOff(r.reported),
			"online", onlineString(r.online), "since", r.mismatchSince.UTC().Format(time.RFC3339))
		if !sent {
			c.command(r, r.commanded, now, "confirm_timeout")
		}
		c.requestStatus(r)
	}
}

func (c *Controller) command(r *relay, on bool, now time.Time, reason string) {
	if on != r.commanded || reason == "initial" {
		r.mismatchSince = time.Time{}
	}
	r.commanded = on
	r.lastSent = now
	payload := "off"
	if on {
		payload = fmt.Sprintf("on,%d", c.leaseSeconds)
	}
	err := c.pub.Publish(r.cfg.CommandTopic(), payload)
	kv := []any{"zone", r.zone.cfg.ID, "relay", r.cfg.String(), "topic", r.cfg.CommandTopic(),
		"payload", payload, "reason", reason}
	if err != nil {
		kv = append(kv, "error", err.Error())
		c.log.Event("relay_command_failed", kv...)
		return
	}
	c.log.Event("relay_command", kv...)
}

func (c *Controller) requestStatus(r *relay) {
	if err := c.pub.Publish(r.cfg.CommandTopic(), "status_update"); err != nil {
		c.log.Event("status_request_failed", "relay", r.cfg.String(), "error", err.Error())
	}
}

// Shutdown turns off every relay, regardless of min_switch_interval (R5).
func (c *Controller) Shutdown(now time.Time) {
	for _, z := range c.zones {
		r := z.relay
		r.commandedSet = true
		r.desired = false
		c.command(r, false, now, "shutdown")
	}
}

func round(v float64) float64 { return math.Round(v*1000) / 1000 }

func roundAll(vs []float64) []float64 {
	out := make([]float64, len(vs))
	for i, v := range vs {
		out[i] = round(v)
	}
	return out
}

func ptr[T any](v T) *T { return &v }

func onlineString(t tri) string {
	switch t {
	case yes:
		return "online"
	case no:
		return "offline"
	}
	return "unknown"
}

type cycleEvent struct {
	Event  string      `json:"event"`
	Schema int         `json:"schema"`
	TS     string      `json:"ts"`
	Unit   string      `json:"unit"`
	Cycle  uint64      `json:"cycle"`
	Rooms  []cycleRoom `json:"rooms"`
	Zones  []cycleZone `json:"zones"`
}

type cycleRoom struct {
	ID          string    `json:"id"`
	Zone        string    `json:"zone"`
	Readings    []float64 `json:"readings"`
	Temperature *float64  `json:"temperature"`
	Target      *float64  `json:"target"`
	Demand      bool      `json:"demand"`
}

type cycleZone struct {
	ID       string `json:"id"`
	Desired  string `json:"desired"`
	Reported string `json:"reported"`
	Degraded bool   `json:"degraded"`
	Online   string `json:"relay_online"`
	Reason   string `json:"reason"`
}
