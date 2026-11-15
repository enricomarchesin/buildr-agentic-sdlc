package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enrico/heatctl/internal/config"
	"github.com/enrico/heatctl/internal/logx"
	"github.com/enrico/heatctl/internal/mqtt"
)

// --- test doubles -----------------------------------------------------------

type recordedEvent struct {
	name   string
	fields logx.Fields
}

type recorder struct {
	mu     sync.Mutex
	events []recordedEvent
}

func newRecorder() *recorder { return &recorder{} }

func (r *recorder) Event(name string, fields logx.Fields) {
	cp := make(logx.Fields, len(fields))
	for k, v := range fields {
		cp[k] = v
	}
	r.mu.Lock()
	r.events = append(r.events, recordedEvent{name: name, fields: cp})
	r.mu.Unlock()
}

func (r *recorder) all() []recordedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedEvent, len(r.events))
	copy(out, r.events)
	return out
}

func (r *recorder) count(name string) int {
	n := 0
	for _, e := range r.all() {
		if e.name == name {
			n++
		}
	}
	return n
}

func (r *recorder) find(name string) []logx.Fields {
	var out []logx.Fields
	for _, e := range r.all() {
		if e.name == name {
			out = append(out, e.fields)
		}
	}
	return out
}

func (r *recorder) last(name string) logx.Fields {
	f := r.find(name)
	if len(f) == 0 {
		return nil
	}
	return f[len(f)-1]
}

func (r *recorder) reset() {
	r.mu.Lock()
	r.events = nil
	r.mu.Unlock()
}

type publish struct {
	topic   string
	payload string
}

type fakeClient struct {
	mu        sync.Mutex
	connected bool
	published []publish
	subs      []string
	failNext  bool
}

func newFakeClient() *fakeClient { return &fakeClient{connected: true} }

func (f *fakeClient) Connect(ctx context.Context) error {
	f.mu.Lock()
	f.connected = true
	f.mu.Unlock()
	return nil
}

func (f *fakeClient) Subscribe(topic string, qos byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs = append(f.subs, topic)
	return nil
}

func (f *fakeClient) Publish(topic string, payload []byte, qos byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.connected {
		return mqtt.ErrNotConnected
	}
	if f.failNext {
		f.failNext = false
		return fmt.Errorf("broker refused the publish")
	}
	f.published = append(f.published, publish{topic: topic, payload: string(payload)})
	return nil
}

func (f *fakeClient) Disconnect(quiesce time.Duration) {
	f.mu.Lock()
	f.connected = false
	f.mu.Unlock()
}

func (f *fakeClient) IsConnected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

