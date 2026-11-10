// Package config loads and validates the heatctl YAML configuration.
package config

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Unit    string
	MQTT    MQTT
	Control Control
	Zones   []Zone
}

type MQTT struct {
	Broker           string
	ClientID         string
	Username         string
	Password         string
	Zigbee2MQTTTopic string
}

type Control struct {
	CycleInterval     time.Duration
	Hysteresis        float64
	StaleAfter        time.Duration
	MinSwitchInterval time.Duration
	RelayLease        time.Duration
	StartupGrace      time.Duration
	ConfirmTimeout    time.Duration
}

type Zone struct {
	ID    string
	Relay Relay
	Rooms []Room
}

type Relay struct {
	Device string
	Switch int
}

type Room struct {
	ID       string
	Target   *float64
	Schedule []Entry
	Sensors  []Sensor
}

// Entry is one schedule entry. Days is indexed by time.Weekday.
type Entry struct {
	Minute int // minutes after local midnight
	Days   [7]bool
	Target float64
}

// Sensor is a Shelly channel (Device, Component) or a Zigbee2MQTT sensor (Zigbee).
type Sensor struct {
	Device    string
	Component int
	Zigbee    string
}

func (s Sensor) IsZigbee() bool { return s.Zigbee != "" }

// Load reads and validates a configuration file.
func Load(path string) (*Config, []error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, []error{err}
	}
	return Parse(data)
}

type walker struct{ errs []error }

func (w *walker) errf(path, format string, args ...any) {
	w.errs = append(w.errs, fmt.Errorf("%s: %s", path, fmt.Sprintf(format, args...)))
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func (w *walker) obj(v any, path string) map[string]any {
	switch m := v.(type) {
	case map[string]any:
		return m
	case nil:
		w.errf(path, "must be a mapping")
	default:
		w.errf(path, "must be a mapping, got %s", kind(v))
	}
	return nil
}

func kind(v any) string {
	switch v.(type) {
	case string:
		return "a string"
	case int, int64, uint64:
		return "an integer"
	case float64:
		return "a number"
	case bool:
		return "a boolean"
	case []any:
		return "a list"
	case map[string]any:
		return "a mapping"
	}
	return fmt.Sprintf("%T", v)
}

func (w *walker) unknown(m map[string]any, path string, allowed ...string) {
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	var bad []string
	for k := range m {
		if !ok[k] {
			bad = append(bad, k)
		}
	}
	sort.Strings(bad)
	for _, k := range bad {
		w.errf(join(path, k), "unknown key")
	}
}

func has(m map[string]any, key string) bool { return m[key] != nil }

func (w *walker) str(m map[string]any, path, key string, required bool) (string, bool) {
	v := m[key]
	if v == nil {
		if required {
			w.errf(join(path, key), "required")
		}
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		w.errf(join(path, key), "must be a string, got %s", kind(v))
		return "", false
	}
	if required && s == "" {
		w.errf(join(path, key), "must not be empty")
		return "", false
	}
	return s, true
}

func (w *walker) integer(m map[string]any, path, key string, required bool) (int, bool) {
	v := m[key]
	if v == nil {
		if required {
			w.errf(join(path, key), "required")
		}
		return 0, false
	}
	var n int
	switch x := v.(type) {
	case int:
		n = x
	case int64:
		n = int(x)
	default:
		w.errf(join(path, key), "must be an integer, got %s", kind(v))
		return 0, false
	}
	if n < 0 {
		w.errf(join(path, key), "must be 0 or more")
		return 0, false
	}
	return n, true
}

func (w *walker) number(m map[string]any, path, key string) (float64, bool) {
	v := m[key]
	if v == nil {
		return 0, false
	}
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	}
	w.errf(join(path, key), "must be a number, got %s", kind(v))
	return 0, false
}

func (w *walker) duration(m map[string]any, path, key string, def time.Duration) time.Duration {
	v := m[key]
	if v == nil {
		return def
	}
	s, ok := v.(string)
	if !ok {
		w.errf(join(path, key), "must be a duration string such as \"60s\", got %s", kind(v))
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		w.errf(join(path, key), "invalid duration %q", s)
		return def
	}
	if d <= 0 {
		w.errf(join(path, key), "must be positive")
		return def
	}
	return d
}

