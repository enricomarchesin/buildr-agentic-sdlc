// Package config loads and validates the heatctl YAML configuration.
package config

import (
	"fmt"
	"os"
	"time"
)

// Unit is the temperature unit of a configuration.
type Unit string

const (
	Celsius    Unit = "C"
	Fahrenheit Unit = "F"
)

// TargetRange returns the inclusive range allowed for targets in unit u.
func (u Unit) TargetRange() (lo, hi float64) {
	if u == Fahrenheit {
		return 45.0, 77.0
	}
	return 7.0, 25.0
}

// DefaultHysteresis returns the default hysteresis for unit u.
func (u Unit) DefaultHysteresis() float64 {
	if u == Fahrenheit {
		return 1.0
	}
	return 0.5
}

// FromCelsius converts a reading in °C to unit u.
func (u Unit) FromCelsius(c float64) float64 {
	if u == Fahrenheit {
		return c*9/5 + 32
	}
	return c
}

// Config is a validated heatctl configuration.
type Config struct {
	Unit    Unit
	MQTT    MQTT
	Control Control
	Zones   []Zone
}

// MQTT holds the broker settings.
type MQTT struct {
	Broker          string
	ClientID        string
	Username        string
	Password        string
	Zigbee2MQTTBase string
}

// Control holds the control loop parameters.
type Control struct {
	CycleInterval     time.Duration
	Hysteresis        float64
	StaleAfter        time.Duration
	MinSwitchInterval time.Duration
	RelayLease        time.Duration
	StartupGrace      time.Duration
	ConfirmTimeout    time.Duration
}

// Zone is a part of the house switched by one relay.
type Zone struct {
	ID    string
	Relay Relay
	Rooms []Room
}

// Relay is one switch channel of a Shelly device.
type Relay struct {
	Device string
	Switch int
}

// CommandTopic is the topic relay commands are published on.
func (r Relay) CommandTopic() string { return fmt.Sprintf("%s/command/switch:%d", r.Device, r.Switch) }

// StatusTopic is the topic the relay publishes its status on.
func (r Relay) StatusTopic() string { return fmt.Sprintf("%s/status/switch:%d", r.Device, r.Switch) }

// OnlineTopic is the device's availability topic.
func (r Relay) OnlineTopic() string { return r.Device + "/online" }

func (r Relay) String() string { return fmt.Sprintf("%s switch:%d", r.Device, r.Switch) }

// Room belongs to exactly one zone.
type Room struct {
	ID       string
	Target   *float64 // fixed target, nil if none
	Schedule []Entry  // weekly schedule, empty if none
	Sensors  []Sensor
}

// HasTarget reports whether the room has a fixed target or a schedule.
func (r Room) HasTarget() bool { return r.Target != nil || len(r.Schedule) > 0 }

// Sensor is either a Shelly temperature channel or a Zigbee2MQTT sensor.
type Sensor struct {
	Device    string // Shelly device topic prefix, empty for Zigbee
	Component int
	Zigbee    string // Zigbee2MQTT friendly name, empty for Shelly
}

// IsZigbee reports whether the sensor is published by Zigbee2MQTT.
func (s Sensor) IsZigbee() bool { return s.Zigbee != "" }

// Topic returns the MQTT topic the sensor's readings arrive on.
func (s Sensor) Topic(zigbeeBase string) string {
	if s.IsZigbee() {
		return zigbeeBase + "/" + s.Zigbee
	}
	return fmt.Sprintf("%s/status/temperature:%d", s.Device, s.Component)
}

func (s Sensor) String() string {
	if s.IsZigbee() {
		return "zigbee:" + s.Zigbee
	}
	return fmt.Sprintf("%s temperature:%d", s.Device, s.Component)
}

// Entry is one schedule entry.
type Entry struct {
	From   string  // HH:MM as written
	Minute int     // minutes after local midnight
	Target float64 // in the configured unit
	Days   [7]bool // indexed by time.Weekday; all true when days is omitted
}

// Load reads and validates the configuration at path.
func Load(path string) (*Config, []error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, []error{err}
	}
	return Parse(data)
}