// sends returns the commands sent to relays, excluding status requests.
func (f *fakeClient) sends() []publish {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []publish
	for _, p := range f.published {
		if p.payload == "status_update" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func (f *fakeClient) reset() {
	f.mu.Lock()
	f.published = nil
	f.mu.Unlock()
}

// --- harness ----------------------------------------------------------------

const testConfig = `
unit: C
mqtt: { broker: tcp://127.0.0.1:1883 }
control:
  cycle_interval: 60s
  hysteresis: 0.5
  stale_after: 45m
  min_switch_interval: 5m
  relay_lease: 15m
  startup_grace: 2m
  confirm_timeout: 24h
zones:
  - id: ground
    relay: { device: shelly1, switch: 0 }
    rooms:
      - id: living
        target: 20.0
        sensors: [{ device: sensor1, component: 0 }]
`

type harness struct {
	t   *testing.T
	c   *Controller
	cli *fakeClient
	rec *recorder
	now time.Time
	cfg *config.Config

	// The fake clock records the timer the controller arms instead of
	// running it, so tests can see that wake-ups are scheduled.
	timerFn    func()
	timerDelay time.Duration
}

func parseCfg(t *testing.T, yaml string) *config.Config {
	t.Helper()
	cfg, errs := config.Parse([]byte(yaml), "test.yaml")
	if len(errs) > 0 {
		t.Fatalf("config errors:\n%s", config.Diagnostics(errs))
	}
	return cfg
}

func newHarness(t *testing.T, yaml string) *harness {
	t.Helper()
	cfg := parseCfg(t, yaml)
	rec := newRecorder()
	h := &harness{
		t:   t,
		rec: rec,
		cfg: cfg,
		now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
	}
	c := New(cfg, rec, func(mqtt.Handlers) mqtt.Client { return nil })
	c.loc = time.UTC
	c.nowFn = func() time.Time { return h.now }
	c.afterFunc = func(d time.Duration, fn func()) *time.Timer {
		h.timerDelay = d
		h.timerFn = fn
		return nil
	}
	c.startAt = h.now
	h.c = c
	h.cli = newFakeClient()
	c.client = h.cli
	return h
}

func (h *harness) connect(first bool) {
	h.t.Helper()
	h.c.handle(event{kind: evConnected, first: first})
}

func (h *harness) advance(d time.Duration) {
	h.t.Helper()
	h.now = h.now.Add(d)
	h.c.handle(event{kind: evTimer, gen: h.c.timerGen.Load()})
}

func (h *harness) sensor(roomID string, i int) *SensorState {
	h.t.Helper()
	for _, room := range h.c.rooms {
		if room.cfg.ID != roomID {
			continue
		}
		if i < len(room.sensors) {
			return room.sensors[i]
		}
	}
	h.t.Fatalf("no sensor %d in room %s", i, roomID)
	return nil
}

// deliver publishes a payload on a sensor's topic.
func (h *harness) deliver(roomID string, i int, payload string) {
	h.t.Helper()
	s := h.sensor(roomID, i)
	h.c.handle(event{kind: evMessage, msg: mqtt.Message{
		Topic:   s.cfg.Topic(h.cfg.MQTT.Zigbee2MQTTTopic),
		Payload: []byte(payload),
	}})
}

// deliverAt delivers a payload on an arbitrary topic.
func (h *harness) deliverAt(topic, payload string, retained bool) {
	h.t.Helper()
	h.c.handle(event{kind: evMessage, msg: mqtt.Message{
		Topic:    topic,
		Payload:  []byte(payload),
		Retained: retained,
	}})
}

func (h *harness) reading(celsius float64) string {
	return fmt.Sprintf(`{"id":0,"tC":%g,"tF":%g}`, celsius, celsius*9/5+32)
}

// start connects and feeds a first reading so the controller starts at once.
func (h *harness) start(celsius float64) {
	h.t.Helper()
	h.connect(true)
	h.cli.reset()
	h.rec.reset()
	if celsius != 0 {
		h.deliver("living", 0, h.reading(celsius))
	}
}

func (h *harness) snapshot() *Snapshot { return h.c.Snapshot() }

func (h *harness) zone() ZoneSnapshot { return h.snapshot().Zones[0] }
func (h *harness) room() RoomSnapshot { return h.snapshot().Rooms[0] }

func (h *harness) cycleEvents() []logx.Fields { return h.rec.find("control_cycle") }

func (h *harness) lastCycle(t *testing.T) logx.Fields {
	t.Helper()
	evs := h.cycleEvents()
	if len(evs) == 0 {
		t.Fatal("no control_cycle event")
	}
	return evs[len(evs)-1]
}

// cycleRoom extracts one room from a control_cycle event.
func (h *harness) cycleRoom(t *testing.T, ev logx.Fields, id string) CycleRoom {
	t.Helper()
	rooms, ok := ev["rooms"].([]CycleRoom)
	if !ok {
		t.Fatalf("rooms field has type %T", ev["rooms"])
	}
	for _, r := range rooms {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("room %s not in cycle event", id)
	return CycleRoom{}
}

func (h *harness) cycleZone(t *testing.T, ev logx.Fields, id string) CycleZone {
	t.Helper()
	zones, ok := ev["zones"].([]CycleZone)
	if !ok {
		t.Fatalf("zones field has type %T", ev["zones"])
	}
	for _, z := range zones {
		if z.ID == id {
			return z
		}
	}
	t.Fatalf("zone %s not in cycle event", id)
	return CycleZone{}
}

// --- tests ------------------------------------------------------------------

func TestStartupWaitsForGraceOrTemperatures(t *testing.T) {
	h := newHarness(t, testConfig)
	h.connect(true)
	if n := len(h.cycleEvents()); n != 0 {
		t.Fatalf("control cycles before start: %d", n)
	}
	if got := h.cli.sends(); len(got) != 0 {
		t.Fatalf("relay commands before start: %+v", got)
	}
	// The relay status was requested (R4).
	if h.rec.count("relay_status_request") != 1 {
		t.Fatalf("status requests = %d", h.rec.count("relay_status_request"))
	}

	// One room with a sensor, so the first reading starts the controller.
	h.deliver("living", 0, h.reading(19.0))
	if h.rec.count("control_started") != 1 {
		t.Fatalf("controller did not start on the first reading")
	}
	if n := len(h.cycleEvents()); n != 1 {
		t.Fatalf("control cycles after start = %d", n)
	}
}

func TestStartupGraceElapsesWithoutReadings(t *testing.T) {
	h := newHarness(t, testConfig)
	h.connect(true)
	h.advance(2 * time.Minute)
	if h.rec.count("control_started") != 1 {
		t.Fatal("controller did not start when startup_grace elapsed")
	}
	ev := h.lastCycle(t)
	room := h.cycleRoom(t, ev, "living")
	if room.Temperature != nil {
		t.Fatalf("temperature = %v, want null", *room.Temperature)
	}
	if room.Demand {
		t.Fatal("room without temperature demands heat")
	}
	zone := h.cycleZone(t, ev, "ground")
	if zone.Desired != "off" || !zone.Degraded {
		t.Fatalf("zone = %+v", zone)
	}
	sends := h.cli.sends()
	if len(sends) != 1 || sends[0].payload != "off" {
		t.Fatalf("commands = %+v", sends)
	}
}

func TestFirstCycleCommandsOnWithLease(t *testing.T) {
	h := newHarness(t, testConfig)
	h.start(19.0)
	sends := h.cli.sends()
	if len(sends) != 1 {
		t.Fatalf("commands = %+v", sends)
	}
	if sends[0].topic != "shelly1/command/switch:0" {
		t.Fatalf("topic = %q", sends[0].topic)
	}
	if sends[0].payload != "on,900" {
		t.Fatalf("payload = %q, want on,900", sends[0].payload)
	}
	ev := h.lastCycle(t)
	zon := h.cycleZone(t, ev, "ground")
	if zon.Desired != "on" || zon.Reported != "unknown" || zon.Degraded {
		t.Fatalf("zone = %+v", zon)
	}
}

func TestHysteresis(t *testing.T) {
	h := newHarness(t, testConfig)
	h.start(19.4)
	if got := len(h.cli.sends()); got != 1 {
		t.Fatalf("commands after 19.4: %d", got)
	}
	// 20.4 is inside the band: demand is kept, the relay stays on.
	h.advance(3 * time.Minute)
	h.deliver("living", 0, h.reading(20.4))
	if got := len(h.cli.sends()); got != 1 {
		t.Fatalf("commands after 20.4: %+v", h.cli.sends())
	}
	if !h.room().Demand {
		t.Fatal("demand dropped inside the band")
	}
	// 20.5 is at target+hysteresis: demand stops and the relay is switched off.
	h.advance(3 * time.Minute)
	h.deliver("living", 0, h.reading(20.5))
	sends := h.cli.sends()
	if len(sends) != 2 || sends[1].payload != "off" {
		t.Fatalf("commands after 20.5: %+v", sends)
	}
	if h.room().Demand {
		t.Fatal("demand kept at target+hysteresis")
	}
}

// F6.AC6: nothing from an earlier run is known after a restart, so a room
// inside the hysteresis band starts without demand and the relay is commanded
// off instead of staying on.
func TestStartInsideBandCommandsOff(t *testing.T) {
	h := newHarness(t, testConfig)
	h.start(20.1)
	sends := h.cli.sends()
	if len(sends) != 1 || sends[0].payload != "off" {
		t.Fatalf("commands = %+v", sends)
	}
	if h.room().Demand {
		t.Fatal("room inside the band demands heat after a restart")
	}
	if h.zone().Desired != "off" {
		t.Fatalf("desired = %q", h.zone().Desired)
	}
}

func TestFahrenheit(t *testing.T) {
	yaml := strings.Replace(testConfig, "unit: C", "unit: F", 1)
	yaml = strings.Replace(yaml, "target: 20.0", "target: 68.0", 1)
	// F defaults: hysteresis 1.0, so 67.0 <= off, 69.0 >= on-point.
	yaml = strings.Replace(yaml, "  hysteresis: 0.5\n", "", 1)
	h := newHarness(t, yaml)
	if h.cfg.Control.Hysteresis != 1.0 {
		t.Fatalf("hysteresis = %v", h.cfg.Control.Hysteresis)
	}
	h.start(19.4) // 66.92 F <= 67.0
	if got := h.cli.sends(); len(got) != 1 || got[0].payload != "on,900" {
		t.Fatalf("commands = %+v", got)
	}
	h.advance(3 * time.Minute)
	h.deliver("living", 0, h.reading(20.5)) // 68.9 F, inside the band
	if got := len(h.cli.sends()); got != 1 {
		t.Fatalf("commands after 20.5 C: %+v", h.cli.sends())
	}
	if !h.room().Demand {
		t.Fatal("demand dropped inside the band")
	}
	h.advance(3 * time.Minute)
	h.deliver("living", 0, h.reading(20.6)) // 69.08 F >= 69.0
	sends := h.cli.sends()
	if len(sends) != 2 || sends[1].payload != "off" {
		t.Fatalf("commands after 20.6 C: %+v", sends)
	}
}

func TestRoomTemperatureIsMeanOfSensors(t *testing.T) {
	yaml := strings.Replace(testConfig,
		`        sensors: [{ device: sensor1, component: 0 }]`,
		`        sensors:
          - { device: sensor1, component: 0 }
          - { zigbee: living-display }`, 1)
	h := newHarness(t, yaml)
	h.connect(true)
	h.deliver("living", 0, h.reading(19.0))
	h.deliver("living", 1, `{"temperature":20.0,"humidity":48.2,"battery":95,"linkquality":120}`)
	room := h.room()
	if room.Temperature == nil || *room.Temperature != 19.5 {
		t.Fatalf("temperature = %v, want 19.5", room.Temperature)
	}
	ev := h.lastCycle(t)
	cr := h.cycleRoom(t, ev, "living")
	if len(cr.Readings) != 2 || cr.Readings[0] != 19.0 || cr.Readings[1] != 20.0 {
		t.Fatalf("readings = %v", cr.Readings)
	}
	if cr.Temperature == nil || *cr.Temperature != 19.5 {
		t.Fatalf("event temperature = %v", cr.Temperature)
	}
}

func TestInvalidReadingsAreIgnored(t *testing.T) {
	h := newHarness(t, testConfig)
	h.start(19.0)
	before := *h.room().Temperature
	cases := []struct {
		payload string
		reason  string
	}{
		{`not json`, "invalid JSON"},
		{`{"id":0,"tC":null,"tF":null,"errors":["read"]}`, "errors"},
		{`{"id":0,"tC":null,"tF":null}`, "no numeric tC"},
		{`{"id":0,"tC":"20.0"}`, "invalid JSON"},
		{`{"id":100,"tC":null,"tF":null,"errors":["read"]}`, "errors"},
	}
	for _, tc := range cases {
		h.deliver("living", 0, tc.payload)
		if got := *h.room().Temperature; got != before {
			t.Fatalf("payload %s changed the temperature to %v", tc.payload, got)
		}
	}
	if n := h.rec.count("ignored_payload"); n != len(cases) {
		t.Fatalf("ignored_payload events = %d, want %d", n, len(cases))
	}
	if n := h.rec.count("control_cycle"); n != 1 {
		t.Fatalf("control cycles = %d, want 1 (invalid payloads must not cycle)", n)
	}
}

func TestOutOfRangeReadingsIgnored(t *testing.T) {
	h := newHarness(t, testConfig)
	h.start(19.0)
	h.deliver("living", 0, h.reading(85.1))
	h.deliver("living", 0, h.reading(-40.1))
	if n := h.rec.count("ignored_reading"); n != 2 {
		t.Fatalf("ignored_reading = %d", n)
	}
	if got := *h.room().Temperature; got != 19.0 {
		t.Fatalf("temperature = %v", got)
	}
	h.deliver("living", 0, h.reading(-40))
	if got := *h.room().Temperature; got != -40 {
		t.Fatalf("temperature = %v, want -40", got)
	}
	h.deliver("living", 0, h.reading(85))
	if got := *h.room().Temperature; got != 85 {
		t.Fatalf("temperature = %v, want 85", got)
	}
}

func TestRetainedReadingIgnored(t *testing.T) {
	h := newHarness(t, testConfig)
	h.connect(true)
	s := h.sensor("living", 0)
	h.deliverAt(s.cfg.Topic("zigbee2mqtt"), h.reading(19.0), true)
	if h.room().Temperature != nil {
		t.Fatalf("retained reading was used: %v", *h.room().Temperature)
	}
	if h.rec.count("ignored_retained") != 1 {
		t.Fatalf("ignored_retained = %d", h.rec.count("ignored_retained"))
	}
	if h.rec.count("control_started") != 0 {
		t.Fatal("controller started from a retained reading")
	}
}

func TestStaleSensor(t *testing.T) {
	h := newHarness(t, testConfig)
	h.start(19.0)
	if got := len(h.cli.sends()); got != 1 {
		t.Fatalf("commands = %d", got)
	}
	// 46 minutes later the sensor is stale; the next cycle notices.
	h.advance(46 * time.Minute)
	if h.rec.count("sensor_stale") != 1 {
		t.Fatalf("sensor_stale events = %d", h.rec.count("sensor_stale"))
	}
	snap := h.room()
	if snap.Temperature != nil {
		t.Fatalf("stale room still has temperature %v", *snap.Temperature)
	}
	if h.zone().Degraded != true {
		t.Fatal("zone with no fresh temperature is not degraded")
	}
	sends := h.cli.sends()
	if len(sends) != 2 || sends[1].payload != "off" {
		t.Fatalf("commands = %+v", sends)
	}
}

func TestRoomWithoutSensorsNeverVotes(t *testing.T) {
	yaml := strings.Replace(testConfig,
		`      - id: living
        target: 20.0
        sensors: [{ device: sensor1, component: 0 }]`,
		`      - id: living
        target: 20.0
        sensors: [{ device: sensor1, component: 0 }]
      - id: hallway`, 1)
	h := newHarness(t, yaml)
	h.connect(true)
	h.deliver("living", 0, h.reading(23.0)) // above target: no demand
	ev := h.lastCycle(t)
	hall := h.cycleRoom(t, ev, "hallway")
	if hall.Temperature != nil || hall.Target != nil || hall.Demand {
		t.Fatalf("hallway = %+v", hall)
	}
	if h.cycleZone(t, ev, "ground").Desired != "off" {
		t.Fatal("zone desired on although only the sensor-less room is cold")
	}
}

func TestOneRoomDemandingTurnsZoneOn(t *testing.T) {
	yaml := strings.Replace(testConfig,
		`    rooms:
      - id: living
        target: 20.0
        sensors: [{ device: sensor1, component: 0 }]`,
		`    rooms:
      - id: living
        target: 20.0
        sensors: [{ device: sensor1, component: 0 }]
      - id: bedroom
        target: 20.0
        sensors: [{ device: sensor2, component: 0 }]`, 1)
	h := newHarness(t, yaml)
	h.connect(true)
	h.deliver("living", 0, h.reading(19.0))
	h.deliver("bedroom", 0, h.reading(22.0))
	ev := h.lastCycle(t)
	if h.cycleZone(t, ev, "ground").Desired != "on" {
		t.Fatalf("zone = %+v", h.cycleZone(t, ev, "ground"))
	}
	if h.cycleRoom(t, ev, "bedroom").Demand {
		t.Fatal("warm room demands heat")
	}
}

func TestMinSwitchIntervalDelaysAndDrops(t *testing.T) {
	h := newHarness(t, testConfig)
	h.start(19.0) // on at t0
	if got := len(h.cli.sends()); got != 1 {
		t.Fatalf("commands = %d", got)
	}
	// One minute later the room is warm: off is desired but too soon.
	h.advance(1 * time.Minute)
	h.deliver("living", 0, h.reading(21.0))
	if got := len(h.cli.sends()); got != 1 {
		t.Fatalf("switch inside min_switch_interval was applied: %+v", h.cli.sends())
	}
	if h.rec.count("relay_switch_pending") != 1 {
		t.Fatalf("pending events = %d", h.rec.count("relay_switch_pending"))
	}
	// Back below the off point: the pending switch must be dropped.
	h.advance(1 * time.Minute)
	h.deliver("living", 0, h.reading(19.0))
	if got := len(h.cli.sends()); got != 1 {
		t.Fatalf("dropped switch was applied: %+v", h.cli.sends())
	}
	// Long after the interval: still no off command.
	h.advance(20 * time.Minute)
	for _, s := range h.cli.sends() {
		if s.payload == "off" {
			t.Fatalf("dropped switch was applied: %+v", h.cli.sends())
		}
	}

	// Now a real change: warm again after the interval has passed.
	h.deliver("living", 0, h.reading(21.0))
	sends := h.cli.sends()
	if len(sends) < 2 || sends[len(sends)-1].payload != "off" {
		t.Fatalf("commands = %+v", sends)
	}
}

func confirmConfig() string {
	return strings.Replace(testConfig, "confirm_timeout: 24h", "confirm_timeout: 30s", 1)
}

func TestConfirmationResends(t *testing.T) {
	h := newHarness(t, confirmConfig())
	h.start(19.0)
	if got := len(h.cli.sends()); got != 1 {
		t.Fatalf("commands = %d", got)
	}
	// No status ever arrives from the relay.
	h.advance(30 * time.Second)
	if got := len(h.cli.sends()); got != 2 {
		t.Fatalf("commands after confirm_timeout = %d", len(h.cli.sends()))
	}
	if h.rec.count("relay_mismatch") != 1 {
		t.Fatalf("relay_mismatch events = %d", h.rec.count("relay_mismatch"))
	}
	h.advance(30 * time.Second)
	if got := len(h.cli.sends()); got != 3 {
		t.Fatalf("commands after the second confirm_timeout = %d", len(h.cli.sends()))
	}
	for _, s := range h.cli.sends() {
		if s.payload != "on,900" {
			t.Fatalf("payload = %q", s.payload)
		}
	}
}

func TestConfirmationStopsWhenReported(t *testing.T) {
	h := newHarness(t, confirmConfig())
	h.start(19.0)
	h.advance(10 * time.Second)
	h.deliverAt("shelly1/status/switch:0", `{"id":0,"source":"MQTT","output":true,"temperature":{"tC":41.2,"tF":106.2}}`, false)
	if h.zone().Reported != "on" {
		t.Fatal("reported state not applied")
	}
	h.advance(2 * time.Minute)
	if got := len(h.cli.sends()); got != 1 {
		t.Fatalf("commands = %+v", h.cli.sends())
	}
}

// A command that cannot be published must not leave its deadline in the past:
// with the broker away every attempt fails, and an immediate wake-up would spin
// the control loop for as long as the outage lasts.
func TestFailedConfirmDoesNotSpin(t *testing.T) {
	h := newHarness(t, confirmConfig())
	h.start(19.0) // the relay is commanded on and never reports back
	h.cli.Disconnect(0)
	h.rec.reset()

	h.advance(30 * time.Second) // the confirmation deadline
	if got := h.rec.count("relay_command_failed"); got != 1 {
		t.Fatalf("relay_command_failed = %d, want 1", got)
	}
	if h.timerDelay <= 0 || h.timerDelay > 30*time.Second {
		t.Fatalf("next wake-up in %s, want one confirm_timeout", h.timerDelay)
	}
	h.advance(h.timerDelay)
	if got := h.rec.count("relay_command_failed"); got != 2 {
		t.Fatalf("relay_command_failed after the retry = %d, want 2", got)
	}
}

func TestFailedLeaseRefreshDoesNotSpin(t *testing.T) {
	h := newHarness(t, testConfig)
	h.start(19.0)
	h.deliverAt("shelly1/status/switch:0", `{"output":true}`, false)
	h.cli.Disconnect(0)
	for i := 0; i < 7; i++ {
		h.advance(60 * time.Second)
	}
	h.rec.reset()

	h.advance(30 * time.Second) // half the 15m lease
	failed := h.rec.find("relay_command_failed")
	if len(failed) != 1 || failed[0]["reason"] != "lease_refresh" {
		t.Fatalf("relay_command_failed = %+v", failed)
	}
	if h.timerDelay <= 0 {
		t.Fatalf("next wake-up in %s after a failed lease refresh", h.timerDelay)
	}
}

func TestLeaseRefreshKeepsRelayOn(t *testing.T) {
	h := newHarness(t, testConfig)
	h.start(19.0)
	h.deliverAt("shelly1/status/switch:0", `{"output":true}`, false)
	h.advance(7*time.Minute + 30*time.Second)
	sends := h.cli.sends()
	if len(sends) != 2 || sends[1].payload != "on,900" {
		t.Fatalf("commands = %+v", sends)
	}
	if h.rec.last("relay_command")["reason"] != "lease_refresh" {
		t.Fatalf("reason = %v", h.rec.last("relay_command")["reason"])
	}
	h.advance(7*time.Minute + 30*time.Second)
	if got := len(h.cli.sends()); got != 3 {
		t.Fatalf("commands after a second refresh period = %d", got)
	}
	if h.rec.count("relay_command_failed") != 0 {
		t.Fatal("unexpected command failure")
	}
}

func TestRelayOfflineDegradesZone(t *testing.T) {
	h := newHarness(t, testConfig)
	h.start(19.0)
	h.deliverAt("shelly1/online", "true", false)
	if h.zone().Degraded {
		t.Fatal("zone degraded while online")
	}
	h.deliverAt("shelly1/online", "false", false)
	if !h.zone().Degraded {
		t.Fatal("zone not degraded while the relay is offline")
	}
	if h.zone().Reported != "unknown" {
		t.Fatalf("reported = %q", h.zone().Reported)
	}
	if h.rec.count("relay_availability") != 2 {
		t.Fatalf("availability events = %d", h.rec.count("relay_availability"))
	}
	// Retained availability is not trusted.
	h.deliverAt("shelly1/online", "true", true)
	if !h.zone().Degraded {
		t.Fatal("retained availability was trusted")
	}
}

func TestRetainedRelayStatusIgnored(t *testing.T) {
	h := newHarness(t, testConfig)
	h.start(19.0)
	h.deliverAt("shelly1/status/switch:0", `{"output":true}`, true)
	if h.zone().Reported != "unknown" {
		t.Fatalf("reported = %q", h.zone().Reported)
	}
}

func TestControlCycleEventShape(t *testing.T) {
	h := newHarness(t, testConfig)
	h.start(19.0)
	ev := h.lastCycle(t)
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"event", "schema", "ts", "unit", "rooms", "zones"} {
		if _, ok := obj[key]; !ok {
			t.Fatalf("control_cycle event misses %q: %s", key, data)
		}
	}
	if obj["unit"] != "C" {
		t.Fatalf("unit = %v", obj["unit"])
	}
	if _, err := time.Parse(time.RFC3339, obj["ts"].(string)); err != nil {
		t.Fatalf("ts = %v: %v", obj["ts"], err)
	}
	rooms := obj["rooms"].([]any)
	room := rooms[0].(map[string]any)
	for _, key := range []string{"id", "readings", "temperature", "target", "demand"} {
		if _, ok := room[key]; !ok {
			t.Fatalf("room misses %q: %v", key, room)
		}
	}
	if room["temperature"].(float64) != 19.0 || room["target"].(float64) != 20.0 || room["demand"] != true {
		t.Fatalf("room = %v", room)
	}
	zones := obj["zones"].([]any)
	zone := zones[0].(map[string]any)
	for _, key := range []string{"id", "desired", "reported", "degraded"} {
		if _, ok := zone[key]; !ok {
			t.Fatalf("zone misses %q: %v", key, zone)
		}
	}
	if zone["desired"] != "on" || zone["reported"] != "unknown" || zone["degraded"] != false {
		t.Fatalf("zone = %v", zone)
	}
}

func TestOneCycleEventPerCycle(t *testing.T) {
	h := newHarness(t, testConfig)
	h.start(19.0)
	if n := len(h.cycleEvents()); n != 1 {
		t.Fatalf("cycles = %d", n)
	}
	h.advance(1 * time.Minute)
	if n := len(h.cycleEvents()); n != 2 {
		t.Fatalf("cycles after tick = %d", n)
	}
	h.deliver("living", 0, h.reading(19.2))
	if n := len(h.cycleEvents()); n != 3 {
		t.Fatalf("cycles after reading = %d", n)
	}
	for i, ev := range h.cycleEvents() {
		if ev["unit"] != "C" {
			t.Fatalf("cycle %d unit = %v", i, ev["unit"])
		}
	}
}

func TestReconnectResubscribesAndResends(t *testing.T) {
	h := newHarness(t, testConfig)
	h.start(19.0)
	h.cli.reset()
	h.rec.reset()

	h.c.handle(event{kind: evDisconnected, err: fmt.Errorf("connection lost")})
	if h.rec.count("mqtt_disconnected") != 1 {
		t.Fatal("disconnect was not logged")
	}
	h.c.handle(event{kind: evConnected, first: false})
	sends := h.cli.sends()
	if len(sends) != 1 || sends[0].payload != "on,900" {
		t.Fatalf("resend after reconnect = %+v", sends)
	}
	if n := h.rec.count("relay_status_request"); n != 1 {
		t.Fatalf("status requests after reconnect = %d", n)
	}
	if n := len(h.cli.subs); n < 3 {
		t.Fatalf("subscriptions after reconnect = %d", n)
	}
	if h.zone().Reported != "unknown" {
		t.Fatalf("reported = %q after reconnect", h.zone().Reported)
	}
}

func TestShutdownTurnsRelaysOff(t *testing.T) {
	h := newHarness(t, testConfig)
	h.start(19.0)
	h.cli.reset()
	h.c.shutdown(context.Background())
	sends := h.cli.sends()
	if len(sends) != 1 || sends[0].payload != "off" || sends[0].topic != "shelly1/command/switch:0" {
		t.Fatalf("shutdown commands = %+v", sends)
	}
	if !h.cli.connected {
		// Disconnect is called after the publishes.
		t.Log("client disconnected after shutdown")
	}
}

func TestPublishFailureIsRetriedNextCycle(t *testing.T) {
	h := newHarness(t, testConfig)
	h.connect(true)
	h.cli.mu.Lock()
	h.cli.failNext = true
	h.cli.mu.Unlock()
	h.deliver("living", 0, h.reading(19.0))
	if h.rec.count("relay_command_failed") != 1 {
		t.Fatalf("relay_command_failed = %d", h.rec.count("relay_command_failed"))
	}
	if got := len(h.cli.sends()); got != 0 {
		t.Fatalf("commands = %+v", h.cli.sends())
	}
	h.advance(1 * time.Minute)
	if got := len(h.cli.sends()); got != 1 {
		t.Fatalf("commands after retry = %+v", h.cli.sends())
	}
}

func TestSnapshotFields(t *testing.T) {
	h := newHarness(t, testConfig)
	h.start(19.2)
	snap := h.snapshot()
	if snap.Unit != "C" || !snap.Started {
		t.Fatalf("snapshot = %+v", snap)
	}
	if len(snap.Zones) != 1 || len(snap.Rooms) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	z := snap.Zones[0]
	if z.ID != "ground" || z.Relay != "shelly1/0" || z.Desired != "on" || z.Reported != "unknown" {
		t.Fatalf("zone = %+v", z)
	}
	r := snap.Rooms[0]
	if r.ID != "living" || r.Zone != "ground" || r.Temperature == nil || *r.Temperature != 19.2 ||
		r.Target == nil || *r.Target != 20.0 || !r.Demand {
		t.Fatalf("room = %+v", r)
	}
	if len(r.Sensors) != 1 || r.Sensors[0].Reading == nil || !r.Sensors[0].Fresh {
		t.Fatalf("sensor = %+v", r.Sensors)
	}
}

func TestMalformedRelayStatusIgnored(t *testing.T) {
	h := newHarness(t, testConfig)
	h.start(19.0)
	for _, payload := range []string{`nope`, `{"id":0}`, `{"output":"on"}`, `{"output":null}`} {
		h.deliverAt("shelly1/status/switch:0", payload, false)
		if got := h.zone().Reported; got != "unknown" {
			t.Fatalf("payload %s set reported=%s", payload, got)
		}
	}
	if n := h.rec.count("ignored_payload"); n != 4 {
		t.Fatalf("ignored_payload = %d", n)
	}
	// The controller keeps working.
	h.advance(30 * time.Second)
	if n := h.rec.count("control_cycle"); n == 0 {
		t.Fatal("controller stopped cycling")
	}
}

func TestUnknownTopicIgnored(t *testing.T) {
	h := newHarness(t, testConfig)
	h.start(19.0)
	h.rec.reset()
	h.deliverAt("someone/else", `{"tC":10}`, false)
	if n := len(h.rec.all()); n != 0 {
		t.Fatalf("unknown topic produced events: %+v", h.rec.all())
	}
}

func TestUnitFConversionAppliedOnArrival(t *testing.T) {
	yaml := strings.Replace(testConfig, "unit: C", "unit: F", 1)
	yaml = strings.Replace(yaml, "target: 20.0", "target: 68.0", 1)
	h := newHarness(t, yaml)
	h.start(19.0)
	if got := *h.room().Temperature; got != 66.2 {
		t.Fatalf("temperature = %v, want 66.2", got)
	}
}

// The controller must keep waking itself up: a timer event has to schedule the
// next wake-up even when no MQTT message arrives in between (F8.AC1, F4.AC1).
func TestTimerRearmedAfterEveryWakeUp(t *testing.T) {
	h := newHarness(t, testConfig)
	h.start(19.0)

	if h.timerFn == nil {
		t.Fatal("no wake-up armed after startup")
	}
	if h.timerDelay <= 0 || h.timerDelay > 60*time.Second {
		t.Fatalf("first wake-up in %s, want the first control cycle", h.timerDelay)
	}
	cycles := h.rec.count("control_cycle")

	h.timerFn = nil
	h.advance(60 * time.Second)

	if h.rec.count("control_cycle") != cycles+1 {
		t.Fatalf("a timer event did not run one control cycle")
	}
	if h.timerFn == nil {
		t.Fatal("no wake-up re-armed after a timer event")
	}
	if h.timerDelay <= 0 || h.timerDelay > 60*time.Second {
		t.Fatalf("next wake-up in %s, want the next control cycle", h.timerDelay)
	}
}
