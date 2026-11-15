// Package control implements heatctl's control loop: it turns sensor readings
// into relay decisions and keeps the relays in the state the house needs.
package control

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/enrico/heatctl/internal/config"
	"github.com/enrico/heatctl/internal/logx"
	"github.com/enrico/heatctl/internal/mqtt"
)

// ClientFactory builds an MQTT client. It exists so that tests can run the
// controller against a fake broker.
type ClientFactory func(mqtt.Handlers) mqtt.Client

// Controller is one heatctl control loop.
type Controller struct {
	cfg       *config.Config
	log       logx.Logger
	newClient ClientFactory

	events chan event
	quit   chan struct{}
	once   sync.Once

	started      bool
	startAt      time.Time
	connected    bool
	connectTries int
	nextCycle    time.Time
	cycleSeq     int64

	sensors            []*SensorState
	sensorByTopic      map[string][]*SensorState
	rooms              []*RoomState
	zones              []*ZoneState
	relays             []*RelayState
	relayByStatusTopic map[string]*RelayState
	relayByOnlineTopic map[string]*RelayState

	client   mqtt.Client
	snapshot atomic.Pointer[Snapshot]

	timer    *time.Timer
	timerAt  time.Time
	timerGen atomic.Uint64

	loc       *time.Location
	nowFn     func() time.Time
	afterFunc func(time.Duration, func()) *time.Timer
}

type eventKind int

const (
	evConnected eventKind = iota
	evDisconnected
	evMessage
	evTimer
)

type event struct {
	kind  eventKind
	first bool
	err   error
	msg   mqtt.Message
	gen   uint64
}

// New builds a controller for a validated configuration.
func New(cfg *config.Config, logger logx.Logger, factory ClientFactory) *Controller {
	c := &Controller{
		cfg:       cfg,
		log:       logger,
		newClient: factory,
		events:    make(chan event, 512),
		quit:      make(chan struct{}),
		loc:       time.Local,
		nowFn:     time.Now,
		afterFunc: time.AfterFunc,
	}
	c.setup()
	c.publishSnapshot(c.now())
	return c
}

func (c *Controller) now() time.Time { return c.nowFn() }

func (c *Controller) setup() {
	c.sensorByTopic = make(map[string][]*SensorState)
	c.relayByStatusTopic = make(map[string]*RelayState)
	c.relayByOnlineTopic = make(map[string]*RelayState)

	for zi := range c.cfg.Zones {
		zc := &c.cfg.Zones[zi]
		z := &ZoneState{cfg: zc}
		r := &RelayState{cfg: zc.Relay, zone: z}
		z.relay = r
		c.relays = append(c.relays, r)
		c.relayByStatusTopic[r.cfg.StatusTopic()] = r
		c.relayByOnlineTopic[r.cfg.OnlineTopic()] = r

		for ri := range zc.Rooms {
			rc := &zc.Rooms[ri]
			room := &RoomState{cfg: rc, zone: z}
			z.rooms = append(z.rooms, room)
			c.rooms = append(c.rooms, room)
			for si := range rc.Sensors {
				s := &SensorState{cfg: rc.Sensors[si], room: room}
				room.sensors = append(room.sensors, s)
				c.sensors = append(c.sensors, s)
				topic := s.cfg.Topic(c.cfg.MQTT.Zigbee2MQTTTopic)
				c.sensorByTopic[topic] = append(c.sensorByTopic[topic], s)
			}
		}
		c.zones = append(c.zones, z)
	}
}

// Snapshot returns the latest state for the status operation.
func (c *Controller) Snapshot() *Snapshot {
	if s := c.snapshot.Load(); s != nil {
		return s
	}
	return &Snapshot{Schema: 1, Unit: string(c.cfg.Unit)}
}

// post hands an event to the control loop. It never blocks once the loop has
// stopped.
func (c *Controller) post(ev event) {
	select {
	case c.events <- ev:
	case <-c.quit:
	}
}

