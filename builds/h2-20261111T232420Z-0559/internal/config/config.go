// Package config loads, decodes and validates heatctl's YAML configuration.
package config

import (
	"fmt"
	"strings"
	"time"
)

// Unit is the temperature unit used throughout a configuration.
type Unit string

const (
	Celsius    Unit = "C"
	Fahrenheit Unit = "F"
)

// Valid reports whether u is one of the supported units.
func (u Unit) Valid() bool { return u == Celsius || u == Fahrenheit }

// Symbol returns the unit as it is shown to operators.
func (u Unit) Symbol() string {
	if u == Fahrenheit {
		return "°F"
	}
	return "°C"
}

// TargetLimits returns the inclusive range a target may take in unit u.
func TargetLimits(u Unit) (min, max float64) {
	if u == Fahrenheit {
		return 45, 77
	}
	return 7, 25
}

// DefaultHysteresis returns the default hysteresis for unit u.
func DefaultHysteresis(u Unit) float64 {
	if u == Fahrenheit {
		return 1.0
	}
	return 0.5
}

// Defaults for the optional control settings.
const (
	DefaultCycleInterval     = 60 * time.Second
	DefaultStaleAfter        = 45 * time.Minute
	DefaultMinSwitchInterval = 5 * time.Minute
	DefaultRelayLease        = 15 * time.Minute
	DefaultStartupGrace      = 2 * time.Minute
	DefaultConfirmTimeout    = 30 * time.Second
)

// Config is a validated heatctl configuration.
type Config struct {
	// Path is the file the configuration was read from.
	Path string

	Unit    Unit
	MQTT    MQTTConfig
	Control Control
	Zones   []Zone
}

// MQTTConfig holds the broker connection settings.
type MQTTConfig struct {
	Broker           string
	ClientID         string
	Username         string
	Password         string
	Zigbee2MQTTTopic string
}

// Control holds the tunable control-loop settings.
type Control struct {
	CycleInterval     time.Duration
	Hysteresis        float64
	StaleAfter        time.Duration
	MinSwitchInterval time.Duration
	RelayLease        time.Duration
	StartupGrace      time.Duration
	ConfirmTimeout    time.Duration
}

// Zone is a part of the house with exactly one relay.
type Zone struct {
	ID    string
	Relay Relay
	Rooms []Room
}

// Relay identifies one switch channel on a device.
type Relay struct {
	Device string
	Switch int
}

// ID returns a stable identifier for the relay channel.
func (r Relay) ID() string { return fmt.Sprintf("%s:%d", r.Device, r.Switch) }

// String returns the relay in the conventional device/switch form.
func (r Relay) String() string { return fmt.Sprintf("%s/%d", r.Device, r.Switch) }

// CommandTopic is the topic commands are published to.
func (r Relay) CommandTopic() string { return fmt.Sprintf("%s/command/switch:%d", r.Device, r.Switch) }

// StatusTopic is the topic the device reports its state on.
func (r Relay) StatusTopic() string { return fmt.Sprintf("%s/status/switch:%d", r.Device, r.Switch) }

// OnlineTopic is the topic the device publishes its availability on.
func (r Relay) OnlineTopic() string { return r.Device + "/online" }

// Room is a part of a zone with zero or more temperature sensors.
type Room struct {
	ID     string
	ZoneID string

	// Target is the fixed target temperature, or nil.
	Target *float64
	// Schedule holds the weekly target schedule. It is empty when Target is set.
	Schedule []ScheduleEntry
	Sensors  []Sensor
}

// HasSensors reports whether the room has at least one configured sensor.
func (r *Room) HasSensors() bool { return len(r.Sensors) > 0 }

// TargetAt returns the target temperature that applies at now. The bool is
// false when the room has neither a fixed target nor a schedule.
//
// A fixed target always applies. Otherwise the entry that most recently
// started is used: the latest occurrence, on a day it applies to, at or before
// now, looking back across days and across the week.
func (r *Room) TargetAt(now time.Time, loc *time.Location) (float64, bool) {
	if r.Target != nil {
		return *r.Target, true
	}
	if len(r.Schedule) == 0 {
		return 0, false
	}
	if loc == nil {
		loc = time.Local
	}
	now = now.In(loc)

	var (
		best       time.Time
		bestTarget float64
		found      bool
	)
	// Every entry applies on at least one day, so eight days back is always
	// enough to find an occurrence, even around a DST change.
	for back := 0; back <= 8; back++ {
		day := now.AddDate(0, 0, -back)
		y, m, d := day.Date()
		for i := range r.Schedule {
			e := &r.Schedule[i]
			if !e.Applies(day.Weekday()) {
				continue
			}
			at := time.Date(y, m, d, e.From/60, e.From%60, 0, 0, loc)
			if at.After(now) {
				continue
			}
			if !found || at.After(best) {
				best, bestTarget, found = at, e.Target, true
			}
		}
	}
	return bestTarget, found
}

