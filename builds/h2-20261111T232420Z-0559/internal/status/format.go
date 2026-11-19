package status

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/enrico/heatctl/internal/control"
)

// Format writes a human readable status report.
func Format(w io.Writer, s *control.Snapshot) error {
	if s == nil {
		return fmt.Errorf("no status")
	}
	unit := s.Unit
	state := "waiting for startup conditions"
	switch {
	case s.Started:
		state = "running control cycles"
	case !s.Connected:
		state = "not connected to the broker"
	}
	fmt.Fprintf(w, "unit %s, %s, updated %s\n\n", unit, state, s.TS)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ZONES")
	fmt.Fprintf(tw, "  ID\tRELAY\tDESIRED\tREPORTED\tDEGRADED\n")
	for _, z := range s.Zones {
		degraded := "no"
		if z.Degraded {
			degraded = "yes"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", z.ID, z.Relay, z.Desired, z.Reported, degraded)
	}
	fmt.Fprintln(tw, "\nROOMS")
	degrees := unit
	if unit == "C" || unit == "F" {
		degrees = "°" + unit
	}
	fmt.Fprintf(tw, "  ID\tZONE\tTEMPERATURE (%s)\tTARGET (%s)\tDEMAND\n", degrees, degrees)
	for _, r := range s.Rooms {
		temp := "-"
		if r.Temperature != nil {
			temp = fmt.Sprintf("%.2f", *r.Temperature)
		}
		target := "-"
		if r.Target != nil {
			target = fmt.Sprintf("%.2f", *r.Target)
		}
		demand := "off"
		if r.Demand {
			demand = "on"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", r.ID, r.Zone, temp, target, demand)
	}
	return tw.Flush()
}