// Run connects to the broker and runs the control loop until ctx is cancelled.
func (c *Controller) Run(ctx context.Context) error {
	c.startAt = c.now()
	c.armTimer()
	c.log.Event("startup", logx.Fields{
		"unit":           string(c.cfg.Unit),
		"broker":         c.cfg.MQTT.Broker,
		"client_id":      c.cfg.MQTT.ClientID,
		"zones":          len(c.zones),
		"rooms":          len(c.rooms),
		"sensors":        len(c.sensors),
		"cycle_interval": c.cfg.Control.CycleInterval.String(),
		"hysteresis":     c.cfg.Control.Hysteresis,
		"stale_after":    c.cfg.Control.StaleAfter.String(),
		"relay_lease":    c.cfg.Control.RelayLease.String(),
		"startup_grace":  c.cfg.Control.StartupGrace.String(),
	})

	c.client = c.newClient(mqtt.Handlers{
		OnConnect:        func(first bool) { c.post(event{kind: evConnected, first: first}) },
		OnConnectionLost: func(err error) { c.post(event{kind: evDisconnected, err: err}) },
		OnMessage:        func(m mqtt.Message) { c.post(event{kind: evMessage, msg: m}) },
	})

	if err := c.connect(ctx); err != nil {
		if ctx.Err() != nil {
			c.log.Event("shutdown", logx.Fields{"reason": "signal before the broker connection was established"})
			return nil
		}
		return err
	}

	for {
		select {
		case <-ctx.Done():
			c.shutdown(ctx)
			return nil
		case ev := <-c.events:
			c.handle(ev)
		}
	}
}

