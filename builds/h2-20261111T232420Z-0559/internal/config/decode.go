package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// The decoder turns YAML into unvalidated values, recording one diagnostic per
// problem it finds. Presence is tracked explicitly so that "not set" and "set
// to zero" can be told apart during validation.

type rawConfig struct {
	unit    string
	unitSet bool

	mqtt    rawMQTT
	control rawControl

	zones        []rawZone
	zonesSeen    bool
	zonesShapeOK bool
	zoneItemBad  bool
	roomItemBad  bool
}

type rawMQTT struct {
	broker      string
	brokerSet   bool
	clientID    string
	clientIDSet bool
	username    string
	userSet     bool
	password    string
	passSet     bool
	zigbee      string
	zigbeeSet   bool
}

type rawControl struct {
	cycle      *time.Duration
	hysteresis *float64
	stale      *time.Duration
	minSwitch  *time.Duration
	lease      *time.Duration
	grace      *time.Duration
	confirm    *time.Duration
}

type rawZone struct {
	id       string
	idSet    bool
	relay    rawRelay
	rooms    []rawRoom
	roomsSet bool
	roomsOK  bool
}

type rawRelay struct {
	device    string
	deviceSet bool
	sw        int
	swSet     bool
}

type rawRoom struct {
	id          string
	idSet       bool
	target      *float64
	schedule    []rawScheduleEntry
	scheduleSet bool
	scheduleOK  bool
	sensors     []rawSensor
	sensorsSet  bool
}

type rawScheduleEntry struct {
	from    int
	fromSet bool
	target  *float64
	days    DaySet
	daysSet bool
}

type rawSensor struct {
	device       string
	deviceSet    bool
	component    int
	componentSet bool
	zigbee       string
	zigbeeSet    bool
}

type decoder struct {
	path  string
	errs  []error
	lines map[string]int
	cols  map[string]int
	root  rawConfig
}

func newDecoder(path string) *decoder {
	return &decoder{
		path:  path,
		lines: make(map[string]int),
		cols:  make(map[string]int),
	}
}

func (d *decoder) add(line int, key, msg string) {
	d.errs = append(d.errs, Diagnostic{Line: line, Key: key, Msg: msg})
}

func (d *decoder) addNode(n *yaml.Node, key, msg string) {
	n = resolve(n)
	line, col := 0, 0
	if n != nil {
		line, col = n.Line, n.Column
	}
	d.errs = append(d.errs, Diagnostic{Line: line, Column: col, Key: key, Msg: msg})
}

// addPath reports a problem with a key that was decoded earlier, using the
// line recorded for it.
func (d *decoder) addPath(key, msg string) {
	d.errs = append(d.errs, Diagnostic{Line: d.lines[key], Column: d.cols[key], Key: key, Msg: msg})
}

func (d *decoder) remember(key string, n *yaml.Node) {
	n = resolve(n)
	if n == nil {
		return
	}
	if n.Line > 0 {
		d.lines[key] = n.Line
		d.cols[key] = n.Column
	}
}

func resolve(n *yaml.Node) *yaml.Node {
	for i := 0; n != nil && n.Kind == yaml.AliasNode && n.Alias != nil && i < 100; i++ {
		n = n.Alias
	}
	return n
}

func isNull(n *yaml.Node) bool {
	n = resolve(n)
	return n == nil || (n.Kind == yaml.ScalarNode && n.Tag == "!!null")
}

func join(base, name string) string {
	if base == "" {
		return name
	}
	return base + "." + name
}

func (d *decoder) decode(data []byte) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		msg := strings.TrimPrefix(err.Error(), "yaml: ")
		d.add(0, "config", "invalid YAML: "+msg)
		return
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		d.add(0, "config", "configuration is empty")
		return
	}
	d.decodeRoot(resolve(doc.Content[0]))
}

