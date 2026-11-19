package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

const base = `unit: C
mqtt:
  broker: tcp://192.168.1.10:1883
zones:
  - id: ground
    relay: { device: shellyplus1-a, switch: 0 }
    rooms:
      - id: living
        schedule:
          - { from: "06:30", target: 21.0, days: [mon, tue, wed, thu, fri] }
          - { from: "08:00", target: 21.0, days: [sat, sun] }
          - { from: "22:30", target: 18.0 }
        sensors:
          - { device: shellyhtg3-a, component: 0 }
          - { zigbee: living-display }
      - id: hallway
  - id: upstairs
    relay: { device: shellypro4pm-b, switch: 2 }
    rooms:
      - id: bedroom
        target: 19
        sensors:
          - { zigbee: bedroom-display }
`

func TestExampleConfigs(t *testing.T) {
	for _, path := range []string{"../../examples/config.yaml"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, errs := Parse(data); len(errs) > 0 {
			t.Errorf("%s: %v", path, errs)
		}
	}
}

func TestValidBase(t *testing.T) {
	cfg, errs := Parse([]byte(base))
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	c := cfg.Control
	if c.CycleInterval != time.Minute || c.Hysteresis != 0.5 || c.StaleAfter != 45*time.Minute ||
		c.MinSwitchInterval != 5*time.Minute || c.RelayLease != 15*time.Minute ||
		c.StartupGrace != 2*time.Minute || c.ConfirmTimeout != 30*time.Second {
		t.Errorf("defaults wrong: %+v", c)
	}
	if cfg.MQTT.ClientID != "heatctl" || cfg.MQTT.Zigbee2MQTTBase != "zigbee2mqtt" {
		t.Errorf("mqtt defaults wrong: %+v", cfg.MQTT)
	}
	if *cfg.Zones[1].Rooms[0].Target != 19 {
		t.Errorf("target 19 not parsed")
	}
	if len(cfg.Zones[0].Rooms[1].Sensors) != 0 || cfg.Zones[0].Rooms[1].HasTarget() {
		t.Errorf("hallway wrong")
	}
}

func TestFahrenheitDefaults(t *testing.T) {
	src := strings.Replace(base, "unit: C", "unit: F", 1)
	src = strings.ReplaceAll(src, "target: 21.0", "target: 70")
	src = strings.ReplaceAll(src, "target: 18.0", "target: 64.5")
	src = strings.ReplaceAll(src, "target: 19", "target: 66")
	cfg, errs := Parse([]byte(src))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if cfg.Control.Hysteresis != 1.0 {
		t.Errorf("hysteresis default with F = %v", cfg.Control.Hysteresis)
	}
	// 19 °F target is out of range with F.
	bad := strings.Replace(src, "target: 66", "target: 19", 1)
	if _, errs := Parse([]byte(bad)); !mentions(errs, "zones[1].rooms[0].target") {
		t.Errorf("19 with unit F accepted: %v", errs)
	}
}

func mentions(errs []error, key string) bool {
	for _, e := range errs {
		if strings.Contains(e.Error(), key) {
			return true
		}
	}
	return false
}

