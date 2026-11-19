package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/heatctl/heatctl/internal/config"
	"github.com/heatctl/heatctl/internal/logx"
)

type sent struct{ topic, payload string }

// switches returns relay commands that change the commanded state,
// ignoring lease refreshes and re-sends, and forgets them.
func (h *harness) switches() []string {
	if h.lastCmd == nil {
		h.lastCmd = map[string]string{}
	}
	var out []string
	for _, m := range h.pub.msgs {
		if m.payload == "status_update" || h.lastCmd[m.topic] == m.payload {
			continue
		}
		h.lastCmd[m.topic] = m.payload
		out = append(out, m.topic+" "+m.payload)
	}
	h.pub.msgs = nil
	return out
}

type fakePub struct {
	msgs    []sent
	pending []sent // not yet seen by the simulated relays
	down    bool
}

func (p *fakePub) Publish(topic, payload string) error {
	if p.down {
		return fmt.Errorf("not connected")
	}
	p.msgs = append(p.msgs, sent{topic, payload})
	p.pending = append(p.pending, sent{topic, payload})
	return nil
}

// commands returns relay commands (not status requests) and forgets them.
func (p *fakePub) commands() []string {
	var out []string
	for _, m := range p.msgs {
		if m.payload != "status_update" {
			out = append(out, m.topic+" "+m.payload)
		}
	}
	p.msgs = nil
	return out
}

type harness struct {
	t   *testing.T
	c   *Controller
	pub *fakePub
	log *bytes.Buffer
	now time.Time

	// When autoReport is set, simulated relays answer commands and status
	// requests like Shelly devices do.
	autoReport bool
	relayOn    map[string]bool
	lastCmd    map[string]string
}

// deliver lets the simulated relays react to what was published.
func (h *harness) deliver() {
	for h.autoReport && len(h.pub.pending) > 0 {
		m := h.pub.pending[0]
		h.pub.pending = h.pub.pending[1:]
		dev, ch, ok := strings.Cut(m.topic, "/command/")
		if !ok {
			continue
		}
		prev, known := h.relayOn[m.topic]
		on := prev
		switch {
		case m.payload == "status_update":
		case m.payload == "off":
			on = false
		case strings.HasPrefix(m.payload, "on"):
			on = true
		}
		h.relayOn[m.topic] = on
		if m.payload == "status_update" || !known || on != prev {
			h.c.HandleMessage(h.now, dev+"/status/"+ch, []byte(fmt.Sprintf(`{"output":%v}`, on)), false)
		}
	}
	if !h.autoReport {
		h.pub.pending = nil
	}
}

const testConfig = `unit: C
mqtt: { broker: "tcp://b:1883" }
zones:
  - id: ground
    relay: { device: relay1, switch: 0 }
    rooms:
      - id: living
        target: 20
        sensors: [{ zigbee: s1 }, { device: ht1, component: 0 }]
      - id: kitchen
        target: 20
        sensors: [{ zigbee: s2 }]
      - id: hallway
  - id: upstairs
    relay: { device: relay2, switch: 1 }
    rooms:
      - id: bedroom
        target: 20
        sensors: [{ zigbee: s3 }]
`

// Monday 2026-11-02 12:00 local.
var t0 = time.Date(2026, 11, 2, 12, 0, 0, 0, time.Local)

func newHarness(t *testing.T, src string) *harness {
	t.Helper()
	cfg, errs := config.Parse([]byte(src))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	h := &harness{t: t, pub: &fakePub{}, log: &bytes.Buffer{}, now: t0, autoReport: true, relayOn: map[string]bool{}}
	h.c = New(cfg, logx.NewSync(h.log, func() time.Time { return h.now }), h.pub, t0)
	h.c.OnConnected(t0)
	h.deliver()
	return h
}

func (h *harness) advance(d time.Duration) {
	end := h.now.Add(d)
	for h.now.Before(end) {
		step := 250 * time.Millisecond
		if end.Sub(h.now) < step {
			step = end.Sub(h.now)
		}
		h.now = h.now.Add(step)
		h.c.Tick(h.now)
		h.deliver()
	}
}

func (h *harness) zigbee(name string, c float64) {
	h.c.HandleMessage(h.now, "zigbee2mqtt/"+name, []byte(fmt.Sprintf(`{"temperature":%v,"humidity":40}`, c)), false)
	h.deliver()
}

func (h *harness) msg(topic, payload string) {
	h.c.HandleMessage(h.now, topic, []byte(payload), false)
	h.deliver()
}

