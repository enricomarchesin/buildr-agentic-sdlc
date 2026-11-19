package config

import (
	"strings"
	"testing"
	"time"
)

const baseYAML = `
unit: C

mqtt:
  broker: tcp://127.0.0.1:1883

zones:
  - id: ground
    relay: { device: shellyplus1-a8032ab1c2d3, switch: 0 }
    rooms:
      - id: living
        schedule:
          - { from: "06:30", target: 21.0, days: [mon, tue, wed, thu, fri] }
          - { from: "08:00", target: 21.0, days: [sat, sun] }
          - { from: "22:30", target: 18.0 }
        sensors:
          - { device: shellyhtg3-84fce63a1b2c, component: 0 }
          - { zigbee: living-display }
      - id: hallway
  - id: upstairs
    relay: { device: shellypro4pm-f008d1d8b8b8, switch: 2 }
    rooms:
      - id: bedroom
        target: 19.0
        sensors:
          - { zigbee: bedroom-display }
`

func mustParse(t *testing.T, yaml string) *Config {
	t.Helper()
	cfg, errs := Parse([]byte(yaml), "test.yaml")
	if len(errs) > 0 {
		t.Fatalf("unexpected errors:\n%s", Diagnostics(errs))
	}
	return cfg
}

func errsOf(t *testing.T, yaml string) []string {
	t.Helper()
	_, errs := Parse([]byte(yaml), "test.yaml")
	out := make([]string, len(errs))
	for i, e := range errs {
		out[i] = e.Error()
	}
	return out
}

func patch(t *testing.T, old, new string) string {
	t.Helper()
	if !strings.Contains(baseYAML, old) {
		t.Fatalf("base config does not contain %q", old)
	}
	return strings.Replace(baseYAML, old, new, 1)
}

