package config

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Issue is one problem found in a configuration file.
type Issue struct {
	Line int    // 1-based line in the file, 0 if unknown
	Key  string // path of the offending key, e.g. zones[0].rooms[1].target
	Msg  string
}

func (e *Issue) Error() string {
	var b strings.Builder
	if e.Line > 0 {
		fmt.Fprintf(&b, "line %d: ", e.Line)
	}
	if e.Key != "" {
		b.WriteString(e.Key)
		b.WriteString(": ")
	}
	b.WriteString(e.Msg)
	return b.String()
}

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

type parser struct {
	issues []error
}

func (p *parser) add(n *yaml.Node, key, format string, args ...any) {
	line := 0
	if n != nil {
		line = n.Line
	}
	p.issues = append(p.issues, &Issue{Line: line, Key: key, Msg: fmt.Sprintf(format, args...)})
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// mapping checks that n is a mapping with only the allowed keys and returns
// its values by key. Unknown and duplicate keys are reported.
func (p *parser) mapping(n *yaml.Node, path string, allowed ...string) (map[string]*yaml.Node, bool) {
	if n.Kind != yaml.MappingNode {
		p.add(n, path, "must be a mapping")
		return nil, false
	}
	out := make(map[string]*yaml.Node)
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		name := k.Value
		if k.Kind != yaml.ScalarNode {
			p.add(k, path, "keys must be plain strings")
			continue
		}
		known := false
		for _, a := range allowed {
			if a == name {
				known = true
				break
			}
		}
		if !known {
			p.add(k, join(path, name), "unknown key")
			continue
		}
		if _, dup := out[name]; dup {
			p.add(k, join(path, name), "duplicate key")
			continue
		}
		out[name] = v
	}
	return out, true
}

func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

func (p *parser) str(n *yaml.Node, path string) (string, bool) {
	if n.Kind != yaml.ScalarNode {
		p.add(n, path, "must be a string")
		return "", false
	}
	switch n.Tag {
	case "!!str", "!!int", "!!float":
		return n.Value, true
	}
	p.add(n, path, "must be a string")
	return "", false
}

func (p *parser) nonEmptyStr(n *yaml.Node, path string) (string, bool) {
	s, ok := p.str(n, path)
	if ok && strings.TrimSpace(s) == "" {
		p.add(n, path, "must not be empty")
		return "", false
	}
	return s, ok
}

// topicPart is a string used inside MQTT topics: no wildcards, no NUL, not empty.
func (p *parser) topicPart(n *yaml.Node, path string) (string, bool) {
	s, ok := p.nonEmptyStr(n, path)
	if !ok {
		return "", false
	}
	if strings.ContainsAny(s, "+#\x00") {
		p.add(n, path, "must not contain the MQTT wildcards + or #")
		return "", false
	}
	if strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") {
		p.add(n, path, "must not start or end with /")
		return "", false
	}
	return s, true
}

func (p *parser) number(n *yaml.Node, path string) (float64, bool) {
	if n.Kind != yaml.ScalarNode || (n.Tag != "!!int" && n.Tag != "!!float") {
		p.add(n, path, "must be a number")
		return 0, false
	}
	var f float64
	if err := n.Decode(&f); err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		p.add(n, path, "must be a finite number")
		return 0, false
	}
	return f, true
}

func (p *parser) channel(n *yaml.Node, path string) (int, bool) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!int" {
		p.add(n, path, "must be an integer, 0 or more")
		return 0, false
	}
	var v int
	if err := n.Decode(&v); err != nil || v < 0 {
		p.add(n, path, "must be an integer, 0 or more")
		return 0, false
	}
	return v, true
}

func (p *parser) duration(n *yaml.Node, path string) (time.Duration, bool) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		p.add(n, path, "must be a duration such as 60s or 5m")
		return 0, false
	}
	d, err := time.ParseDuration(n.Value)
	if err != nil {
		p.add(n, path, "must be a duration such as 60s or 5m: %q", n.Value)
		return 0, false
	}
	if d <= 0 {
		p.add(n, path, "must be positive")
		return 0, false
	}
	return d, true
}