func (h *harness) status() Status { return h.c.Snapshot(h.now, "test") }

func (h *harness) room(id string) RoomStatus {
	for _, z := range h.status().Zones {
		for _, r := range z.Rooms {
			if r.ID == id {
				return r
			}
		}
	}
	h.t.Fatalf("no room %s", id)
	return RoomStatus{}
}

func (h *harness) zone(id string) ZoneStatus {
	for _, z := range h.status().Zones {
		if z.ID == id {
			return z
		}
	}
	h.t.Fatalf("no zone %s", id)
	return ZoneStatus{}
}

func (h *harness) events(name string) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(h.log.String(), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			h.t.Fatalf("log line is not JSON: %q", line)
		}
		if m["event"] == name {
			out = append(out, m)
		}
	}
	return out
}

// startAll gives every room with sensors a temperature, ending the start.
func (h *harness) startAll(living, kitchen, bedroom float64) {
	h.zigbee("s1", living)
	h.zigbee("s2", kitchen)
	h.zigbee("s3", bedroom)
	if !h.c.Started() {
		h.t.Fatal("not started after every room has a temperature")
	}
}

func eqCmds(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
}

func temp(t *testing.T, r RoomStatus) float64 {
	t.Helper()
	if r.Temperature == nil {
		t.Fatalf("room %s has no temperature", r.ID)
	}
	return *r.Temperature
}

func TestF2AC1MeanOfFreshSensors(t *testing.T) {
	h := newHarness(t, testConfig)
	h.zigbee("s1", 19.0)
	h.msg("ht1/status/temperature:0", `{"id":0,"tC":20.0,"tF":68.0}`)
	if got := temp(t, h.room("living")); got != 19.5 {
		t.Errorf("temperature %v, want 19.5", got)
	}
}

func TestF2AC2InvalidReadings(t *testing.T) {
	h := newHarness(t, testConfig)
	h.zigbee("s1", 19.0)
	h.msg("ht1/status/temperature:0", `{"id":0,"tC":null,"tF":null,"errors":["read"]}`)
	h.msg("ht1/status/temperature:0", `{"id":0,"tC":21.0,"errors":["out_of_range"]}`)
	h.msg("ht1/status/temperature:0", `{"id":0,"tC":"hot"}`)
	h.msg("ht1/status/temperature:0", `not json`)
	h.msg("zigbee2mqtt/s1", `{"temperature":null}`)
	h.msg("zigbee2mqtt/s1", `{"temperature":"20"}`)
	h.msg("zigbee2mqtt/s1", `{"battery":90}`)
	h.msg("zigbee2mqtt/s1", `{"temperature":99}`)    // out of range (R3)
	h.msg("zigbee2mqtt/s1", `{"temperature":-40.5}`) // out of range (R3)
	if got := temp(t, h.room("living")); got != 19.0 {
		t.Errorf("temperature %v, want 19.0", got)
	}
	if n := len(h.events("reading_invalid")) + len(h.events("payload_invalid")) + len(h.events("reading_out_of_range")); n != 8 {
		t.Errorf("logged %d ignored payloads, want 8:\n%s", n, h.log)
	}
}

func TestF2AC3Stale(t *testing.T) {
	h := newHarness(t, testConfig)
	h.zigbee("s1", 19.0)
	h.advance(10 * time.Minute)
	h.msg("ht1/status/temperature:0", `{"id":0,"tC":21.0}`)
	if got := temp(t, h.room("living")); got != 20 {
		t.Fatalf("temperature %v", got)
	}
	h.advance(35*time.Minute + time.Second) // s1 is now 45m01s old
	if got := temp(t, h.room("living")); got != 21 {
		t.Errorf("temperature %v, want 21 (stale sensor dropped)", got)
	}
	h.advance(10 * time.Minute)
	if h.room("living").Temperature != nil {
		t.Errorf("room still has a temperature with every sensor stale")
	}
}

func TestF4AC1AC2Hysteresis(t *testing.T) {
	h := newHarness(t, testConfig)
	h.startAll(20.0, 20.0, 20.0)
	eqCmds(t, h.switches(), "relay1/command/switch:0 off", "relay2/command/switch:1 off")
	h.advance(6 * time.Minute)
	h.zigbee("s2", 19.4)
	eqCmds(t, h.switches(), "relay1/command/switch:0 on,900")
	h.advance(6 * time.Minute)
	h.zigbee("s2", 20.4)
	eqCmds(t, h.switches())
	if h.zone("ground").Desired != "on" {
		t.Error("relay not kept on at 20.4")
	}
	h.zigbee("s2", 20.5)
	eqCmds(t, h.switches(), "relay1/command/switch:0 off")
}