func wantErr(t *testing.T, yaml string, wants ...string) {
	t.Helper()
	errs := errsOf(t, yaml)
	if len(errs) == 0 {
		t.Fatalf("expected errors containing %q, got none", wants)
	}
	for _, want := range wants {
		found := false
		for _, err := range errs {
			if strings.Contains(err, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("error %q not found in:\n%s", want, strings.Join(errs, "\n"))
		}
	}
}

func TestParseSpecExample(t *testing.T) {
	cfgYAML := `
unit: C

mqtt:
  broker: tcp://192.168.1.10:1883
  client_id: heatctl
  username: ""
  password: ""
  zigbee2mqtt_topic: zigbee2mqtt

control:
  cycle_interval: 60s
  hysteresis: 0.5
  stale_after: 45m
  min_switch_interval: 5m
  relay_lease: 15m
  startup_grace: 2m
  confirm_timeout: 30s

zones:
  - id: ground
    relay:
      device: shellyplus1-a8032ab1c2d3
      switch: 0
    rooms:
      - id: living
        schedule:
          - { from: "06:30", target: 21.0, days: [mon, tue, wed, thu, fri] }
          - { from: "08:00", target: 21.0, days: [sat, sun] }
          - { from: "22:30", target: 18.0 }
        sensors:
          - { device: shellyhtg3-84fce63a1b2c, component: 0 }
          - { zigbee: living-display }
      - id: hallway
  - id: upstairs
    relay:
      device: shellypro4pm-f008d1d8b8b8
      switch: 2
    rooms:
      - id: bedroom
        target: 19.0
        sensors:
          - { zigbee: bedroom-display }
`
	cfg := mustParse(t, cfgYAML)
	if cfg.Unit != Celsius {
		t.Fatalf("unit = %q, want C", cfg.Unit)
	}
	if cfg.MQTT.Broker != "tcp://192.168.1.10:1883" {
		t.Fatalf("broker = %q", cfg.MQTT.Broker)
	}
	if cfg.MQTT.ClientID != "heatctl" || cfg.MQTT.Zigbee2MQTTTopic != "zigbee2mqtt" {
		t.Fatalf("mqtt defaults = %+v", cfg.MQTT)
	}
	if cfg.Control.CycleInterval != 60*time.Second || cfg.Control.Hysteresis != 0.5 ||
		cfg.Control.StaleAfter != 45*time.Minute || cfg.Control.MinSwitchInterval != 5*time.Minute ||
		cfg.Control.RelayLease != 15*time.Minute || cfg.Control.StartupGrace != 2*time.Minute ||
		cfg.Control.ConfirmTimeout != 30*time.Second {
		t.Fatalf("control = %+v", cfg.Control)
	}
	if len(cfg.Zones) != 2 {
		t.Fatalf("zones = %d", len(cfg.Zones))
	}
	if got := cfg.Zones[0].Relay; got.Device != "shellyplus1-a8032ab1c2d3" || got.Switch != 0 {
		t.Fatalf("relay = %+v", got)
	}
	if got := cfg.Zones[0].Relay.StatusTopic(); got != "shellyplus1-a8032ab1c2d3/status/switch:0" {
		t.Fatalf("status topic = %q", got)
	}
	if got := cfg.Zones[0].Relay.CommandTopic(); got != "shellyplus1-a8032ab1c2d3/command/switch:0" {
		t.Fatalf("command topic = %q", got)
	}
	if len(cfg.Zones[0].Rooms) != 2 || cfg.Zones[0].Rooms[1].ID != "hallway" {
		t.Fatalf("rooms = %+v", cfg.Zones[0].Rooms)
	}
	living := cfg.Zones[0].Rooms[0]
	if living.ZoneID != "ground" {
		t.Fatalf("room zone = %q", living.ZoneID)
	}
	if len(living.Sensors) != 2 {
		t.Fatalf("sensors = %+v", living.Sensors)
	}
	if got := living.Sensors[0].Topic(cfg.MQTT.Zigbee2MQTTTopic); got != "shellyhtg3-84fce63a1b2c/status/temperature:0" {
		t.Fatalf("shelly topic = %q", got)
	}
	if got := living.Sensors[1].Topic(cfg.MQTT.Zigbee2MQTTTopic); got != "zigbee2mqtt/living-display" {
		t.Fatalf("zigbee topic = %q", got)
	}
	if living.Target != nil {
		t.Fatalf("living target should be nil")
	}
	if len(living.Schedule) != 3 {
		t.Fatalf("schedule = %+v", living.Schedule)
	}
	if living.Schedule[0].From != 6*60+30 || living.Schedule[0].Target != 21 ||
		living.Schedule[0].Days != Monday|Tuesday|Wednesday|Thursday|Friday {
		t.Fatalf("entry 0 = %+v", living.Schedule[0])
	}
	if living.Schedule[2].Days != 0 {
		t.Fatalf("entry 2 days = %v", living.Schedule[2].Days)
	}
	bedroom := cfg.Zones[1].Rooms[0]
	if bedroom.Target == nil || *bedroom.Target != 19.0 {
		t.Fatalf("bedroom target = %v", bedroom.Target)
	}
}

func TestDefaults(t *testing.T) {
	cfg := mustParse(t, baseYAML)
	if cfg.Control.CycleInterval != DefaultCycleInterval ||
		cfg.Control.StaleAfter != DefaultStaleAfter ||
		cfg.Control.MinSwitchInterval != DefaultMinSwitchInterval ||
		cfg.Control.RelayLease != DefaultRelayLease ||
		cfg.Control.StartupGrace != DefaultStartupGrace ||
		cfg.Control.ConfirmTimeout != DefaultConfirmTimeout {
		t.Fatalf("control defaults = %+v", cfg.Control)
	}
	if cfg.Control.Hysteresis != 0.5 {
		t.Fatalf("C hysteresis = %v, want 0.5", cfg.Control.Hysteresis)
	}
	f := mustParse(t, `
unit: F
mqtt: { broker: tcp://127.0.0.1:1883 }
zones:
  - id: z
    relay: { device: d, switch: 0 }
    rooms:
      - id: r
        target: 68
        sensors: [{ zigbee: s }]
`)
	if f.Control.Hysteresis != 1.0 {
		t.Fatalf("F hysteresis = %v, want 1.0", f.Control.Hysteresis)
	}
	if f.Control.CycleInterval != 60*time.Second {
		t.Fatalf("F cycle = %v", f.Control.CycleInterval)
	}
}

func loc() *time.Location { return time.FixedZone("CET", 3600) }

func at(t *testing.T, y, mo, d, h, mi int) time.Time {
	t.Helper()
	return time.Date(y, time.Month(mo), d, h, mi, 0, 0, loc())
}

func TestScheduleTargets(t *testing.T) {
	cfg := mustParse(t, baseYAML)
	living := cfg.Zones[0].Rooms[0]

	cases := []struct {
		name string
		now  time.Time
		want float64
	}{
		// F3.AC1: 06:30 -> 21.0, 22:30 -> 18.0 every day.
		{"midday", at(t, 2026, 11, 4, 12, 0), 21.0},
		{"late", at(t, 2026, 11, 4, 23, 0), 18.0},
		{"early", at(t, 2026, 11, 4, 3, 0), 18.0},
		// F3.AC3 / F3.AC4.
		{"wednesday 07:00", at(t, 2026, 11, 4, 7, 0), 21.0},
		{"saturday 07:00", at(t, 2026, 11, 7, 7, 0), 18.0},
		{"saturday 09:00", at(t, 2026, 11, 7, 9, 0), 21.0},
		{"monday 05:00", at(t, 2026, 11, 2, 5, 0), 18.0},
		{"exactly at 06:30", at(t, 2026, 11, 4, 6, 30), 21.0},
		{"one minute before 06:30", at(t, 2026, 11, 4, 6, 29), 18.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := living.TargetAt(tc.now, loc()); !ok || got != tc.want {
				t.Fatalf("TargetAt(%v) = %v, %v; want %v", tc.now, got, ok, tc.want)
			}
		})
	}
}

