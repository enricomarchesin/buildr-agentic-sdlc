package control

import "time"

// CycleEvent is the L1 control cycle event, encoded as one JSON object.
type CycleEvent struct {
	Event  string      `json:"event"`
	Schema int         `json:"schema"`
	TS     string      `json:"ts"`
	Unit   string      `json:"unit"`
	Seq    int64       `json:"seq"`
	Rooms  []CycleRoom `json:"rooms"`
	Zones  []CycleZone `json:"zones"`
}

// CycleRoom is one room in a control cycle event.
type CycleRoom struct {
	ID          string    `json:"id"`
	Zone        string    `json:"zone"`
	Readings    []float64 `json:"readings"`
	Temperature *float64  `json:"temperature"`
	Target      *float64  `json:"target"`
	Demand      bool      `json:"demand"`
}

// CycleZone is one zone in a control cycle event.
type CycleZone struct {
	ID       string `json:"id"`
	Relay    string `json:"relay"`
	Desired  string `json:"desired"`
	Reported string `json:"reported"`
	Degraded bool   `json:"degraded"`
}

// Snapshot is the state the status operation shows.
type Snapshot struct {
	Schema    int            `json:"schema"`
	TS        string         `json:"ts"`
	Unit      string         `json:"unit"`
	Started   bool           `json:"started"`
	Connected bool           `json:"connected"`
	Rooms     []RoomSnapshot `json:"rooms"`
	Zones     []ZoneSnapshot `json:"zones"`
}

// RoomSnapshot is the status of one room.
type RoomSnapshot struct {
	ID          string           `json:"id"`
	Zone        string           `json:"zone"`
	Temperature *float64         `json:"temperature"`
	Target      *float64         `json:"target"`
	Demand      bool             `json:"demand"`
	Sensors     []SensorSnapshot `json:"sensors"`
}

// SensorSnapshot is the status of one sensor.
type SensorSnapshot struct {
	ID      string   `json:"id"`
	Reading *float64 `json:"reading"`
	AgeS    *float64 `json:"age_s"`
	Fresh   bool     `json:"fresh"`
}

// ZoneSnapshot is the status of one zone.
type ZoneSnapshot struct {
	ID       string `json:"id"`
	Relay    string `json:"relay"`
	Desired  string `json:"desired"`
	Reported string `json:"reported"`
	Degraded bool   `json:"degraded"`
	Online   *bool  `json:"online"`
}

func (c *Controller) publishSnapshot(now time.Time) {
	snap := &Snapshot{
		Schema:    1,
		TS:        now.UTC().Format(time.RFC3339),
		Unit:      string(c.cfg.Unit),
		Started:   c.started,
		Connected: c.connected,
		Rooms:     make([]RoomSnapshot, 0, len(c.rooms)),
		Zones:     make([]ZoneSnapshot, 0, len(c.zones)),
	}
	for _, room := range c.rooms {
		rs := RoomSnapshot{
			ID:      room.cfg.ID,
			Zone:    room.cfg.ZoneID,
			Demand:  room.demand,
			Sensors: make([]SensorSnapshot, 0, len(room.sensors)),
		}
		if t := c.roomTemperature(room, now); t != nil {
			rs.Temperature = t
		}
		// A room without a temperature does not vote (B5).
		if rs.Temperature == nil {
			rs.Demand = false
		}
		if t, ok := room.cfg.TargetAt(now, c.loc); ok {
			target := t
			rs.Target = &target
		}
		for _, s := range room.sensors {
			ss := SensorSnapshot{ID: s.cfg.ID()}
			if s.last != nil {
				v := s.last.Value
				age := now.Sub(s.last.At).Seconds()
				ss.Reading = &v
				ss.AgeS = &age
				ss.Fresh = now.Sub(s.last.At) <= c.cfg.Control.StaleAfter
			}
			rs.Sensors = append(rs.Sensors, ss)
		}
		snap.Rooms = append(snap.Rooms, rs)
	}
	for _, z := range c.zones {
		snap.Zones = append(snap.Zones, ZoneSnapshot{
			ID:       z.cfg.ID,
			Relay:    z.cfg.Relay.String(),
			Desired:  onOff(z.desired),
			Reported: reportedString(z.relay.reported),
			Degraded: c.zoneDegraded(z, now),
			Online:   z.relay.online,
		})
	}
	c.snapshot.Store(snap)
}