func TestF4AC3AnyRoomDemands(t *testing.T) {
	h := newHarness(t, testConfig)
	h.startAll(22.0, 19.0, 22.0)
	eqCmds(t, h.pub.commands(), "relay1/command/switch:0 on,900", "relay2/command/switch:1 off")
}

func TestF4AC4AllStaleDegraded(t *testing.T) {
	h := newHarness(t, testConfig)
	h.startAll(19.0, 19.0, 19.0)
	h.pub.commands()
	for i := 0; i < 50; i++ {
		h.advance(time.Minute)
		h.zigbee("s3", 19.0) // keep upstairs fresh
	}
	z := h.zone("ground")
	if !z.Degraded || z.Desired != "off" || z.Commanded != "off" {
		t.Errorf("ground after all sensors went stale: %+v", z)
	}
	if h.zone("upstairs").Degraded {
		t.Error("upstairs degraded")
	}
	cmds := h.pub.commands()
	found := false
	for _, c := range cmds {
		if c == "relay1/command/switch:0 off" {
			found = true
		}
	}
	if !found {
		t.Errorf("ground relay not commanded off: %q", cmds)
	}
	ev := h.events("control_cycle")
	last := ev[len(ev)-1]["zones"].([]any)[0].(map[string]any)
	if last["degraded"] != true || last["desired"] != "off" {
		t.Errorf("last cycle zone: %v", last)
	}
}

func TestF4AC5RoomWithoutSensors(t *testing.T) {
	h := newHarness(t, testConfig)
	h.startAll(20.0, 20.0, 20.0)
	r := h.room("hallway")
	if r.Temperature != nil || r.Target != nil || r.Demand {
		t.Errorf("hallway: %+v", r)
	}
	if h.zone("ground").Degraded {
		t.Error("zone degraded because of a room without sensors")
	}
}

func TestF4AC6Fahrenheit(t *testing.T) {
	src := strings.Replace(testConfig, "unit: C", "unit: F", 1)
	src = strings.ReplaceAll(src, "target: 20", "target: 68.0")
	h := newHarness(t, src)
	h.startAll(25, 25, 25)
	h.switches()
	h.advance(6 * time.Minute)
	h.zigbee("s1", 19.4)
	h.zigbee("s2", 19.4)
	eqCmds(t, h.switches(), "relay1/command/switch:0 on,900")
	if got := temp(t, h.room("living")); got != 66.92 {
		t.Errorf("19.4 °C shown as %v °F", got)
	}
	h.advance(6 * time.Minute)
	h.zigbee("s1", 20.5)
	h.zigbee("s2", 20.5)
	eqCmds(t, h.switches())
	h.zigbee("s1", 20.6)
	h.zigbee("s2", 20.6)
	eqCmds(t, h.switches(), "relay1/command/switch:0 off")
	if ev := h.events("control_cycle"); ev[len(ev)-1]["unit"] != "F" {
		t.Error("unit not F in control_cycle")
	}
}

func TestF5AC1MinSwitchInterval(t *testing.T) {
	h := newHarness(t, testConfig)
	h.startAll(19.0, 20.0, 20.0) // ground on at t0
	eqCmds(t, h.pub.commands(), "relay1/command/switch:0 on,900", "relay2/command/switch:1 off")

	// Change too soon: waits until 5 min after the last switch.
	h.advance(time.Minute)
	h.zigbee("s1", 21.0)
	eqCmds(t, h.pub.commands())
	h.advance(3*time.Minute + 58*time.Second)
	eqCmds(t, h.pub.commands())
	h.advance(2 * time.Second)
	eqCmds(t, h.pub.commands(), "relay1/command/switch:0 off")

	// Change and change back within the interval: dropped.
	h.advance(time.Minute)
	h.zigbee("s1", 19.0)
	h.advance(time.Minute)
	h.zigbee("s1", 21.0)
	h.advance(10 * time.Minute)
	eqCmds(t, h.pub.commands())
	if len(h.events("switch_dropped")) != 1 {
		t.Error("dropped switch not logged")
	}
}