func TestScheduleEveryDayFallback(t *testing.T) {
	// F3.AC5: the only entry is Sunday 10:00.
	cfg := mustParse(t, `
unit: C
mqtt: { broker: tcp://127.0.0.1:1883 }
zones:
  - id: z
    relay: { device: d, switch: 0 }
    rooms:
      - id: r
        schedule:
          - { from: "10:00", target: 20.0, days: [sun] }
        sensors: [{ zigbee: s }]
`)
	living := cfg.Zones[0].Rooms[0]
	for _, now := range []time.Time{
		at(t, 2026, 11, 2, 9, 0),  // Monday before 10:00 -> last Sunday 10:00
		at(t, 2026, 11, 2, 11, 0), // Monday after 10:00 -> last Sunday 10:00
		at(t, 2026, 11, 8, 9, 0),  // Sunday before 10:00 -> previous Sunday
		at(t, 2026, 11, 8, 10, 0), // Sunday exactly 10:00
		at(t, 2026, 11, 8, 23, 0), // Sunday evening
	} {
		if got, ok := living.TargetAt(now, loc()); !ok || got != 20.0 {
			t.Fatalf("TargetAt(%v) = %v, %v; want 20.0", now, got, ok)
		}
	}
}

func TestFixedTargetAllDay(t *testing.T) {
	cfg := mustParse(t, baseYAML)
	bedroom := cfg.Zones[1].Rooms[0]
	for h := 0; h < 24; h++ {
		now := at(t, 2026, 11, 4, h, 15)
		if got, ok := bedroom.TargetAt(now, loc()); !ok || got != 19.0 {
			t.Fatalf("TargetAt(%v) = %v, %v; want 19.0", now, got, ok)
		}
	}
}

func TestNoTarget(t *testing.T) {
	cfg := mustParse(t, baseYAML)
	hallway := cfg.Zones[0].Rooms[1]
	if _, ok := hallway.TargetAt(at(t, 2026, 11, 4, 12, 0), loc()); ok {
		t.Fatal("hallway should have no target")
	}
}