func (p *parser) sequence(n *yaml.Node, path string) ([]*yaml.Node, bool) {
	if isNull(n) {
		return nil, true
	}
	if n.Kind != yaml.SequenceNode {
		p.add(n, path, "must be a list")
		return nil, false
	}
	return n.Content, true
}

// Parse parses and validates a configuration. It returns every problem found.
func Parse(data []byte) (*Config, []error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, []error{fmt.Errorf("invalid YAML: %w", err)}
	}
	p := &parser{}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		p.add(nil, "", "configuration is empty")
		return nil, p.issues
	}
	cfg := p.config(doc.Content[0])
	if len(p.issues) > 0 {
		return nil, p.issues
	}
	return cfg, nil
}

func (p *parser) config(root *yaml.Node) *Config {
	cfg := &Config{}
	m, ok := p.mapping(root, "", "unit", "mqtt", "control", "zones")
	if !ok {
		return cfg
	}

	if n, ok := m["unit"]; !ok {
		p.add(root, "unit", "is required (C or F)")
	} else if s, ok := p.str(n, "unit"); ok {
		switch Unit(s) {
		case Celsius, Fahrenheit:
			cfg.Unit = Unit(s)
		default:
			p.add(n, "unit", "must be C or F, not %q", s)
		}
	}
	unitKnown := cfg.Unit != ""
	if !unitKnown {
		cfg.Unit = Celsius // keep checking the rest against something sensible
	}

	if n, ok := m["mqtt"]; !ok {
		p.add(root, "mqtt", "is required")
	} else {
		cfg.MQTT = p.mqtt(n)
	}

	cfg.Control = p.control(m["control"], cfg.Unit)

	if n, ok := m["zones"]; !ok {
		p.add(root, "zones", "is required")
	} else {
		cfg.Zones = p.zones(n, cfg.Unit, unitKnown)
	}
	return cfg
}

func (p *parser) mqtt(n *yaml.Node) MQTT {
	out := MQTT{ClientID: "heatctl", Zigbee2MQTTBase: "zigbee2mqtt"}
	m, ok := p.mapping(n, "mqtt", "broker", "client_id", "username", "password", "zigbee2mqtt_topic")
	if !ok {
		return out
	}
	if v, ok := m["broker"]; !ok {
		p.add(n, "mqtt.broker", "is required")
	} else if s, ok := p.nonEmptyStr(v, "mqtt.broker"); ok {
		u, err := url.Parse(s)
		switch {
		case err != nil:
			p.add(v, "mqtt.broker", "is not a valid URL: %v", err)
		case u.Scheme != "tcp" && u.Scheme != "mqtt" && u.Scheme != "ssl" && u.Scheme != "tls" && u.Scheme != "mqtts":
			p.add(v, "mqtt.broker", "must be a URL such as tcp://192.168.1.10:1883 (schemes tcp, mqtt, ssl, tls, mqtts)")
		case u.Host == "":
			p.add(v, "mqtt.broker", "must include a host, e.g. tcp://192.168.1.10:1883")
		default:
			out.Broker = s
		}
	}
	if v, ok := m["client_id"]; ok {
		if s, ok := p.nonEmptyStr(v, "mqtt.client_id"); ok {
			out.ClientID = s
		}
	}
	if v, ok := m["username"]; ok && !isNull(v) {
		if s, ok := p.str(v, "mqtt.username"); ok {
			out.Username = s
		}
	}
	if v, ok := m["password"]; ok && !isNull(v) {
		if s, ok := p.str(v, "mqtt.password"); ok {
			out.Password = s
		}
	}
	if v, ok := m["zigbee2mqtt_topic"]; ok {
		if s, ok := p.topicPart(v, "mqtt.zigbee2mqtt_topic"); ok {
			out.Zigbee2MQTTBase = s
		}
	}
	return out
}