func (w *walker) list(m map[string]any, path, key string) ([]any, bool) {
	v := m[key]
	if v == nil {
		return nil, false
	}
	l, ok := v.([]any)
	if !ok {
		w.errf(join(path, key), "must be a list, got %s", kind(v))
		return nil, false
	}
	return l, true
}

var dayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// Parse decodes and validates configuration data. It returns every error found.
func Parse(data []byte) (*Config, []error) {
	var raw any
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&raw); err != nil {
		if err.Error() == "EOF" {
			return nil, []error{fmt.Errorf("configuration is empty")}
		}
		return nil, []error{fmt.Errorf("yaml: %w", err)}
	}
	w := &walker{}
	top := w.obj(raw, "config")
	if top == nil {
		return nil, w.errs
	}
	cfg := &Config{}
	w.unknown(top, "", "unit", "mqtt", "control", "zones")

	if u, ok := w.str(top, "", "unit", true); ok {
		if u != "C" && u != "F" {
			w.errf("unit", "must be C or F, got %q", u)
		} else {
			cfg.Unit = u
		}
	}
	w.mqtt(top, cfg)
	w.control(top, cfg)
	w.zones(top, cfg)
	if len(w.errs) > 0 {
		return nil, w.errs
	}
	return cfg, nil
}

func (w *walker) mqtt(top map[string]any, cfg *Config) {
	cfg.MQTT.ClientID = "heatctl"
	cfg.MQTT.Zigbee2MQTTTopic = "zigbee2mqtt"
	if top["mqtt"] == nil {
		w.errf("mqtt", "required")
		return
	}
	m := w.obj(top["mqtt"], "mqtt")
	if m == nil {
		return
	}
	w.unknown(m, "mqtt", "broker", "client_id", "username", "password", "zigbee2mqtt_topic")
	if b, ok := w.str(m, "mqtt", "broker", true); ok {
		u, err := url.Parse(b)
		switch {
		case err != nil || u.Host == "":
			w.errf("mqtt.broker", "must be a URL such as tcp://host:1883, got %q", b)
		case !map[string]bool{"tcp": true, "mqtt": true, "ssl": true, "tls": true, "mqtts": true, "tcps": true, "ws": true, "wss": true}[u.Scheme]:
			w.errf("mqtt.broker", "unsupported scheme %q", u.Scheme)
		}
		cfg.MQTT.Broker = b
	}
	if s, ok := w.str(m, "mqtt", "client_id", false); ok {
		if s == "" {
			w.errf("mqtt.client_id", "must not be empty")
		} else {
			cfg.MQTT.ClientID = s
		}
	}
	cfg.MQTT.Username, _ = w.str(m, "mqtt", "username", false)
	cfg.MQTT.Password, _ = w.str(m, "mqtt", "password", false)
	if s, ok := w.str(m, "mqtt", "zigbee2mqtt_topic", false); ok {
		if s == "" || strings.ContainsAny(s, "+#") {
			w.errf("mqtt.zigbee2mqtt_topic", "must be a non-empty topic without wildcards")
		} else {
			cfg.MQTT.Zigbee2MQTTTopic = strings.TrimRight(s, "/")
		}
	}
}

func (w *walker) control(top map[string]any, cfg *Config) {
	c := &cfg.Control
	m := map[string]any{}
	if top["control"] != nil {
		if mm := w.obj(top["control"], "control"); mm != nil {
			m = mm
		}
	}
	w.unknown(m, "control", "cycle_interval", "hysteresis", "stale_after", "min_switch_interval", "relay_lease", "startup_grace", "confirm_timeout")
	c.CycleInterval = w.duration(m, "control", "cycle_interval", 60*time.Second)
	c.StaleAfter = w.duration(m, "control", "stale_after", 45*time.Minute)
	c.MinSwitchInterval = w.duration(m, "control", "min_switch_interval", 5*time.Minute)
	c.RelayLease = w.duration(m, "control", "relay_lease", 15*time.Minute)
	c.StartupGrace = w.duration(m, "control", "startup_grace", 2*time.Minute)
	c.ConfirmTimeout = w.duration(m, "control", "confirm_timeout", 30*time.Second)
	c.Hysteresis = 0.5
	if cfg.Unit == "F" {
		c.Hysteresis = 1.0
	}
	if h, ok := w.number(m, "control", "hysteresis"); ok {
		if h < 0 {
			w.errf("control.hysteresis", "must be 0 or more")
		} else {
			c.Hysteresis = h
		}
	}
	if c.RelayLease <= c.CycleInterval {
		w.errf("control.relay_lease", "must be longer than control.cycle_interval")
	}
}