func TestValidationErrors(t *testing.T) {
	cases := []struct {
		name  string
		yaml  string
		wants []string
	}{
		{"unit missing", strings.Replace(baseYAML, "unit: C", "", 1), []string{"unit: is required"}},
		{"unit invalid", strings.Replace(baseYAML, "unit: C", "unit: K", 1), []string{`unit: must be "C" or "F"`}},
		{"broker missing", strings.Replace(baseYAML, "  broker: tcp://127.0.0.1:1883\n", "", 1), []string{"mqtt.broker: is required"}},
		{"broker empty", strings.Replace(baseYAML, "tcp://127.0.0.1:1883", `""`, 1), []string{"mqtt.broker: must not be empty"}},
		{"broker no scheme", strings.Replace(baseYAML, "tcp://127.0.0.1:1883", "127.0.0.1:1883", 1), []string{"mqtt.broker: must include a scheme"}},
		{"broker bad scheme", strings.Replace(baseYAML, "tcp://", "ftp://", 1), []string{`mqtt.broker: unsupported scheme "ftp"`}},
		{"client id empty", strings.Replace(baseYAML, "  broker: tcp://127.0.0.1:1883", "  broker: tcp://127.0.0.1:1883\n  client_id: \"\"", 1), []string{"mqtt.client_id: must not be empty"}},
		{"zigbee wildcard", strings.Replace(baseYAML, "  broker: tcp://127.0.0.1:1883", "  broker: tcp://127.0.0.1:1883\n  zigbee2mqtt_topic: \"zigbee/+\"", 1), []string{"mqtt.zigbee2mqtt_topic: must not contain"}},
		{"cycle zero", strings.Replace(baseYAML, "zones:", "control:\n  cycle_interval: 0s\nzones:", 1), []string{"control.cycle_interval: must be greater than zero"}},
		{"cycle negative", strings.Replace(baseYAML, "zones:", "control:\n  cycle_interval: -60s\nzones:", 1), []string{"control.cycle_interval: must be greater than zero"}},
		{"cycle not a duration", strings.Replace(baseYAML, "zones:", "control:\n  cycle_interval: 60\nzones:", 1), []string{"control.cycle_interval: must be a Go duration"}},
		{"hysteresis negative", strings.Replace(baseYAML, "zones:", "control:\n  hysteresis: -0.5\nzones:", 1), []string{"control.hysteresis: must be 0 or more"}},
		{"relay lease too short", strings.Replace(baseYAML, "zones:", "control:\n  cycle_interval: 60s\n  relay_lease: 60s\nzones:", 1), []string{"control.relay_lease: must be longer than control.cycle_interval"}},
		{"stale after zero", strings.Replace(baseYAML, "zones:", "control:\n  stale_after: 0s\nzones:", 1), []string{"control.stale_after: must be greater than zero"}},
		{"zones missing", "unit: C\nmqtt:\n  broker: tcp://127.0.0.1:1883\n", []string{"zones: is required"}},
		{"zones empty", "unit: C\nmqtt:\n  broker: tcp://127.0.0.1:1883\nzones: []\n", []string{"zones: at least one zone is required"}},
		{"zone without relay", strings.Replace(baseYAML, "    relay: { device: shellyplus1-a8032ab1c2d3, switch: 0 }\n", "", 1), []string{"zones[0].relay.device: is required", "zones[0].relay.switch: is required"}},
		{"zone without switch", strings.Replace(baseYAML, "relay: { device: shellyplus1-a8032ab1c2d3, switch: 0 }", "relay: { device: shellyplus1-a8032ab1c2d3 }", 1), []string{"zones[0].relay.switch: is required"}},
		{"negative switch", strings.Replace(baseYAML, "switch: 0 }", "switch: -1 }", 1), []string{"zones[0].relay.switch: must be 0 or more"}},
		{"switch not an integer", strings.Replace(baseYAML, "switch: 0 }", "switch: zero }", 1), []string{"zones[0].relay.switch: must be an integer"}},
		{"duplicate zone id", strings.Replace(baseYAML, "  - id: upstairs", "  - id: ground", 1), []string{`zones[1].id: duplicate zone id "ground"`}},
		{"duplicate relay", strings.Replace(baseYAML, "device: shellypro4pm-f008d1d8b8b8, switch: 2", "device: shellyplus1-a8032ab1c2d3, switch: 0", 1), []string{"zones[1].relay: relay shellyplus1-a8032ab1c2d3/0 is already used"}},
		{"zone without rooms", strings.Replace(baseYAML, `  - id: upstairs
    relay:
`, `  - id: upstairs
    relay:
`, 1), nil},
		{"empty rooms", strings.Replace(baseYAML, `    rooms:
      - id: bedroom
        target: 19.0
        sensors:
          - { zigbee: bedroom-display }`, `    rooms: []`, 1), []string{"zones[1].rooms: one or more rooms are required"}},
		{"duplicate room id", strings.Replace(baseYAML, "      - id: bedroom", "      - id: living", 1), []string{`duplicate room id "living"`}},
		{"room with sensors and no target", strings.Replace(baseYAML, `        target: 19.0
`, "", 1), []string{"a room with sensors needs either target or schedule"}},
		{"target and schedule", strings.Replace(baseYAML, `        schedule:
          - { from: "06:30", target: 21.0, days: [mon, tue, wed, thu, fri] }
          - { from: "08:00", target: 21.0, days: [sat, sun] }
          - { from: "22:30", target: 18.0 }`, `        target: 20.0
        schedule:
          - { from: "06:30", target: 21.0 }`, 1), []string{"target and schedule cannot both be set"}},
		{"target too high", strings.Replace(baseYAML, "target: 19.0", "target: 30.0", 1), []string{"zones[1].rooms[0].target: must be between 7 and 25"}},
		{"target too low", strings.Replace(baseYAML, "target: 19.0", "target: 6.9", 1), []string{"must be between 7 and 25"}},
		{"schedule target too high", strings.Replace(baseYAML, "target: 21.0, days", "target: 25.1, days", 1), []string{"zones[0].rooms[0].schedule[0].target: must be between 7 and 25"}},
		{"schedule empty", strings.Replace(baseYAML, `        schedule:
          - { from: "06:30", target: 21.0, days: [mon, tue, wed, thu, fri] }
          - { from: "08:00", target: 21.0, days: [sat, sun] }
          - { from: "22:30", target: 18.0 }`, "        schedule: []", 1), []string{"zones[0].rooms[0].schedule: must have at least one entry"}},
		{"schedule missing from", strings.Replace(baseYAML, `{ from: "22:30", target: 18.0 }`, `{ target: 18.0 }`, 1), []string{"schedule[2].from: is required"}},
		{"schedule bad from", strings.Replace(baseYAML, `"06:30"`, `"25:00"`, 1), []string{"schedule[0].from: must be HH:MM"}},
		{"schedule short from", strings.Replace(baseYAML, `"06:30"`, `"6:30"`, 1), []string{"must be HH:MM"}},
		{"schedule missing target", strings.Replace(baseYAML, `{ from: "22:30", target: 18.0 }`, `{ from: "22:30" }`, 1), []string{"schedule[2].target: is required"}},
		{"days empty", strings.Replace(baseYAML, "days: [mon, tue, wed, thu, fri]", "days: []", 1), []string{"days: must not be empty"}},
		{"day unknown", strings.Replace(baseYAML, "days: [mon, tue, wed, thu, fri]", "days: [mon, funday]", 1), []string{`unknown day "funday"`}},
		{"day duplicate", strings.Replace(baseYAML, "days: [mon, tue, wed, thu, fri]", "days: [mon, mon]", 1), []string{`duplicate day "mon"`}},
		{"days not a list", strings.Replace(baseYAML, "days: [mon, tue, wed, thu, fri]", "days: mon", 1), []string{"days: must be a list of day names"}},
		{"overlap every day vs sunday", strings.Replace(baseYAML, `          - { from: "22:30", target: 18.0 }`, `          - { from: "22:30", target: 18.0 }
          - { from: "22:30", target: 17.0, days: [sun] }`, 1), []string{"schedule[3].from: 22:30 is already taken"}},
		{"sensor both kinds", strings.Replace(baseYAML, `{ device: shellyhtg3-84fce63a1b2c, component: 0 }`, `{ device: shellyhtg3-84fce63a1b2c, component: 0, zigbee: living-display }`, 1), []string{"a sensor is either {device, component} or {zigbee}, not both"}},
		{"sensor neither kind", strings.Replace(baseYAML, `{ zigbee: bedroom-display }`, `{}`, 1), []string{"a sensor needs either {device, component} or {zigbee}"}},
		{"sensor missing component", strings.Replace(baseYAML, `{ device: shellyhtg3-84fce63a1b2c, component: 0 }`, `{ device: shellyhtg3-84fce63a1b2c }`, 1), []string{"sensors[0].component: is required"}},
		{"sensor missing device", strings.Replace(baseYAML, `{ device: shellyhtg3-84fce63a1b2c, component: 0 }`, `{ component: 0 }`, 1), []string{"sensors[0].device: is required"}},
		{"sensor negative component", strings.Replace(baseYAML, "component: 0 }", "component: -1 }", 1), []string{"sensors[0].component: must be 0 or more"}},
		{"duplicate sensor", strings.Replace(baseYAML, `{ zigbee: bedroom-display }`, `{ zigbee: living-display }`, 1), []string{`zones[1].rooms[0].sensors[0]: sensor zigbee living-display is already used`}},
		{"unknown top level key", strings.Replace(baseYAML, "unit: C", "unit: C\nfoo: bar", 1), []string{"foo: unknown key"}},
		{"unknown mqtt key", strings.Replace(baseYAML, "  broker:", "  port: 1883\n  broker:", 1), []string{"mqtt.port: unknown key"}},
		{"unknown control key", strings.Replace(baseYAML, "zones:", "control:\n  cooldown: 5m\nzones:", 1), []string{"control.cooldown: unknown key"}},
		{"unknown zone key", strings.Replace(baseYAML, "    relay: { device: shellyplus1-a8032ab1c2d3, switch: 0 }", "    name: Ground floor\n    relay: { device: shellyplus1-a8032ab1c2d3, switch: 0 }", 1), []string{"zones[0].name: unknown key"}},
		{"unknown relay key", strings.Replace(baseYAML, "switch: 0 }", "switch: 0, channel: 1 }", 1), []string{"relay.channel: unknown key"}},
		{"unknown room key", strings.Replace(baseYAML, "      - id: hallway", "      - id: hallway\n        name: Hall", 1), []string{"rooms[1].name: unknown key"}},
		{"unknown sensor key", strings.Replace(baseYAML, "{ zigbee: bedroom-display }", "{ zigbee: bedroom-display, topic: x }", 1), []string{"sensors[0].topic: unknown key"}},
		{"unknown schedule key", strings.Replace(baseYAML, `{ from: "22:30", target: 18.0 }`, `{ from: "22:30", target: 18.0, until: "06:00" }`, 1), []string{"schedule[2].until: unknown key"}},
		{"duplicate key", strings.Replace(baseYAML, "unit: C", "unit: C\nunit: F", 1), []string{"unit: duplicate key"}},
		{"top level not a mapping", "- a\n- b\n", []string{"config: must be a mapping"}},
		{"empty file", "", []string{"config: configuration is empty"}},
		{"bad yaml", "unit: C\n  bad: [\n", []string{"invalid YAML"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wants == nil {
				// "zone without rooms" is covered by a dedicated case below.
				return
			}
			wantErr(t, tc.yaml, tc.wants...)
		})
	}
}