func (p *parser) control(n *yaml.Node, unit Unit) Control {
	c := Control{
		CycleInterval:     60 * time.Second,
		Hysteresis:        unit.DefaultHysteresis(),
		StaleAfter:        45 * time.Minute,
		MinSwitchInterval: 5 * time.Minute,
		RelayLease:        15 * time.Minute,
		StartupGrace:      2 * time.Minute,
		ConfirmTimeout:    30 * time.Second,
	}
	if n == nil || isNull(n) {
		return c
	}
	m, ok := p.mapping(n, "control", "cycle_interval", "hysteresis", "stale_after",
		"min_switch_interval", "relay_lease", "startup_grace", "confirm_timeout")
	if !ok {
		return c
	}
	durations := []struct {
		key string
		dst *time.Duration
	}{
		{"cycle_interval", &c.CycleInterval},
		{"stale_after", &c.StaleAfter},
		{"min_switch_interval", &c.MinSwitchInterval},
		{"relay_lease", &c.RelayLease},
		{"startup_grace", &c.StartupGrace},
		{"confirm_timeout", &c.ConfirmTimeout},
	}
	valid := map[string]bool{}
	for _, d := range durations {
		v, present := m[d.key]
		if !present {
			valid[d.key] = true
			continue
		}
		if got, ok := p.duration(v, "control."+d.key); ok {
			*d.dst = got
			valid[d.key] = true
		}
	}
	if v, ok := m["hysteresis"]; ok {
		if h, ok := p.number(v, "control.hysteresis"); ok {
			if h < 0 {
				p.add(v, "control.hysteresis", "must be at least 0")
			} else {
				c.Hysteresis = h
			}
		}
	}
	if valid["relay_lease"] && valid["cycle_interval"] && c.RelayLease <= c.CycleInterval {
		at := m["relay_lease"]
		if at == nil {
			at = m["cycle_interval"]
		}
		p.add(at, "control.relay_lease", "must be longer than control.cycle_interval (%s <= %s)", c.RelayLease, c.CycleInterval)
	}
	if valid["relay_lease"] && c.RelayLease < 2*time.Second {
		p.add(m["relay_lease"], "control.relay_lease", "must be at least 2s: relays are leased in whole seconds")
	}
	return c
}

type relayKey struct {
	device string
	sw     int
}

func (p *parser) zones(n *yaml.Node, unit Unit, unitKnown bool) []Zone {
	items, ok := p.sequence(n, "zones")
	if !ok {
		return nil
	}
	if len(items) == 0 {
		p.add(n, "zones", "must list at least one zone")
		return nil
	}
	zoneIDs := map[string]string{}
	roomIDs := map[string]string{}
	relays := map[relayKey]string{}
	sensors := map[string]string{}
	var out []Zone
	for i, zn := range items {
		path := fmt.Sprintf("zones[%d]", i)
		m, ok := p.mapping(zn, path, "id", "relay", "rooms")
		if !ok {
			continue
		}
		var z Zone
		if v, ok := m["id"]; !ok {
			p.add(zn, path+".id", "is required")
		} else if s, ok := p.nonEmptyStr(v, path+".id"); ok {
			if prev, dup := zoneIDs[s]; dup {
				p.add(v, path+".id", "zone id %q is already used by %s", s, prev)
			} else {
				zoneIDs[s] = path
			}
			z.ID = s
		}
		if v, ok := m["relay"]; !ok {
			p.add(zn, path+".relay", "is required")
		} else if r, ok := p.relay(v, path+".relay"); ok {
			k := relayKey{r.Device, r.Switch}
			if prev, dup := relays[k]; dup {
				p.add(v, path+".relay", "relay channel %s is already used by %s", r, prev)
			} else {
				relays[k] = path + ".relay"
			}
			z.Relay = r
		}
		if v, ok := m["rooms"]; !ok {
			p.add(zn, path+".rooms", "is required: a zone has one or more rooms")
		} else if rooms, ok := p.sequence(v, path+".rooms"); ok {
			if len(rooms) == 0 {
				p.add(v, path+".rooms", "must list at least one room")
			}
			for j, rn := range rooms {
				rp := fmt.Sprintf("%s.rooms[%d]", path, j)
				if r, ok := p.room(rn, rp, unit, unitKnown, roomIDs, sensors); ok {
					z.Rooms = append(z.Rooms, r)
				}
			}
		}
		out = append(out, z)
	}
	return out
}

