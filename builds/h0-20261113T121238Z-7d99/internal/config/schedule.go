package config

import "time"

// TargetAt returns the room's target at local time t, and false if the room
// has neither a target nor a schedule.
//
// A schedule uses the entry that most recently started: the latest
// occurrence, on a day the entry applies to, at or before t. The search goes
// back across days and across the week.
func (r Room) TargetAt(t time.Time) (float64, bool) {
	if r.Target != nil {
		return *r.Target, true
	}
	if len(r.Schedule) == 0 {
		return 0, false
	}
	y, m, d := t.Date()
	loc := t.Location()
	// Eight days back covers an entry that applies on one weekday only and
	// starts later in the day than t.
	for back := 0; back <= 7; back++ {
		day := time.Date(y, m, d-back, 12, 0, 0, 0, loc)
		var best time.Time
		var target float64
		bestMinute := -1
		found := false
		for _, e := range r.Schedule {
			if !e.Days[day.Weekday()] {
				continue
			}
			start := time.Date(y, m, d-back, e.Minute/60, e.Minute%60, 0, 0, loc)
			if start.After(t) {
				continue
			}
			// On a DST change two entries can start at the same instant;
			// the one written later in the day wins.
			if !found || start.After(best) || (start.Equal(best) && e.Minute > bestMinute) {
				best, target, bestMinute, found = start, e.Target, e.Minute, true
			}
		}
		if found {
			return target, true
		}
	}
	return 0, false
}