func TestScheduleNullIsRejected(t *testing.T) {
	// F1.AC2: a schedule must have at least one entry. null, ~ and an empty
	// value are a schedule with no entries, not an omitted schedule.
	for _, line := range []string{"schedule: null", "schedule: ~", "schedule:"} {
		yaml := `
unit: C
mqtt: { broker: tcp://127.0.0.1:1883 }
zones:
  - id: z
    relay: { device: d, switch: 0 }
    rooms:
      - id: r
        ` + line + `
        sensors: [{ zigbee: s }]
`
		errs := errsOf(t, yaml)
		if len(errs) != 1 {
			t.Fatalf("%s: errors = %v, want exactly one", line, errs)
		}
		if want := "zones[0].rooms[0].schedule: must have at least one entry"; !strings.Contains(errs[0], want) {
			t.Fatalf("%s: diagnostic = %q, want %q", line, errs[0], want)
		}
	}
}

func TestZoneWithoutRooms(t *testing.T) {
	yaml := `
unit: C
mqtt: { broker: tcp://127.0.0.1:1883 }
zones:
  - id: ground
    relay: { device: dev1, switch: 0 }
`
	wantErr(t, yaml, "zones[0].rooms: one or more rooms are required")
}

func fahrenheitConfig(target string) string {
	return `
unit: F
mqtt: { broker: tcp://127.0.0.1:1883 }
zones:
  - id: z
    relay: { device: d, switch: 0 }
    rooms:
      - id: r
        target: ` + target + `
        sensors: [{ zigbee: s }]
`
}