func (d *decoder) decodeRoot(n *yaml.Node) {
	if isNull(n) {
		d.addNode(n, "config", "must be a YAML mapping")
		return
	}
	f, ok := d.fields(n, "", []string{"unit", "mqtt", "control", "zones"})
	if !ok {
		return
	}
	if v, ok := f["unit"]; ok && !isNull(v) {
		d.root.unitSet = true
		if s, ok := d.stringField(v, "unit"); ok {
			d.root.unit = s
		}
	}
	if v, ok := f["mqtt"]; ok {
		d.decodeMQTT(v)
	}
	if v, ok := f["control"]; ok {
		d.decodeControl(v)
	}
	if v, ok := f["zones"]; ok {
		d.root.zonesSeen = true
		d.decodeZones(v)
	}
}

func (d *decoder) decodeMQTT(n *yaml.Node) {
	f, ok := d.fields(n, "mqtt", []string{"broker", "client_id", "username", "password", "zigbee2mqtt_topic"})
	if !ok {
		return
	}
	m := &d.root.mqtt
	if v, ok := f["broker"]; ok {
		m.brokerSet = true
		if s, ok := d.stringField(v, "mqtt.broker"); ok {
			m.broker = s
		}
	}
	if v, ok := f["client_id"]; ok {
		m.clientIDSet = true
		if s, ok := d.stringField(v, "mqtt.client_id"); ok {
			m.clientID = s
		}
	}
	if v, ok := f["username"]; ok {
		m.userSet = true
		if s, ok := d.stringField(v, "mqtt.username"); ok {
			m.username = s
		}
	}
	if v, ok := f["password"]; ok {
		m.passSet = true
		if s, ok := d.stringField(v, "mqtt.password"); ok {
			m.password = s
		}
	}
	if v, ok := f["zigbee2mqtt_topic"]; ok {
		m.zigbeeSet = true
		if s, ok := d.stringField(v, "mqtt.zigbee2mqtt_topic"); ok {
			m.zigbee = s
		}
	}
}

func (d *decoder) decodeControl(n *yaml.Node) {
	f, ok := d.fields(n, "control", []string{
		"cycle_interval", "hysteresis", "stale_after", "min_switch_interval",
		"relay_lease", "startup_grace", "confirm_timeout",
	})
	if !ok {
		return
	}
	c := &d.root.control
	readDuration := func(key string, dst **time.Duration) {
		if v, ok := f[key]; ok {
			if dur, ok := d.durationField(v, "control."+key); ok {
				*dst = &dur
			}
		}
	}
	readDuration("cycle_interval", &c.cycle)
	readDuration("stale_after", &c.stale)
	readDuration("min_switch_interval", &c.minSwitch)
	readDuration("relay_lease", &c.lease)
	readDuration("startup_grace", &c.grace)
	readDuration("confirm_timeout", &c.confirm)
	if v, ok := f["hysteresis"]; ok {
		if h, ok := d.floatField(v, "control.hysteresis"); ok {
			c.hysteresis = &h
		}
	}
}

func (d *decoder) decodeZones(n *yaml.Node) {
	n = resolve(n)
	if isNull(n) {
		d.addNode(n, "zones", "at least one zone is required")
		return
	}
	if n.Kind != yaml.SequenceNode {
		d.addNode(n, "zones", "must be a list of zones")
		return
	}
	d.root.zonesShapeOK = true
	for i, item := range n.Content {
		base := fmt.Sprintf("zones[%d]", i)
		var z rawZone
		f, ok := d.fields(item, base, []string{"id", "relay", "rooms"})
		if !ok {
			d.root.zoneItemBad = true
			d.root.zones = append(d.root.zones, z)
			continue
		}
		if v, ok := f["id"]; ok {
			if s, ok := d.stringField(v, base+".id"); ok {
				z.id, z.idSet = s, true
			}
		}
		if v, ok := f["relay"]; ok {
			d.decodeRelay(v, base+".relay", &z.relay)
		}
		if v, ok := f["rooms"]; ok {
			z.roomsSet = true
			d.decodeRooms(v, base+".rooms", &z)
		}
		d.root.zones = append(d.root.zones, z)
	}
}