func TestF5AC2Confirmation(t *testing.T) {
	h := newHarness(t, testConfig)
	h.autoReport = false
	h.msg("relay1/status/switch:0", `{"id":0,"output":false}`)
	h.msg("relay2/status/switch:1", `{"id":1,"output":false}`)
	h.startAll(19.0, 20.0, 20.0)
	eqCmds(t, h.pub.commands(), "relay1/command/switch:0 on,900", "relay2/command/switch:1 off")
	h.advance(29 * time.Second)
	eqCmds(t, h.pub.commands())
	h.advance(1500 * time.Millisecond)
	eqCmds(t, h.pub.commands(), "relay1/command/switch:0 on,900")
	if len(h.events("relay_mismatch")) != 1 {
		t.Error("mismatch not logged")
	}
	h.advance(29 * time.Second)
	eqCmds(t, h.pub.commands())
	h.advance(1500 * time.Millisecond)
	eqCmds(t, h.pub.commands(), "relay1/command/switch:0 on,900")
	h.msg("relay1/status/switch:0", `{"id":0,"output":true}`)
	h.advance(5 * time.Minute)
	if n := len(h.events("relay_mismatch")); n != 2 {
		t.Errorf("%d mismatches logged, want 2", n)
	}
	if z := h.zone("ground"); z.Reported != "on" {
		t.Errorf("reported %s", z.Reported)
	}
}

func TestF5AC4Offline(t *testing.T) {
	h := newHarness(t, testConfig)
	h.startAll(19.0, 20.0, 20.0)
	h.msg("relay1/online", "false")
	if !h.zone("ground").Degraded {
		t.Error("offline relay does not degrade its zone")
	}
	h.advance(time.Minute)
	if ev := h.events("control_cycle"); ev[len(ev)-1]["zones"].([]any)[0].(map[string]any)["degraded"] != true {
		t.Error("control_cycle does not show the zone degraded")
	}
	h.pub.commands()
	h.msg("relay1/online", "true")
	if h.zone("ground").Degraded {
		t.Error("still degraded once online")
	}
	eqCmds(t, h.pub.commands(), "relay1/command/switch:0 on,900") // lost commands re-sent
}

func TestF5AC5LeaseRefreshed(t *testing.T) {
	h := newHarness(t, testConfig)
	h.startAll(19.0, 20.0, 20.0)
	h.pub.commands()
	last := h.now
	var gaps []time.Duration
	for i := 0; i < 4*60*60; i++ { // 4 hours
		h.advance(time.Second)
		if i%60 == 0 {
			h.zigbee("s1", 19.0)
		}
		for _, c := range h.pub.commands() {
			if c == "relay1/command/switch:0 on,900" {
				gaps = append(gaps, h.now.Sub(last))
				last = h.now
			} else if strings.HasPrefix(c, "relay1") {
				t.Fatalf("unexpected %s", c)
			}
		}
	}
	if len(gaps) < 10 {
		t.Fatalf("only %d refreshes", len(gaps))
	}
	for _, g := range gaps {
		if g >= 15*time.Minute-time.Minute {
			t.Errorf("refresh gap %s too close to the 15m lease", g)
		}
	}
}

func TestF6AC3StartupGrace(t *testing.T) {
	h := newHarness(t, testConfig)
	h.zigbee("s1", 19.0)
	h.zigbee("s2", 19.0)
	h.advance(2*time.Minute - time.Second)
	if h.c.Started() || len(h.events("control_cycle")) != 0 || len(h.pub.commands()) != 0 {
		t.Fatal("control cycle or command during startup_grace")
	}
	// Status requests are allowed during the start.
	h.advance(time.Second)
	if !h.c.Started() || len(h.events("control_cycle")) != 1 {
		t.Fatal("did not start after startup_grace")
	}
	eqCmds(t, h.pub.commands(), "relay1/command/switch:0 on,900", "relay2/command/switch:1 off")
	if !h.zone("upstairs").Degraded {
		t.Error("upstairs without temperature not degraded")
	}
}

func TestF6AC3EarlyStart(t *testing.T) {
	h := newHarness(t, testConfig)
	h.advance(10 * time.Second)
	h.zigbee("s1", 19.0)
	h.zigbee("s2", 19.0)
	if h.c.Started() {
		t.Fatal("started before bedroom has a temperature")
	}
	h.zigbee("s3", 19.0)
	if !h.c.Started() {
		t.Fatal("not started once every room has a temperature")
	}
	if got := h.events("startup_complete")[0]["reason"]; got != "all_rooms_have_temperature" {
		t.Errorf("reason %v", got)
	}
}

