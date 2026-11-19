package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/heatctl/heatctl/internal/config"
	"github.com/heatctl/heatctl/internal/control"
	"github.com/heatctl/heatctl/internal/statussock"
)

func cmdValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "configuration file")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: heatctl validate CONFIG")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path, err := configArg(fs, *cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "heatctl validate: %v\n", err)
		fs.Usage()
		return 2
	}
	cfg, errs := config.Load(path)
	if len(errs) > 0 {
		printIssues(stdout, path, errs)
		return 1
	}
	rooms, sensors := 0, 0
	for _, z := range cfg.Zones {
		rooms += len(z.Rooms)
		for _, r := range z.Rooms {
			sensors += len(r.Sensors)
		}
	}
	fmt.Fprintf(stdout, "%s: OK (unit %s, %d zones, %d rooms, %d sensors)\n", path, cfg.Unit, len(cfg.Zones), rooms, sensors)
	return 0
}

func cmdStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", defaultSocket(), "status socket path")
	asJSON := fs.Bool("json", false, "print the raw JSON status")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: heatctl status [-socket PATH] [-json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	data, err := statussock.Query(*socket)
	if err != nil {
		if errors.Is(err, statussock.ErrNotRunning) {
			fmt.Fprintf(stderr, "heatctl is not running: %v\n", err)
		} else {
			fmt.Fprintf(stderr, "heatctl status: %v\n", err)
		}
		return 1
	}
	if *asJSON {
		stdout.Write(data)
		return 0
	}
	var st control.Status
	if err := json.Unmarshal(data, &st); err != nil {
		fmt.Fprintf(stderr, "heatctl status: unreadable answer: %v\n", err)
		return 1
	}
	printStatus(stdout, st)
	return 0
}

func printStatus(w io.Writer, st control.Status) {
	broker := "connected"
	if !st.Connected {
		broker = "NOT CONNECTED"
	}
	fmt.Fprintf(w, "heatctl %s, up %s, broker %s, unit °%s, at %s\n", st.Version, st.Uptime, broker, st.Unit, st.TS)
	if !st.Started {
		fmt.Fprintln(w, "starting: waiting for every room to have a temperature or for startup_grace; no relay commands yet")
	}
	for _, z := range st.Zones {
		fmt.Fprintln(w)
		degraded := "no"
		if z.Degraded {
			degraded = "YES"
		}
		pending := ""
		if z.Pending {
			pending = " (switch pending: min_switch_interval)"
		}
		fmt.Fprintf(w, "zone %s  relay %s\n", z.ID, z.Relay)
		fmt.Fprintf(w, "  desired %s%s, commanded %s, reported %s, relay %s, degraded %s\n",
			z.Desired, pending, z.Commanded, z.Reported, z.Online, degraded)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  ROOM\tTEMPERATURE\tTARGET\tDEMAND\tSENSORS")
		for _, r := range z.Rooms {
			demand := "no"
			if r.Demand {
				demand = "yes"
			}
			fresh := 0
			for _, s := range r.Sensors {
				if s.Fresh {
					fresh++
				}
			}
			sensors := "none"
			if len(r.Sensors) > 0 {
				sensors = fmt.Sprintf("%d/%d fresh", fresh, len(r.Sensors))
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", r.ID, num(r.Temperature), num(r.Target), demand, sensors)
		}
		tw.Flush()
		for _, r := range z.Rooms {
			for _, s := range r.Sensors {
				if s.Reading == nil {
					fmt.Fprintf(w, "    %s / %s: no reading yet\n", r.ID, s.Sensor)
					continue
				}
				state := "fresh"
				if !s.Fresh {
					state = "STALE"
				}
				fmt.Fprintf(w, "    %s / %s: %s, %ss ago, %s\n", r.ID, s.Sensor, num(s.Reading), strconv.FormatInt(*s.AgeSeconds, 10), state)
			}
		}
	}
}

func num(v *float64) string {
	if v == nil {
		return "-"
	}
	s := strconv.FormatFloat(*v, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	if strings.HasSuffix(s, ".") {
		s += "0"
	}
	return s
}