// connect establishes the first broker connection, retrying with a backoff
// capped at 60s.
func (c *Controller) connect(ctx context.Context) error {
	backoff := time.Second
	for {
		c.connectTries++
		c.log.Event("mqtt_connecting", logx.Fields{
			"broker":  c.cfg.MQTT.Broker,
			"attempt": c.connectTries,
		})
		err := c.client.Connect(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if c.connectTries >= 3 && backoff < 60*time.Second {
			backoff *= 2
			if backoff > 60*time.Second {
				backoff = 60 * time.Second
			}
		}
		c.log.Event("mqtt_connect_failed", logx.Fields{
			"broker":     c.cfg.MQTT.Broker,
			"error":      err.Error(),
			"retry_in_s": backoff.Seconds(),
		})
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *Controller) handle(ev event) {
	switch ev.kind {
	case evConnected:
		c.onConnected(ev.first)
	case evDisconnected:
		c.onDisconnected(ev.err)
	case evMessage:
		c.onMessage(ev.msg)
	case evTimer:
		if ev.gen != c.timerGen.Load() {
			return
		}
		now := c.now()
		wasStarted := c.started
		c.maybeStart(now)
		if c.started && wasStarted {
			// A due cycle moves the cycle clock; a wake-up that was only
			// scheduled for a relay deadline evaluates without resetting it.
			if !now.Before(c.nextCycle) {
				c.nextCycle = now.Add(c.cfg.Control.CycleInterval)
			}
			c.evaluate(now)
		}
	}
	c.armTimer()
}

func (c *Controller) onConnected(first bool) {
	c.connected = true
	c.log.Event("mqtt_connected", logx.Fields{
		"broker":    c.cfg.MQTT.Broker,
		"client_id": c.cfg.MQTT.ClientID,
		"first":     first,
	})

	subs := make([]string, 0, len(c.sensorByTopic)+2*len(c.relays))
	seen := make(map[string]bool)
	addSub := func(topic string) {
		if topic == "" || seen[topic] {
			return
		}
		seen[topic] = true
		subs = append(subs, topic)
	}
	for topic := range c.sensorByTopic {
		addSub(topic)
	}
	for _, r := range c.relays {
		addSub(r.cfg.StatusTopic())
		addSub(r.cfg.OnlineTopic())
	}
	for _, topic := range subs {
		if err := c.client.Subscribe(topic, mqtt.QoSSensor); err != nil {
			c.log.Event("subscribe_failed", logx.Fields{"topic": topic, "error": err.Error()})
		}
	}
	c.log.Event("subscriptions_active", logx.Fields{"count": len(subs)})

	for _, r := range c.relays {
		c.requestStatus(r)
	}

	now := c.now()
	if first {
		// Count startup_grace from the moment the broker has been reached and
		// subscribed to: a broker that was not up yet when the process started
		// gave sensors no chance to be heard (R4).
		c.startAt = now
		if c.maybeStart(now) {
			return
		}
		c.logStartupWait(now)
		return
	}

	// Reconnected: states learned before the loss are no longer trustworthy
	// and subscriptions have to be made again (done above).
	for _, r := range c.relays {
		r.reported = nil
		r.online = nil
		r.mismatchSince = time.Time{}
		r.lastConfirmAt = time.Time{}
		r.pendingAt = time.Time{}
		if c.started {
			c.resendDesired(r, now)
		}
	}
	c.publishSnapshot(now)
	c.maybeStart(now)
}

func (c *Controller) onDisconnected(err error) {
	if !c.connected {
		return
	}
	c.connected = false
	fields := logx.Fields{}
	if err != nil {
		fields["error"] = err.Error()
	}
	c.log.Event("mqtt_disconnected", fields)
	for _, r := range c.relays {
		r.reported = nil
		r.online = nil
		r.pendingAt = time.Time{}
		r.mismatchSince = time.Time{}
	}
	c.publishSnapshot(c.now())
}

func (c *Controller) logStartupWait(now time.Time) {
	var missing []string
	for _, room := range c.rooms {
		if !room.cfg.HasSensors() {
			continue
		}
		if c.roomTemperature(room, now) == nil {
			missing = append(missing, room.cfg.ID)
		}
	}
	c.log.Event("startup_wait", logx.Fields{
		"remaining_s":               c.cfg.Control.StartupGrace.Seconds() - now.Sub(c.startAt).Seconds(),
		"rooms_without_temperature": missing,
	})
}

// maybeStart runs the first control cycle once the startup conditions hold:
// either startup_grace has passed or every room with sensors has a
// temperature. It returns true when the controller is (now) started.
func (c *Controller) maybeStart(now time.Time) bool {
	if c.started {
		return true
	}
	if !c.connected {
		return false
	}
	reason := ""
	switch {
	case now.Sub(c.startAt) >= c.cfg.Control.StartupGrace:
		reason = "startup_grace_elapsed"
	default:
		all := true
		anySensor := false
		for _, room := range c.rooms {
			if !room.cfg.HasSensors() {
				continue
			}
			anySensor = true
			if c.roomTemperature(room, now) == nil {
				all = false
				break
			}
		}
		if !anySensor {
			reason = "no_sensor_rooms"
		} else if all {
			reason = "all_rooms_have_temperature"
		}
	}
	if reason == "" {
		return false
	}
	c.started = true
	c.nextCycle = now.Add(c.cfg.Control.CycleInterval)
	c.log.Event("control_started", logx.Fields{"reason": reason})
	c.evaluate(now)
	return true
}

// roomTemperature computes the mean of a room's fresh readings without
// changing any state.
func (c *Controller) roomTemperature(room *RoomState, now time.Time) *float64 {
	var sum float64
	n := 0
	for _, s := range room.sensors {
		if s.last == nil {
			continue
		}
		if now.Sub(s.last.At) > c.cfg.Control.StaleAfter {
			continue
		}
		sum += s.last.Value
		n++
	}
	if n == 0 {
		return nil
	}
	t := sum / float64(n)
	return &t
}

// shutdown turns every relay off and closes the connection (R5).
func (c *Controller) shutdown(ctx context.Context) {
	c.once.Do(func() {
		close(c.quit)
		if c.timer != nil {
			c.timer.Stop()
		}
		off := make(chan struct{})
		go func() {
			defer close(off)
			if c.client == nil || !c.client.IsConnected() {
				return
			}
			var wg sync.WaitGroup
			for _, r := range c.relays {
				wg.Add(1)
				go func(r *RelayState) {
					defer wg.Done()
					topic := r.cfg.CommandTopic()
					if err := c.client.Publish(topic, []byte("off"), mqtt.QoSCommand); err != nil {
						c.log.Event("relay_command_failed", logx.Fields{
							"relay": r.cfg.String(), "command": "off", "reason": "shutdown", "error": err.Error(),
						})
						return
					}
					c.log.Event("relay_command", logx.Fields{
						"relay": r.cfg.String(), "device": r.cfg.Device, "switch": r.cfg.Switch,
						"command": "off", "reason": "shutdown",
					})
				}(r)
			}
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
			}
		}()
		select {
		case <-off:
		case <-time.After(3500 * time.Millisecond):
		}
		if c.client != nil {
			c.client.Disconnect(200 * time.Millisecond)
		}
		c.log.Event("shutdown", logx.Fields{"relays": len(c.relays)})
	})
}

// armTimer schedules the next wake-up for the control loop.
func (c *Controller) armTimer() {
	at, ok := c.nextDeadline()
	gen := c.timerGen.Add(1)
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	if !ok {
		return
	}
	c.timerAt = at
	d := at.Sub(c.now())
	if d < 0 {
		d = 0
	}
	c.timer = c.afterFunc(d, func() {
		c.post(event{kind: evTimer, gen: gen})
	})
}

func (c *Controller) nextDeadline() (time.Time, bool) {
	var next time.Time
	have := false
	add := func(t time.Time) {
		if t.IsZero() {
			return
		}
		if !have || t.Before(next) {
			next, have = t, true
		}
	}
	now := c.now()
	if !c.started {
		if !c.connected {
			return time.Time{}, false
		}
		grace := c.startAt.Add(c.cfg.Control.StartupGrace)
		if grace.After(now) {
			return grace, true
		}
		return now, true
	}
	add(c.nextCycle)
	ct := c.cfg.Control
	for _, r := range c.relays {
		if !r.pendingAt.IsZero() {
			add(r.pendingAt)
		}
		desired := r.zone.desired
		if r.lastCommand != nil && *r.lastCommand == desired && (r.reported == nil || *r.reported != desired) {
			// Wait one confirm_timeout from the command or the last attempt
			// before deciding that the relay did not obey.
			base := r.mismatchSince
			if base.IsZero() {
				base = r.lastCommandAt
			}
			if !base.IsZero() {
				due := base.Add(ct.ConfirmTimeout)
				if !r.lastConfirmAt.IsZero() {
					if later := r.lastConfirmAt.Add(ct.ConfirmTimeout); later.After(due) {
						due = later
					}
				}
				add(due)
			}
		}
		if desired && r.lastCommand != nil && *r.lastCommand && !r.lastLeaseRefresh.IsZero() {
			add(r.lastLeaseRefresh.Add(c.refreshInterval()))
		}
	}
	return next, have
}

func (c *Controller) refreshInterval() time.Duration {
	d := c.cfg.Control.RelayLease / 2
	if d <= 0 {
		d = time.Second
	}
	return d
}

// leaseSeconds is the value heatctl sends in "on,<seconds>". It is rounded up
// so the relay's own timer never expires before the lease heatctl planned for.
func (c *Controller) leaseSeconds() int {
	secs := c.cfg.Control.RelayLease.Seconds()
	n := int(secs)
	if float64(n) < secs {
		n++
	}
	if n < 1 {
		n = 1
	}
	return n
}