func TestUnitFahrenheitRanges(t *testing.T) {
	wantErr(t, fahrenheitConfig("30"), "zones[0].rooms[0].target: must be between 45 and 77 °F, got 30")
	wantErr(t, fahrenheitConfig("80"), "zones[0].rooms[0].target: must be between 45 and 77 °F, got 80")

	cfg := mustParse(t, fahrenheitConfig("68"))
	if cfg.Zones[0].Rooms[0].Target == nil || *cfg.Zones[0].Rooms[0].Target != 68 {
		t.Fatalf("target = %v", cfg.Zones[0].Rooms[0].Target)
	}
	if cfg.Control.Hysteresis != 1.0 {
		t.Fatalf("hysteresis = %v", cfg.Control.Hysteresis)
	}
}

func TestMultipleErrorsReported(t *testing.T) {
	yaml := `
unit: X
mqtt: {}
zones:
  - id: z
    relay: { device: d, switch: 0 }
    rooms:
      - id: r
        target: 100
        sensors: [{ zigbee: s }]
`
	errs := errsOf(t, yaml)
	if len(errs) < 3 {
		t.Fatalf("expected several errors, got %v", errs)
	}
	joined := strings.Join(errs, "\n")
	for _, want := range []string{"unit: must be", "mqtt.broker: is required", "rooms[0].target: must be between"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in:\n%s", want, joined)
		}
	}
}

func TestErrorsNameLine(t *testing.T) {
	yaml := "unit: C\nmqtt:\n  broker: tcp://127.0.0.1:1883\nzones:\n  - id: z\n    relay: { device: d, switch: 0 }\n    rooms:\n      - id: r\n        target: 99\n        sensors: [{ zigbee: s }]\n"
	errs := errsOf(t, yaml)
	if len(errs) != 1 {
		t.Fatalf("errors = %v", errs)
	}
	if !strings.Contains(errs[0], "line 9: zones[0].rooms[0].target:") {
		t.Fatalf("diagnostic = %q", errs[0])
	}
}

func TestRoomWithoutSensorsMayHaveNoTarget(t *testing.T) {
	cfg := mustParse(t, baseYAML)
	h := cfg.Zones[0].Rooms[1]
	if h.Target != nil || len(h.Schedule) != 0 || len(h.Sensors) != 0 {
		t.Fatalf("hallway = %+v", h)
	}
}
