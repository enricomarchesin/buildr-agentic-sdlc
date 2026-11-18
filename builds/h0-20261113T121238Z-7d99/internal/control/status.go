package control

import "time"

// Status is the snapshot served to `heatctl status`.
type Status struct {
	Version   string       `json:"version"`
	TS        string       `json:"ts"`
	Unit      string       `json:"unit"`
	Started   bool         `json:"started"`
	Connected bool         `json:"connected"`
	Uptime    string       `json:"uptime"`
	Zones     []ZoneStatus `json:"zones"`
}

// ZoneStatus describes one zone and its relay.
type ZoneStatus struct {
	ID        string       `json:"id"`
	Relay     string       `json:"relay"`
	Desired   string       `json:"desired"`   // on or off
	Commanded string       `json:"commanded"` // on, off or none
	Reported  string       `json:"reported"`  // on, off or unknown
	Online    string       `json:"online"`    // online, offline or unknown
	Degraded  bool         `json:"degraded"`
	Pending   bool         `json:"pending"` // a switch waits for min_switch_interval
	Rooms     []RoomStatus `json:"rooms"`
}

// RoomStatus describes one room.
type RoomStatus struct {
	ID          string         `json:"id"`
	Temperature *float64       `json:"temperature"`
	Target      *float64       `json:"target"`
	Demand      bool           `json:"demand"`
	Sensors     []SensorStatus `json:"sensors"`
}

// SensorStatus describes one sensor.
type SensorStatus struct {
	Sensor     string   `json:"sensor"`
	Reading    *float64 `json:"reading"`
	AgeSeconds *int64   `json:"age_seconds"`
	Fresh      bool     `json:"fresh"`
}

// Snapshot returns the current state for display. It does not change any
// state.
func (c *Controller) Snapshot(now time.Time, version string) Status {
	st := Status{
		Version:   version,
		TS:        now.UTC().Format(time.RFC3339),
		Unit:      string(c.cfg.Unit),
		Started:   c.started,
		Connected: c.connected,
		Uptime:    now.Sub(c.startAt).Round(time.Second).String(),
		Zones:     []ZoneStatus{},
	}
	for _, z := range c.zones {
		r := z.relay
		zs := ZoneStatus{
			ID:        z.cfg.ID,
			Relay:     r.cfg.String(),
			Desired:   onOff(triOf(z.desired)),
			Commanded: "none",
			Reported:  onOff(r.reported),
			Online:    onlineString(r.online),
			Pending:   r.commandedSet && r.desired != r.commanded,
			Rooms:     []RoomStatus{},
		}
		if r.commandedSet {
			zs.Commanded = onOff(triOf(r.commanded))
		}
		anyTemp := false
		for _, rm := range z.rooms {
			rs := RoomStatus{ID: rm.cfg.ID, Demand: rm.demand, Sensors: []SensorStatus{}}
			if t, ok := c.temperature(rm, now); ok {
				rs.Temperature = ptr(round(t))
				if rm.cfg.HasTarget() {
					anyTemp = true
				}
			}
			if t, ok := c.target(rm, now); ok {
				rs.Target = ptr(round(t))
			}
			for _, s := range rm.sensors {
				ss := SensorStatus{Sensor: s.cfg.String(), Fresh: c.fresh(s, now)}
				if s.has {
					ss.Reading = ptr(round(s.value))
					ss.AgeSeconds = ptr(int64(now.Sub(s.at) / time.Second))
				}
				rs.Sensors = append(rs.Sensors, ss)
			}
			zs.Rooms = append(zs.Rooms, rs)
		}
		zs.Degraded = !anyTemp || r.online == no
		st.Zones = append(st.Zones, zs)
	}
	return st
}
