package config

import (
	"strings"
	"testing"
)

const good = `
unit: C
mqtt: {broker: "tcp://x:1883"}
zones:
  - id: a
    relay: {device: r, switch: 0}
    rooms:
      - id: l
        target: 20
        sensors: [{zigbee: z}]
`

func TestValid(t *testing.T) {
	if _, errs := Parse([]byte(good)); errs != nil {
		t.Fatal(errs)
	}
}

func TestInvalid(t *testing.T) {
	cases := map[string]struct{ from, to, want string }{
		"unknown":  {"unit: C", "unit: C\nbogus: 1", "bogus"},
		"unit":     {"unit: C", "unit: K", "unit"},
		"target":   {"target: 20", "target: 26", "target"},
		"both":     {"target: 20", "target: 20\n        schedule: [{from: \"06:00\", target: 20}]", "target"},
		"neither":  {"        target: 20\n", "", "target"},
		"lease":    {"zones:", "control: {relay_lease: 30s}\nzones:", "relay_lease"},
		"hyst":     {"zones:", "control: {hysteresis: -1}\nzones:", "hysteresis"},
		"dur":      {"zones:", "control: {stale_after: 0s}\nzones:", "stale_after"},
		"dupzone":  {"zones:\n", "zones:\n  - {id: a, relay: {device: q, switch: 1}, rooms: [{id: o}]}\n", "id"},
		"duproom":  {"zones:\n", "zones:\n  - {id: b, relay: {device: q, switch: 1}, rooms: [{id: l}]}\n", "id"},
		"samerel":  {"zones:\n", "zones:\n  - {id: b, relay: {device: r, switch: 0}, rooms: [{id: o}]}\n", "relay"},
		"bothsens": {"{zigbee: z}", "{zigbee: z, device: d, component: 0}", "zigbee"},
		"negsw":    {"switch: 0", "switch: -1", "switch"},
		"days":     {"target: 20", "schedule: [{from: \"06:00\", target: 20, days: [mon, mon]}]", "days"},
		"overlap":  {"target: 20", "schedule: [{from: \"06:00\", target: 20}, {from: \"06:00\", target: 20, days: [sun]}]", "from"},
		"from":     {"target: 20", "schedule: [{from: \"6am\", target: 20}]", "from"},
	}
	for name, c := range cases {
		_, errs := Parse([]byte(strings.Replace(good, c.from, c.to, 1)))
		if name == "dupzone" || name == "duproom" || name == "samerel" {
			// replacement above prepends extra zones; strip the trailing marker line
		}
		if len(errs) == 0 {
			t.Errorf("%s: no error", name)
			continue
		}
		var all []string
		for _, e := range errs {
			all = append(all, e.Error())
		}
		if !strings.Contains(strings.Join(all, "\n"), c.want) {
			t.Errorf("%s: %v does not name %q", name, all, c.want)
		}
	}
}