func (d *decoder) decodeRelay(n *yaml.Node, base string, r *rawRelay) {
	f, ok := d.fields(n, base, []string{"device", "switch"})
	if !ok {
		return
	}
	if v, ok := f["device"]; ok {
		r.deviceSet = true
		if s, ok := d.stringField(v, base+".device"); ok {
			r.device = s
		}
	}
	if v, ok := f["switch"]; ok {
		r.swSet = true
		if i, ok := d.intField(v, base+".switch"); ok {
			r.sw = i
		}
	}
}

func (d *decoder) decodeRooms(n *yaml.Node, base string, z *rawZone) {
	n = resolve(n)
	if isNull(n) {
		d.addNode(n, base, "one or more rooms are required")
		return
	}
	if n.Kind != yaml.SequenceNode {
		d.addNode(n, base, "must be a list of rooms")
		return
	}
	z.roomsOK = true
	for i, item := range n.Content {
		p := fmt.Sprintf("%s[%d]", base, i)
		var r rawRoom
		f, ok := d.fields(item, p, []string{"id", "target", "schedule", "sensors"})
		if !ok {
			d.root.roomItemBad = true
			z.rooms = append(z.rooms, r)
			continue
		}
		if v, ok := f["id"]; ok {
			if s, ok := d.stringField(v, p+".id"); ok {
				r.id, r.idSet = s, true
			}
		}
		if v, ok := f["target"]; ok && !isNull(v) {
			if fv, ok := d.floatField(v, p+".target"); ok {
				r.target = &fv
			}
		}
		if v, ok := f["schedule"]; ok {
			r.scheduleSet = true
			d.decodeSchedule(v, p+".schedule", &r)
		}
		if v, ok := f["sensors"]; ok {
			r.sensorsSet = true
			d.decodeSensors(v, p+".sensors", &r)
		}
		z.rooms = append(z.rooms, r)
	}
}

func (d *decoder) decodeSchedule(n *yaml.Node, base string, r *rawRoom) {
	n = resolve(n)
	if isNull(n) {
		d.addNode(n, base, "must have at least one entry")
		return
	}
	if n.Kind != yaml.SequenceNode {
		d.addNode(n, base, "must be a list of schedule entries")
		return
	}
	r.scheduleOK = true
	for i, item := range n.Content {
		p := fmt.Sprintf("%s[%d]", base, i)
		var e rawScheduleEntry
		f, ok := d.fields(item, p, []string{"from", "target", "days"})
		if !ok {
			continue
		}
		if v, ok := f["from"]; ok {
			if s, ok := d.stringField(v, p+".from"); ok {
				if mins, ok := parseClock(s); ok {
					e.from, e.fromSet = mins, true
				} else {
					d.addNode(v, p+".from", fmt.Sprintf("must be HH:MM local time, got %q", s))
				}
			}
		}
		if v, ok := f["target"]; ok && !isNull(v) {
			if fv, ok := d.floatField(v, p+".target"); ok {
				e.target = &fv
			}
		}
		if v, ok := f["days"]; ok {
			e.daysSet = true
			d.decodeDays(v, p+".days", &e)
		}
		r.schedule = append(r.schedule, e)
	}
}

func (d *decoder) decodeDays(n *yaml.Node, base string, e *rawScheduleEntry) {
	n = resolve(n)
	if isNull(n) {
		d.addNode(n, base, "must not be empty")
		return
	}
	if n.Kind != yaml.SequenceNode {
		d.addNode(n, base, "must be a list of day names")
		return
	}
	if len(n.Content) == 0 {
		d.addNode(n, base, "must not be empty")
		return
	}
	for i, item := range n.Content {
		p := fmt.Sprintf("%s[%d]", base, i)
		s, ok := d.stringField(item, p)
		if !ok {
			continue
		}
		bit, ok := ParseDay(s)
		if !ok {
			d.addNode(item, p, fmt.Sprintf("unknown day %q (use mon, tue, wed, thu, fri, sat, sun)", s))
			continue
		}
		if e.days&bit != 0 {
			d.addNode(item, p, fmt.Sprintf("duplicate day %q", s))
			continue
		}
		e.days |= bit
	}
}

