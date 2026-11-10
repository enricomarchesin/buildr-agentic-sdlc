package control

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"heatctl/internal/config"
)

type fakePub struct{ msgs []string }

func (f *fakePub) Publish(t, p string) { f.msgs = append(f.msgs, t+" "+p) }
func (f *fakePub) take() []string      { m := f.msgs; f.msgs = nil; return m }

type nopLog struct{ cycles int }

func (n *nopLog) Emit(any)           { n.cycles++ }
func (n *nopLog) Log(string, ...any) {}

const yml = `
unit: %s
mqtt: {broker: "tcp://x:1883"}
zones:
  - id: z
    relay: {device: r1, switch: 0}
    rooms:
      - id: a
        target: %s
        sensors: [{zigbee: s1}]
      - id: b
        sensors: [{device: h1, component: 0}]
        target: %s
`

func setup(t *testing.T, unit, target string) (*Controller, *fakePub, *nopLog, time.Time) {
	cfg, errs := config.Parse([]byte(fmt.Sprintf(yml, unit, target, target)))
	if errs != nil {
		t.Fatal(errs)
	}
	f, l := &fakePub{}, &nopLog{}
	t0 := time.Date(2026, 11, 4, 12, 0, 0, 0, time.Local)
	return New(cfg, f, l, t0), f, l, t0
}

func zig(c *Controller, now time.Time, v float64) {
	c.HandleMessage(now, "zigbee2mqtt/s1", []byte(fmt.Sprintf(`{"temperature":%v}`, v)), false)
}
func shelly(c *Controller, now time.Time, v float64) {
	c.HandleMessage(now, "h1/status/temperature:0", []byte(fmt.Sprintf(`{"id":0,"tC":%v}`, v)), false)
}

func TestStartGraceAndHysteresis(t *testing.T) {
	c, f, l, t0 := setup(t, "C", "20")
	zig(c, t0, 19.4)
	if l.cycles != 0 || len(f.msgs) != 0 {
		t.Fatal("must wait for every room with sensors")
	}
	shelly(c, t0.Add(time.Second), 22) // room b target 21, no demand
	if l.cycles != 1 {
		t.Fatalf("cycles=%d", l.cycles)
	}
	if m := f.take(); len(m) != 1 || m[0] != "r1/command/switch:0 on,900" {
		t.Fatalf("%v", m)
	}
	now := t0.Add(2 * time.Second)
	zig(c, now, 20.4) // stays on
	c.Tick(now.Add(time.Second))
	if !c.zones[0].desired {
		t.Fatal("should stay on at 20.4")
	}
	zig(c, now, 20.5)
	if c.zones[0].desired {
		t.Fatal("should want off at 20.5")
	}
	// min switch interval: not yet commanded
	if m := f.take(); len(m) != 0 {
		t.Fatalf("switched too soon: %v", m)
	}
	c.Tick(now.Add(6 * time.Minute))
	found := false
	for _, m := range f.take() {
		if m == "r1/command/switch:0 off" {
			found = true
		}
	}
	if !found {
		t.Fatal("off not sent after interval")
	}
}

func TestGraceTimeoutAndInBandStartsOff(t *testing.T) {
	c, f, _, t0 := setup(t, "C", "20")
	zig(c, t0, 20.1)
	c.Tick(t0.Add(3 * time.Minute))
	m := f.take()
	if len(m) != 1 || m[0] != "r1/command/switch:0 off" {
		t.Fatalf("%v", m)
	}
}

func TestStaleDegraded(t *testing.T) {
	c, f, _, t0 := setup(t, "C", "20")
	zig(c, t0, 19)
	c.Tick(t0.Add(3 * time.Minute))
	f.take()
	later := t0.Add(3*time.Minute + 50*time.Minute)
	c.Cycle(later)
	if !c.zones[0].degraded || c.zones[0].desired {
		t.Fatal("expected degraded and off")
	}
}

func TestFahrenheit(t *testing.T) {
	c, _, _, t0 := setup(t, "F", "68")
	zig(c, t0, 19.4)
	c.Tick(t0.Add(3 * time.Minute))
	if !c.zones[0].desired {
		t.Fatal("19.4C should turn on")
	}
	zig(c, t0.Add(4*time.Minute), 20.5)
	if !c.zones[0].desired {
		t.Fatal("20.5C keeps on")
	}
	zig(c, t0.Add(4*time.Minute), 20.6)
	if c.zones[0].desired {
		t.Fatal("20.6C turns off")
	}
}