func (w *walker) zones(top map[string]any, cfg *Config) {
	zl, ok := w.list(top, "", "zones")
	if !ok {
		if top["zones"] == nil {
			w.errf("zones", "required: at least one zone")
		}
		return
	}
	if len(zl) == 0 {
		w.errf("zones", "must have at least one zone")
	}
	zoneIDs := map[string]bool{}
	roomIDs := map[string]bool{}
	relays := map[Relay]bool{}
	shelly := map[[2]any]bool{}
	zigbee := map[string]bool{}
	for i, zv := range zl {
		zp := fmt.Sprintf("zones[%d]", i)
		zm := w.obj(zv, zp)
		if zm == nil {
			continue
		}
		w.unknown(zm, zp, "id", "relay", "rooms")
		var z Zone
		if id, ok := w.str(zm, zp, "id", true); ok {
			if zoneIDs[id] {
				w.errf(zp+".id", "duplicate zone id %q", id)
			}
			zoneIDs[id] = true
			z.ID = id
		}
		rp := zp + ".relay"
		if zm["relay"] == nil {
			w.errf(rp, "required: each zone has exactly one relay")
		} else if rm := w.obj(zm["relay"], rp); rm != nil {
			w.unknown(rm, rp, "device", "switch")
			dev, ok1 := w.str(rm, rp, "device", true)
			sw, ok2 := w.integer(rm, rp, "switch", true)
			if ok1 && ok2 {
				z.Relay = Relay{dev, sw}
				if relays[z.Relay] {
					w.errf(rp, "relay %s switch %d is already used by another zone", dev, sw)
				}
				relays[z.Relay] = true
			}
		}
		rl, ok := w.list(zm, zp, "rooms")
		if !ok || len(rl) == 0 {
			w.errf(zp+".rooms", "required: at least one room")
		}
		for j, rv := range rl {
			pp := fmt.Sprintf("%s.rooms[%d]", zp, j)
			rm := w.obj(rv, pp)
			if rm == nil {
				continue
			}
			w.unknown(rm, pp, "id", "target", "schedule", "sensors")
			var room Room
			if id, ok := w.str(rm, pp, "id", true); ok {
				if roomIDs[id] {
					w.errf(pp+".id", "duplicate room id %q (room ids are unique across the house)", id)
				}
				roomIDs[id] = true
				room.ID = id
			}
			w.room(rm, pp, cfg.Unit, &room, shelly, zigbee)
			z.Rooms = append(z.Rooms, room)
		}
		cfg.Zones = append(cfg.Zones, z)
	}
}