// SensorKind distinguishes the supported sensor integrations.
type SensorKind int

const (
	// ShellySensor is a Shelly temperature channel.
	ShellySensor SensorKind = iota
	// ZigbeeSensor is a sensor published by Zigbee2MQTT.
	ZigbeeSensor
)

// Sensor identifies one temperature reading channel.
type Sensor struct {
	Kind SensorKind

	// Shelly sensors only.
	Device    string
	Component int

	// Zigbee sensors only.
	Zigbee string
}

// ID returns a stable identifier for the sensor.
func (s Sensor) ID() string {
	if s.Kind == ZigbeeSensor {
		return "zigbee:" + s.Zigbee
	}
	return fmt.Sprintf("shelly:%s:%d", s.Device, s.Component)
}

// String returns a human readable form of the identifier.
func (s Sensor) String() string {
	if s.Kind == ZigbeeSensor {
		return "zigbee " + s.Zigbee
	}
	return fmt.Sprintf("shelly %s temperature:%d", s.Device, s.Component)
}

// Topic returns the MQTT topic the sensor reading arrives on.
func (s Sensor) Topic(zigbeeBase string) string {
	if s.Kind == ZigbeeSensor {
		return strings.TrimSuffix(zigbeeBase, "/") + "/" + s.Zigbee
	}
	return fmt.Sprintf("%s/status/temperature:%d", s.Device, s.Component)
}

// ScheduleEntry is one target in a weekly schedule.
type ScheduleEntry struct {
	// From is the local time of day the entry starts at, in minutes after
	// midnight.
	From int
	// Target is the target temperature while the entry is in force.
	Target float64
	// Days selects the days the entry applies on. Zero means every day.
	Days DaySet
}

// Applies reports whether the entry is in force on the given weekday.
func (e ScheduleEntry) Applies(weekday time.Weekday) bool {
	return e.Days == 0 || e.Days.Has(weekday)
}

// FromString formats From as HH:MM.
func (e ScheduleEntry) FromString() string {
	return fmt.Sprintf("%02d:%02d", e.From/60, e.From%60)
}

// DaySet is a set of weekdays, one bit per day (Monday is bit 0).
type DaySet uint8

// Day constants in the order they appear in YAML.
const (
	Monday DaySet = 1 << iota
	Tuesday
	Wednesday
	Thursday
	Friday
	Saturday
	Sunday
)

var dayNames = []struct {
	name string
	bit  DaySet
	day  time.Weekday
}{
	{"mon", Monday, time.Monday},
	{"tue", Tuesday, time.Tuesday},
	{"wed", Wednesday, time.Wednesday},
	{"thu", Thursday, time.Thursday},
	{"fri", Friday, time.Friday},
	{"sat", Saturday, time.Saturday},
	{"sun", Sunday, time.Sunday},
}

// ParseDay parses one of the names used in a configuration.
func ParseDay(s string) (DaySet, bool) {
	for _, d := range dayNames {
		if d.name == s {
			return d.bit, true
		}
	}
	return 0, false
}

// Bit returns the DaySet bit for a weekday.
func Bit(weekday time.Weekday) DaySet {
	for _, d := range dayNames {
		if d.day == weekday {
			return d.bit
		}
	}
	return 0
}

// Has reports whether the set contains the given weekday.
func (d DaySet) Has(weekday time.Weekday) bool {
	return d&Bit(weekday) != 0
}

// Names returns the names of the days in the set, in week order.
func (d DaySet) Names() []string {
	if d == 0 {
		return []string{"every day"}
	}
	var names []string
	for _, day := range dayNames {
		if d&day.bit != 0 {
			names = append(names, day.name)
		}
	}
	return names
}

// Rooms returns every room in the house in configuration order.
func (c *Config) Rooms() []*Room {
	var rooms []*Room
	for i := range c.Zones {
		for j := range c.Zones[i].Rooms {
			rooms = append(rooms, &c.Zones[i].Rooms[j])
		}
	}
	return rooms
}

// Room returns the room with the given id.
func (c *Config) Room(id string) (*Room, bool) {
	for i := range c.Zones {
		for j := range c.Zones[i].Rooms {
			if c.Zones[i].Rooms[j].ID == id {
				return &c.Zones[i].Rooms[j], true
			}
		}
	}
	return nil, false
}

// AllSensors returns every configured sensor together with its room.
func (c *Config) AllSensors() []SensorRef {
	var refs []SensorRef
	for i := range c.Zones {
		for j := range c.Zones[i].Rooms {
			room := &c.Zones[i].Rooms[j]
			for k := range room.Sensors {
				refs = append(refs, SensorRef{Sensor: room.Sensors[k], RoomID: room.ID})
			}
		}
	}
	return refs
}

// SensorRef pairs a sensor with the room it is listed under.
type SensorRef struct {
	Sensor Sensor
	RoomID string
}