func TestBadInputAndRetained(t *testing.T) {
	c, _, l, t0 := setup(t, "C", "20")
	c.HandleMessage(t0, "zigbee2mqtt/s1", []byte(`{"temperature":19}`), true)
	c.HandleMessage(t0, "zigbee2mqtt/s1", []byte(`{"temperature":"x"}`), false)
	c.HandleMessage(t0, "zigbee2mqtt/s1", []byte(`{"temperature":99}`), false)
	c.HandleMessage(t0, "zigbee2mqtt/s1", []byte(`garbage`), false)
	c.HandleMessage(t0, "h1/status/temperature:0", []byte(`{"tC":20,"errors":["read"]}`), false)
	c.HandleMessage(t0, "h1/status/temperature:0", []byte(`{"tC":null}`), false)
	c.Cycle(t0)
	if c.rooms[0].temp != nil || c.rooms[1].temp != nil || l.cycles != 1 {
		t.Fatal("bad readings must not count")
	}
}

func TestConfirmAndLease(t *testing.T) {
	c, f, _, t0 := setup(t, "C", "20")
	zig(c, t0, 19)
	c.Tick(t0.Add(3 * time.Minute))
	f.take()
	now := t0.Add(3*time.Minute + 31*time.Second)
	c.Tick(now)
	if m := f.take(); len(m) != 1 {
		t.Fatalf("expected confirm resend, got %v", m)
	}
	c.HandleMessage(now, "r1/status/switch:0", []byte(`{"output":true}`), false)
	now = now.Add(5*time.Minute + time.Second)
	zig(c, now, 19)
	c.Tick(now)
	m := strings.Join(c.pubMsgs(f), ",")
	if !strings.Contains(m, "on,900") {
		t.Fatalf("lease refresh missing: %v", m)
	}
}

func (c *Controller) pubMsgs(f *fakePub) []string { return f.take() }

func TestOfflineDegraded(t *testing.T) {
	c, _, _, t0 := setup(t, "C", "20")
	zig(c, t0, 19)
	c.Tick(t0.Add(3 * time.Minute))
	c.HandleMessage(t0, "r1/online", []byte("false"), false)
	c.Cycle(t0.Add(4 * time.Minute))
	if !c.zones[0].degraded {
		t.Fatal("offline relay must degrade zone")
	}
}

func TestScheduleTarget(t *testing.T) {
	cfg, errs := config.Parse([]byte(`
unit: C
mqtt: {broker: "tcp://x:1"}
zones:
  - id: z
    relay: {device: r, switch: 0}
    rooms:
      - id: l
        sensors: [{zigbee: a}]
        schedule:
          - { from: "06:30", target: 21.0, days: [mon, tue, wed, thu, fri] }
          - { from: "08:00", target: 21.0, days: [sat, sun] }
          - { from: "22:30", target: 18.0 }
      - id: s
        sensors: [{zigbee: b}]
        schedule:
          - { from: "10:00", target: 20.0, days: [sun] }
`))
	if errs != nil {
		t.Fatal(errs)
	}
	at := func(day int, h, m int) time.Time { return time.Date(2026, 11, day, h, m, 0, 0, time.Local) } // Nov 2 2026 = Monday
	cases := []struct {
		t time.Time
		w float64
	}{{at(4, 7, 0), 21}, {at(7, 7, 0), 18}, {at(7, 9, 0), 21}, {at(2, 5, 0), 18}}
	for _, k := range cases {
		if g, _ := ScheduleTarget(cfg.Zones[0].Rooms[0].Schedule, k.t); g != k.w {
			t.Errorf("%v: got %v want %v", k.t, g, k.w)
		}
	}
	for _, d := range []int{2, 5, 8} {
		if g, ok := ScheduleTarget(cfg.Zones[0].Rooms[1].Schedule, at(d, 3, 0)); !ok || g != 20 {
			t.Errorf("sun-only day %d: %v", d, g)
		}
	}
}
