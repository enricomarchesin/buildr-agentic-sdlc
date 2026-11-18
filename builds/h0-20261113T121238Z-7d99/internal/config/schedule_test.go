package config

import (
	"testing"
	"time"
)

func mustRoom(t *testing.T, src string) Room {
	t.Helper()
	cfg, errs := Parse([]byte(src))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	return cfg.Zones[0].Rooms[0]
}

func roomWithSchedule(sched string) string {
	return `unit: C
mqtt: { broker: "tcp://b:1883" }
zones:
  - id: z
    relay: { device: r, switch: 0 }
    rooms:
      - id: room
        sensors: [{ zigbee: s }]
        schedule:
` + sched
}

// 2026-11-02 is a Monday.
func at(day, hour, min int) time.Time {
	return time.Date(2026, 11, day, hour, min, 0, 0, time.Local)
}

func check(t *testing.T, r Room, when time.Time, want float64) {
	t.Helper()
	got, ok := r.TargetAt(when)
	if !ok || got != want {
		t.Errorf("%s %s: target %v (%v), want %v", when.Weekday(), when.Format("15:04"), got, ok, want)
	}
}

func TestF3AC1(t *testing.T) {
	r := mustRoom(t, roomWithSchedule(`          - { from: "06:30", target: 21.0 }
          - { from: "22:30", target: 18.0 }
`))
	check(t, r, at(4, 12, 0), 21)
	check(t, r, at(4, 23, 0), 18)
	check(t, r, at(4, 3, 0), 18)
	check(t, r, at(4, 6, 30), 21)
	check(t, r, at(4, 6, 29), 18)
}

func TestF3AC2FixedTarget(t *testing.T) {
	cfg, errs := Parse([]byte(base))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	r := cfg.Zones[1].Rooms[0]
	for h := 0; h < 24; h++ {
		check(t, r, at(4, h, 17), 19)
	}
}

const exampleSchedule = `          - { from: "06:30", target: 21.0, days: [mon, tue, wed, thu, fri] }
          - { from: "08:00", target: 21.0, days: [sat, sun] }
          - { from: "22:30", target: 18.0 }
`

func TestF3AC3AC4(t *testing.T) {
	r := mustRoom(t, roomWithSchedule(exampleSchedule))
	check(t, r, at(4, 7, 0), 21)  // Wednesday 07:00
	check(t, r, at(7, 7, 0), 18)  // Saturday 07:00
	check(t, r, at(7, 9, 0), 21)  // Saturday 09:00
	check(t, r, at(2, 5, 0), 18)  // Monday 05:00, from Sunday 22:30
	check(t, r, at(8, 23, 0), 18) // Sunday 23:00
}

func TestF3AC5SingleWeeklyEntry(t *testing.T) {
	r := mustRoom(t, roomWithSchedule(`          - { from: "10:00", target: 20.0, days: [sun] }
`))
	for d := 2; d <= 8; d++ {
		for h := 0; h < 24; h++ {
			check(t, r, at(d, h, 0), 20)
		}
	}
	check(t, r, at(8, 9, 59), 20) // Sunday just before: last week's entry
}

func TestNoTarget(t *testing.T) {
	cfg, _ := Parse([]byte(base))
	if _, ok := cfg.Zones[0].Rooms[1].TargetAt(at(4, 12, 0)); ok {
		t.Error("hallway has a target")
	}
}