// Each rule of section 4 broken on its own must fail and name the key (F1.AC2, F1.AC3).
func TestRulesBrokenOneByOne(t *testing.T) {
	cases := []struct {
		name, old, new, key string
	}{
		{"unit missing", "unit: C\n", "", "unit"},
		{"unit invalid", "unit: C", "unit: K", "unit"},
		{"mqtt missing", "mqtt:\n  broker: tcp://192.168.1.10:1883\n", "", "mqtt"},
		{"broker missing", "  broker: tcp://192.168.1.10:1883\n", "  client_id: x\n", "mqtt.broker"},
		{"broker not url", "tcp://192.168.1.10:1883", "192.168.1.10", "mqtt.broker"},
		{"unknown top key", "unit: C", "unit: C\nextra: 1", "extra"},
		{"unknown mqtt key", "  broker: tcp://192.168.1.10:1883", "  broker: tcp://192.168.1.10:1883\n  port: 1", "mqtt.port"},
		{"unknown zone key", "  - id: ground\n", "  - id: ground\n    name: x\n", "zones[0].name"},
		{"unknown room key", "      - id: hallway\n", "      - id: hallway\n        size: 3\n", "zones[0].rooms[1].size"},
		{"unknown sensor key", "{ zigbee: living-display }", "{ zigbee: living-display, battery: true }", "zones[0].rooms[0].sensors[1].battery"},
		{"unknown entry key", `{ from: "22:30", target: 18.0 }`, `{ from: "22:30", target: 18.0, to: "23:00" }`, "schedule[2].to"},
		{"unknown relay key", "{ device: shellyplus1-a, switch: 0 }", "{ device: shellyplus1-a, switch: 0, type: plus }", "zones[0].relay.type"},
		{"unknown control key", "unit: C", "unit: C\ncontrol:\n  boost: 1", "control.boost"},
		{"bad duration", "unit: C", "unit: C\ncontrol:\n  stale_after: soon", "control.stale_after"},
		{"zero duration", "unit: C", "unit: C\ncontrol:\n  confirm_timeout: 0s", "control.confirm_timeout"},
		{"negative duration", "unit: C", "unit: C\ncontrol:\n  min_switch_interval: -5m", "control.min_switch_interval"},
		{"negative hysteresis", "unit: C", "unit: C\ncontrol:\n  hysteresis: -0.1", "control.hysteresis"},
		{"lease not longer than cycle", "unit: C", "unit: C\ncontrol:\n  cycle_interval: 15m", "control.relay_lease"},
		{"lease equal cycle", "unit: C", "unit: C\ncontrol:\n  cycle_interval: 10m\n  relay_lease: 10m", "control.relay_lease"},
		{"no zones", "zones:", "zones: []\nx_zones:", "zones: must list"},
		{"zone without relay", "    relay: { device: shellypro4pm-b, switch: 2 }\n", "", "zones[1].relay"},
		{"zone without rooms", "    rooms:\n      - id: bedroom\n        target: 19\n        sensors:\n          - { zigbee: bedroom-display }\n", "    rooms: []\n", "zones[1].rooms"},
		{"duplicate zone id", "  - id: upstairs", "  - id: ground", "zones[1].id"},
		{"duplicate room id across zones", "      - id: bedroom", "      - id: living", "zones[1].rooms[0].id"},
		{"shared relay channel", "{ device: shellypro4pm-b, switch: 2 }", "{ device: shellyplus1-a, switch: 0 }", "zones[1].relay"},
		{"negative switch", "switch: 2", "switch: -2", "zones[1].relay.switch"},
		{"non-integer switch", "switch: 2", "switch: 1.5", "zones[1].relay.switch"},
		{"negative component", "component: 0", "component: -1", "sensors[0].component"},
		{"sensor both kinds", "{ zigbee: living-display }", "{ zigbee: living-display, device: x, component: 1 }", "sensors[1].zigbee"},
		{"sensor neither kind", "{ zigbee: living-display }", "{ }", "zones[0].rooms[0].sensors[1]"},
		{"shelly sensor missing component", "{ device: shellyhtg3-a, component: 0 }", "{ device: shellyhtg3-a }", "sensors[0].component"},
		{"sensor in two rooms", "{ zigbee: bedroom-display }", "{ zigbee: living-display }", "zones[1].rooms[0].sensors[0]"},
		{"target and schedule", "        target: 19\n", "        target: 19\n        schedule: [{ from: \"07:00\", target: 20 }]\n", "zones[1].rooms[0].target"},
		{"sensors without target", "        target: 19\n", "", "zones[1].rooms[0].target"},
		{"target too low", "target: 19", "target: 6.9", "zones[1].rooms[0].target"},
		{"target too high", "target: 19", "target: 25.1", "zones[1].rooms[0].target"},
		{"target not a number", "target: 19", "target: warm", "zones[1].rooms[0].target"},
		{"empty schedule", "        schedule:\n          - { from: \"06:30\", target: 21.0, days: [mon, tue, wed, thu, fri] }\n          - { from: \"08:00\", target: 21.0, days: [sat, sun] }\n          - { from: \"22:30\", target: 18.0 }\n", "        schedule: []\n", "zones[0].rooms[0].schedule"},
		{"entry without from", `{ from: "22:30", target: 18.0 }`, `{ target: 18.0 }`, "schedule[2].from"},
		{"entry without target", `{ from: "22:30", target: 18.0 }`, `{ from: "22:30" }`, "schedule[2].target"},
		{"entry target out of range", `{ from: "22:30", target: 18.0 }`, `{ from: "22:30", target: 30 }`, "schedule[2].target"},
		{"bad from", `from: "22:30"`, `from: "22.30"`, "schedule[2].from"},
		{"from out of range", `from: "22:30"`, `from: "24:00"`, "schedule[2].from"},
		{"empty days", "days: [sat, sun]", "days: []", "schedule[1].days"},
		{"bad day", "days: [sat, sun]", "days: [sat, sunday]", "schedule[1].days[1]"},
		{"repeated day", "days: [sat, sun]", "days: [sat, sat]", "schedule[1].days[1]"},
		{"overlapping entries", `{ from: "22:30", target: 18.0 }`, `{ from: "22:30", target: 18.0 }
          - { from: "22:30", target: 19.0, days: [sun] }`, "schedule[3]"},
		{"duplicate key", "unit: C", "unit: C\nunit: F", "unit"},
		{"wildcard in device", "shellyplus1-a", "shellyplus1-#", "zones[0].relay.device"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(base, tc.old) {
				t.Fatalf("test bug: %q not in base", tc.old)
			}
			src := strings.Replace(base, tc.old, tc.new, 1)
			_, errs := Parse([]byte(src))
			if len(errs) == 0 {
				t.Fatalf("accepted:\n%s", src)
			}
			if !mentions(errs, tc.key) {
				t.Errorf("errors do not name %q: %v", tc.key, errs)
			}
		})
	}
}

func TestAllErrorsReported(t *testing.T) {
	src := strings.Replace(base, "unit: C", "unit: C\nfoo: 1\nbar: 2", 1)
	_, errs := Parse([]byte(src))
	if len(errs) != 2 {
		t.Errorf("want 2 errors, got %v", errs)
	}
}

func TestRoomWithoutSensorsMayHaveTarget(t *testing.T) {
	src := strings.Replace(base, "      - id: hallway\n", "      - id: hallway\n        target: 15\n        sensors: []\n", 1)
	if _, errs := Parse([]byte(src)); len(errs) > 0 {
		t.Error(errs)
	}
}