func (w *walker) room(rm map[string]any, pp, unit string, room *Room, shelly map[[2]any]bool, zigbee map[string]bool) {
	if sl, ok := w.list(rm, pp, "sensors"); ok {
		for k, sv := range sl {
			sp := fmt.Sprintf("%s.sensors[%d]", pp, k)
			sm := w.obj(sv, sp)
			if sm == nil {
				continue
			}
			w.unknown(sm, sp, "device", "component", "zigbee")
			var s Sensor
			switch {
			case has(sm, "zigbee") && (has(sm, "device") || has(sm, "component")):
				w.errf(sp, "a sensor is either {device, component} or {zigbee}, never both")
				continue
			case has(sm, "zigbee"):
				if name, ok := w.str(sm, sp, "zigbee", true); ok {
					if strings.ContainsAny(name, "+#") {
						w.errf(sp+".zigbee", "must not contain + or #")
						continue
					}
					if zigbee[name] {
						w.errf(sp+".zigbee", "sensor %q is already listed under a room", name)
					}
					zigbee[name] = true
					s.Zigbee = name
				} else {
					continue
				}
			default:
				dev, ok1 := w.str(sm, sp, "device", true)
				comp, ok2 := w.integer(sm, sp, "component", true)
				if !ok1 || !ok2 {
					continue
				}
				key := [2]any{dev, comp}
				if shelly[key] {
					w.errf(sp, "sensor %s component %d is already listed under a room", dev, comp)
				}
				shelly[key] = true
				s = Sensor{Device: dev, Component: comp}
			}
			room.Sensors = append(room.Sensors, s)
		}
	}
	lo, hi := 7.0, 25.0
	if unit == "F" {
		lo, hi = 45.0, 77.0
	}
	checkTarget := func(path string, t float64) bool {
		if t < lo || t > hi {
			w.errf(path, "%v is outside %v to %v (unit %s)", t, lo, hi, unit)
			return false
		}
		return true
	}
	hasTarget, hasSched := has(rm, "target"), has(rm, "schedule")
	if hasTarget {
		if t, ok := w.number(rm, pp, "target"); ok && checkTarget(pp+".target", t) {
			room.Target = &t
		}
	}
	if hasSched {
		room.Schedule = w.schedule(rm, pp, checkTarget)
	}
	switch {
	case hasTarget && hasSched:
		w.errf(pp+".target", "a room has either target or schedule, not both")
	case !hasTarget && !hasSched && len(room.Sensors) > 0:
		w.errf(pp, "a room with sensors needs either target or schedule")
	}
}

func (w *walker) schedule(rm map[string]any, pp string, checkTarget func(string, float64) bool) []Entry {
	sl, ok := w.list(rm, pp, "schedule")
	if !ok {
		return nil
	}
	if len(sl) == 0 {
		w.errf(pp+".schedule", "must have at least one entry")
	}
	var out []Entry
	seen := map[[2]int]int{} // (weekday, minute) -> entry index
	for i, ev := range sl {
		ep := fmt.Sprintf("%s.schedule[%d]", pp, i)
		em := w.obj(ev, ep)
		if em == nil {
			continue
		}
		w.unknown(em, ep, "from", "target", "days")
		var e Entry
		good := true
		if from, ok := w.str(em, ep, "from", true); ok {
			t, err := time.Parse("15:04", from)
			if err != nil || len(from) != 5 {
				w.errf(ep+".from", "must be HH:MM (00:00 to 23:59), got %q", from)
				good = false
			}
			e.Minute = t.Hour()*60 + t.Minute()
		} else {
			good = false
		}
		if !has(em, "target") {
			w.errf(ep+".target", "required")
			good = false
		} else if t, ok := w.number(em, ep, "target"); !ok || !checkTarget(ep+".target", t) {
			good = false
		} else {
			e.Target = t
		}
		if has(em, "days") {
			dl, ok := w.list(em, ep, "days")
			if ok && len(dl) == 0 {
				w.errf(ep+".days", "must not be empty; omit it for every day")
			}
			for _, dv := range dl {
				s, _ := dv.(string)
				d, known := dayNames[s]
				switch {
				case !known:
					w.errf(ep+".days", "invalid day %v (use mon, tue, wed, thu, fri, sat, sun)", dv)
					good = false
				case e.Days[d]:
					w.errf(ep+".days", "day %q repeated", s)
					good = false
				default:
					e.Days[d] = true
				}
			}
			if !ok {
				good = false
			}
		} else {
			for d := range e.Days {
				e.Days[d] = true
			}
		}
		if !good {
			continue
		}
		for d := 0; d < 7; d++ {
			if !e.Days[d] {
				continue
			}
			if j, dup := seen[[2]int{d, e.Minute}]; dup {
				w.errf(ep+".from", "overlaps schedule[%d]: both apply on %s at the same time", j, time.Weekday(d).String()[:3])
				break
			}
			seen[[2]int{d, e.Minute}] = i
		}
		out = append(out, e)
	}
	return out
}