func (p *parser) relay(n *yaml.Node, path string) (Relay, bool) {
	var r Relay
	m, ok := p.mapping(n, path, "device", "switch")
	if !ok {
		return r, false
	}
	good := true
	if v, ok := m["device"]; !ok {
		p.add(n, path+".device", "is required")
		good = false
	} else if s, ok := p.topicPart(v, path+".device"); ok {
		r.Device = s
	} else {
		good = false
	}
	if v, ok := m["switch"]; !ok {
		p.add(n, path+".switch", "is required")
		good = false
	} else if id, ok := p.channel(v, path+".switch"); ok {
		r.Switch = id
	} else {
		good = false
	}
	return r, good
}

func (p *parser) room(n *yaml.Node, path string, unit Unit, unitKnown bool, roomIDs, sensorsSeen map[string]string) (Room, bool) {
	var r Room
	m, ok := p.mapping(n, path, "id", "target", "schedule", "sensors")
	if !ok {
		return r, false
	}
	if v, ok := m["id"]; !ok {
		p.add(n, path+".id", "is required")
	} else if s, ok := p.nonEmptyStr(v, path+".id"); ok {
		if prev, dup := roomIDs[s]; dup {
			p.add(v, path+".id", "room id %q is already used by %s (room ids are unique across the house)", s, prev)
		} else {
			roomIDs[s] = path
		}
		r.ID = s
	}

	if v, ok := m["sensors"]; ok {
		if items, ok := p.sequence(v, path+".sensors"); ok {
			for k, sn := range items {
				sp := fmt.Sprintf("%s.sensors[%d]", path, k)
				s, ok := p.sensor(sn, sp)
				if !ok {
					continue
				}
				key := s.String()
				if prev, dup := sensorsSeen[key]; dup {
					p.add(sn, sp, "sensor %s is already listed at %s", key, prev)
				} else {
					sensorsSeen[key] = sp
				}
				r.Sensors = append(r.Sensors, s)
			}
		}
	}
	hasSensors := false
	if v, ok := m["sensors"]; ok && v.Kind == yaml.SequenceNode && len(v.Content) > 0 {
		hasSensors = true
	}

	tn, hasTarget := m["target"]
	sn, hasSchedule := m["schedule"]
	switch {
	case hasTarget && hasSchedule:
		p.add(tn, path+".target", "a room has either target or schedule, not both")
	case hasSensors && !hasTarget && !hasSchedule:
		p.add(n, path+".target", "a room with sensors needs target or schedule")
	}
	lo, hi := unit.TargetRange()
	checkTarget := func(v *yaml.Node, key string) (float64, bool) {
		t, ok := p.number(v, key)
		if !ok {
			return 0, false
		}
		if unitKnown && (t < lo || t > hi) {
			p.add(v, key, "must be between %.1f and %.1f with unit %s, not %v", lo, hi, unit, t)
			return 0, false
		}
		return t, true
	}
	if hasTarget {
		if t, ok := checkTarget(tn, path+".target"); ok {
			r.Target = &t
		}
	}
	if hasSchedule {
		r.Schedule = p.schedule(sn, path+".schedule", checkTarget)
	}
	return r, true
}