func (d *decoder) decodeSensors(n *yaml.Node, base string, r *rawRoom) {
	n = resolve(n)
	if isNull(n) {
		return
	}
	if n.Kind != yaml.SequenceNode {
		d.addNode(n, base, "must be a list of sensors")
		return
	}
	for i, item := range n.Content {
		p := fmt.Sprintf("%s[%d]", base, i)
		f, ok := d.fields(item, p, []string{"device", "component", "zigbee"})
		if !ok {
			continue
		}
		var s rawSensor
		if v, ok := f["device"]; ok {
			s.deviceSet = true
			if str, ok := d.stringField(v, p+".device"); ok {
				s.device = str
			}
		}
		if v, ok := f["component"]; ok {
			s.componentSet = true
			if iv, ok := d.intField(v, p+".component"); ok {
				s.component = iv
			}
		}
		if v, ok := f["zigbee"]; ok {
			s.zigbeeSet = true
			if str, ok := d.stringField(v, p+".zigbee"); ok {
				s.zigbee = str
			}
		}
		r.sensors = append(r.sensors, s)
	}
}

// fields iterates a YAML mapping, reporting unknown and duplicate keys. The
// returned map holds the value nodes of the known keys that are present.
func (d *decoder) fields(n *yaml.Node, base string, known []string) (map[string]*yaml.Node, bool) {
	n = resolve(n)
	out := make(map[string]*yaml.Node, len(known))
	if base != "" {
		d.remember(base, n)
	}
	if isNull(n) {
		key := base
		if key == "" {
			key = "config"
		}
		d.addNode(n, key, "must be a mapping")
		return out, false
	}
	if n.Kind != yaml.MappingNode {
		key := base
		if key == "" {
			key = "config"
		}
		d.addNode(n, key, "must be a mapping")
		return out, false
	}
	allowed := make(map[string]bool, len(known))
	for _, k := range known {
		allowed[k] = true
	}
	seen := make(map[string]bool, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := resolve(n.Content[i]), n.Content[i+1]
		if k == nil || k.Kind != yaml.ScalarNode {
			d.addNode(k, base, "key must be a string")
			continue
		}
		name := k.Value
		key := join(base, name)
		if seen[name] {
			d.addNode(k, key, "duplicate key")
			continue
		}
		seen[name] = true
		if !allowed[name] {
			d.addNode(k, key, "unknown key")
			continue
		}
		d.remember(key, v)
		out[name] = v
	}
	return out, true
}

func (d *decoder) stringField(n *yaml.Node, key string) (string, bool) {
	n = resolve(n)
	d.remember(key, n)
	if isNull(n) {
		d.addNode(n, key, "must not be empty")
		return "", false
	}
	if n.Kind != yaml.ScalarNode {
		d.addNode(n, key, "must be a string")
		return "", false
	}
	return n.Value, true
}

func (d *decoder) floatField(n *yaml.Node, key string) (float64, bool) {
	s, ok := d.stringField(n, key)
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		d.addNode(n, key, fmt.Sprintf("must be a number, got %q", s))
		return 0, false
	}
	return v, true
}

func (d *decoder) intField(n *yaml.Node, key string) (int, bool) {
	s, ok := d.stringField(n, key)
	if !ok {
		return 0, false
	}
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		d.addNode(n, key, fmt.Sprintf("must be an integer, got %q", s))
		return 0, false
	}
	return v, true
}

func (d *decoder) durationField(n *yaml.Node, key string) (time.Duration, bool) {
	s, ok := d.stringField(n, key)
	if !ok {
		return 0, false
	}
	v, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		d.addNode(n, key, fmt.Sprintf("must be a Go duration such as 60s, got %q", s))
		return 0, false
	}
	return v, true
}

func parseClock(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	for _, i := range []int{0, 1, 3, 4} {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	hh, _ := strconv.Atoi(s[0:2])
	mm, _ := strconv.Atoi(s[3:5])
	if hh > 23 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}