func TestF6AC6RestartDecidesFromScratch(t *testing.T) {
	// The relay is on from before the restart.
	h := newHarness(t, testConfig)
	h.msg("relay1/status/switch:0", `{"output":true}`)
	h.startAll(19.0, 19.0, 20.0)
	eqCmds(t, h.pub.commands(), "relay1/command/switch:0 on,900", "relay2/command/switch:1 off")

	h = newHarness(t, testConfig)
	h.msg("relay1/status/switch:0", `{"output":true}`)
	h.startAll(20.1, 20.1, 20.0)
	eqCmds(t, h.pub.commands(), "relay1/command/switch:0 off", "relay2/command/switch:1 off")
}

func TestF6AC7RetainedReadingIgnored(t *testing.T) {
	h := newHarness(t, testConfig)
	h.c.HandleMessage(h.now, "zigbee2mqtt/s1", []byte(`{"temperature":15}`), true)
	h.c.HandleMessage(h.now, "ht1/status/temperature:0", []byte(`{"tC":15}`), true)
	if h.room("living").Temperature != nil {
		t.Fatal("retained reading used")
	}
	h.zigbee("s1", 21.0)
	if got := temp(t, h.room("living")); got != 21 {
		t.Errorf("temperature %v", got)
	}
}

func TestF8AC1OneEventPerCycle(t *testing.T) {
	h := newHarness(t, testConfig)
	h.startAll(19.0, 20.0, 20.0)
	h.advance(10 * time.Minute) // 10 periodic cycles
	h.zigbee("s1", 19.2)        // 1 reading cycle
	ev := h.events("control_cycle")
	if len(ev) != 1+10+1 { // first cycle, periodic cycles, reading cycle
		t.Errorf("%d control_cycle events, want 12", len(ev))
	}
	e := ev[len(ev)-1]
	if e["schema"] != 1.0 || e["unit"] != "C" {
		t.Errorf("event: %v", e)
	}
	if _, err := time.Parse(time.RFC3339, e["ts"].(string)); err != nil || !strings.HasSuffix(e["ts"].(string), "Z") {
		t.Errorf("ts %v", e["ts"])
	}
	rooms := e["rooms"].([]any)
	living := rooms[0].(map[string]any)
	if living["id"] != "living" || living["temperature"] != 19.2 || living["target"] != 20.0 || living["demand"] != true {
		t.Errorf("living: %v", living)
	}
	if r := living["readings"].([]any); len(r) != 1 || r[0] != 19.2 {
		t.Errorf("readings %v", r)
	}
	hall := rooms[2].(map[string]any)
	if v, ok := hall["temperature"]; !ok || v != nil {
		t.Errorf("hallway temperature must be null: %v", hall)
	}
	if v, ok := hall["target"]; !ok || v != nil {
		t.Errorf("hallway target must be null: %v", hall)
	}
	z := e["zones"].([]any)[0].(map[string]any)
	if z["id"] != "ground" || z["desired"] != "on" || z["reported"] != "on" || z["degraded"] != false {
		t.Errorf("zone: %v", z)
	}
}

func TestBrokerReconnectResends(t *testing.T) {
	h := newHarness(t, testConfig)
	h.startAll(19.0, 20.0, 20.0)
	h.pub.commands()
	h.c.OnDisconnected(h.now)
	h.pub.down = true
	h.advance(3 * time.Minute)
	h.pub.down = false
	h.c.OnConnected(h.now)
	got := h.pub.msgs
	var cmds []string
	reqs := 0
	for _, m := range got {
		if m.payload == "status_update" {
			reqs++
		} else {
			cmds = append(cmds, m.topic+" "+m.payload)
		}
	}
	h.pub.msgs = nil
	if reqs != 2 {
		t.Errorf("%d status requests, want 2", reqs)
	}
	eqCmds(t, cmds, "relay1/command/switch:0 on,900", "relay2/command/switch:1 off")
}

func TestShutdownTurnsAllOff(t *testing.T) {
	h := newHarness(t, testConfig)
	h.startAll(19.0, 19.0, 19.0)
	h.pub.commands()
	h.advance(10 * time.Second) // well within min_switch_interval
	h.c.Shutdown(h.now)
	eqCmds(t, h.pub.commands(), "relay1/command/switch:0 off", "relay2/command/switch:1 off")
}

func TestUnknownTopicIgnored(t *testing.T) {
	h := newHarness(t, testConfig)
	h.msg("something/else", "{}")
	h.msg("relay1/status/switch:0", "garbage")
	h.msg("relay1/online", "maybe")
	if len(h.events("topic_ignored")) != 1 || len(h.events("payload_invalid")) != 2 {
		t.Errorf("log:\n%s", h.log)
	}
}