func (p *parser) sensor(n *yaml.Node, path string) (Sensor, bool) {
	var s Sensor
	m, ok := p.mapping(n, path, "device", "component", "zigbee")
	if !ok {
		return s, false
	}
	dv, hasDevice := m["device"]
	cv, hasComponent := m["component"]
	zv, hasZigbee := m["zigbee"]
	switch {
	case hasZigbee && (hasDevice || hasComponent):
		p.add(n, path+".zigbee", "a sensor is either { device, component } or { zigbee }, never both")
		return s, false
	case hasZigbee:
		name, ok := p.topicPart(zv, path+".zigbee")
		if !ok {
			return s, false
		}
		s.Zigbee = name
		return s, true
	case !hasDevice && !hasComponent:
		p.add(n, path, "a sensor needs { device, component } or { zigbee }")
		return s, false
	}
	good := true
	if !hasDevice {
		p.add(n, path+".device", "is required with component")
		good = false
	} else if d, ok := p.topicPart(dv, path+".device"); ok {
		s.Device = d
	} else {
		good = false
	}
	if !hasComponent {
		p.add(n, path+".component", "is required with device")
		good = false
	} else if c, ok := p.channel(cv, path+".component"); ok {
		s.Component = c
	} else {
		good = false
	}
	return s, good
}

func (p *parser) schedule(n *yaml.Node, path string, checkTarget func(*yaml.Node, string) (float64, bool)) []Entry {
	items, ok := p.sequence(n, path)
	if !ok {
		return nil
	}
	if len(items) == 0 {
		p.add(n, path, "must have at least one entry")
		return nil
	}
	var out []Entry
	var outPaths []string
	for i, en := range items {
		ep := fmt.Sprintf("%s[%d]", path, i)
		m, ok := p.mapping(en, ep, "from", "target", "days")
		if !ok {
			continue
		}
		e := Entry{}
		good := true
		if v, ok := m["from"]; !ok {
			p.add(en, ep+".from", "is required")
			good = false
		} else if s, ok := p.str(v, ep+".from"); ok {
			if min, ok := parseClock(s); ok {
				e.From, e.Minute = s, min
			} else {
				p.add(v, ep+".from", "must be HH:MM local time (00:00 to 23:59), not %q", s)
				good = false
			}
		} else {
			good = false
		}
		if v, ok := m["target"]; !ok {
			p.add(en, ep+".target", "is required")
			good = false
		} else if t, ok := checkTarget(v, ep+".target"); ok {
			e.Target = t
		} else {
			good = false
		}
		if v, ok := m["days"]; !ok {
			for d := range e.Days {
				e.Days[d] = true
			}
		} else if days, ok := p.days(v, ep+".days"); ok {
			e.Days = days
		} else {
			good = false
		}
		if !good {
			continue
		}
		for j, prev := range out {
			if prev.Minute != e.Minute {
				continue
			}
			for d := time.Sunday; d <= time.Saturday; d++ {
				if prev.Days[d] && e.Days[d] {
					p.add(en, ep, "applies at %s on %s, like %s", e.From, dayName(d), outPaths[j])
					break
				}
			}
		}
		out = append(out, e)
		outPaths = append(outPaths, ep)
	}
	return out
}

func (p *parser) days(n *yaml.Node, path string) ([7]bool, bool) {
	var out [7]bool
	if n.Kind != yaml.SequenceNode {
		p.add(n, path, "must be a list of mon, tue, wed, thu, fri, sat, sun")
		return out, false
	}
	if len(n.Content) == 0 {
		p.add(n, path, "must not be empty; omit days for every day")
		return out, false
	}
	good := true
	for i, dn := range n.Content {
		dp := fmt.Sprintf("%s[%d]", path, i)
		s, ok := p.str(dn, dp)
		if !ok {
			good = false
			continue
		}
		d, ok := weekdays[s]
		if !ok {
			p.add(dn, dp, "must be one of mon, tue, wed, thu, fri, sat, sun, not %q", s)
			good = false
			continue
		}
		if out[d] {
			p.add(dn, dp, "%s is listed twice", s)
			good = false
			continue
		}
		out[d] = true
	}
	return out, good
}

func dayName(d time.Weekday) string {
	return strings.ToLower(d.String()[:3])
}

// parseClock parses HH:MM into minutes after midnight.
func parseClock(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	h, err1 := strconv.Atoi(s[:2])
	m, err2 := strconv.Atoi(s[3:])
	if err1 != nil || err2 != nil || s[0] == '+' || s[3] == '+' || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}
