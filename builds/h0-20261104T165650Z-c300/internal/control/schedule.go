package control

import (
	"time"

	"heatctl/internal/config"
)

// ScheduleTarget returns the target of the entry that most recently started
// at or before now, searching back across the week.
func ScheduleTarget(sched []config.Entry, now time.Time) (float64, bool) {
	cur := now.Hour()*60 + now.Minute()
	for off := 0; off <= 7; off++ {
		day := int((now.Weekday() + 7 - time.Weekday(off%7)) % 7)
		best, found := -1, 0.0
		for _, e := range sched {
			if !e.Days[day] {
				continue
			}
			if off == 0 && e.Minute > cur {
				continue
			}
			if off == 7 && e.Minute <= cur {
				continue // already considered on offset 0
			}
			if e.Minute > best {
				best, found = e.Minute, e.Target
			}
		}
		if best >= 0 {
			return found, true
		}
	}
	return 0, false
}
