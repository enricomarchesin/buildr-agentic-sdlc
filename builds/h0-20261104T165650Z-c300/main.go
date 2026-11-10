// heatctl controls the heating of a home through an MQTT broker.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"heatctl/internal/bus"
	"heatctl/internal/config"
	"heatctl/internal/control"
	"heatctl/internal/logx"
)

var version = "1.0.0"

const usage = `usage: heatctl <command> [options]

commands:
  run [-config] FILE [-socket PATH]   run the controller in the foreground
  validate [-config] FILE             check a configuration file
  status [-socket PATH] [-json]       show the state of a running controller
  version                             print the version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "run":
		os.Exit(cmdRun(args))
	case "validate":
		os.Exit(cmdValidate(args))
	case "status":
		os.Exit(cmdStatus(args))
	case "version", "-version", "--version":
		fmt.Println("heatctl", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "heatctl: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func defaultSocket() string {
	if p := os.Getenv("HEATCTL_SOCKET"); p != "" {
		return p
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "heatctl.sock")
	}
	return "/run/heatctl/heatctl.sock"
}

func configArg(fs *flag.FlagSet, path *string) bool {
	if *path == "" && fs.NArg() > 0 {
		*path = fs.Arg(0)
	}
	if *path == "" {
		fmt.Fprintln(os.Stderr, "heatctl: a configuration file is required")
		return false
	}
	return true
}

func cmdValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	path := fs.String("config", "", "configuration file")
	if fs.Parse(args) != nil || !configArg(fs, path) {
		return 2
	}
	if _, errs := config.Load(*path); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, "error:", e)
		}
		fmt.Fprintf(os.Stderr, "%s: %d error(s)\n", *path, len(errs))
		return 1
	}
	fmt.Printf("%s: ok\n", *path)
	return 0
}

type pubHolder struct{ p control.Publisher }

func (h *pubHolder) Publish(t, p string) { h.p.Publish(t, p) }

func listen(path string) (net.Listener, error) {
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("another heatctl is already listening on %s", path)
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSocket != 0 {
		os.Remove(path) // stale socket from a crashed run
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	os.Chmod(path, 0o600)
	return l, nil
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	path := fs.String("config", "", "configuration file")
	sock := fs.String("socket", defaultSocket(), "status socket path")
	if fs.Parse(args) != nil || !configArg(fs, path) {
		return 2
	}
	cfg, errs := config.Load(*path)
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, "error:", e)
		}
		return 1
	}

	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE) // a closed stdout must not kill us (L3)
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	log := logx.New(os.Stdout)
	holder := &pubHolder{}
	ctrl := control.New(cfg, holder, log, time.Now())
	events := make(chan bus.Event, 1024)
	client := bus.New(cfg.MQTT, ctrl.Topics(), events)
	holder.p = client

	statusReq := make(chan chan control.Status)
	closeSock := func() {}
	if l, err := listen(*sock); err != nil {
		fmt.Fprintln(os.Stderr, "heatctl: status socket disabled:", err)
		log.Log("status_socket_error", "error", err.Error())
	} else {
		go serveStatus(l, statusReq)
		closeSock = func() { l.Close(); os.Remove(*sock) }
		defer closeSock()
	}

	log.Log("starting", "version", version, "unit", cfg.Unit, "zones", len(cfg.Zones))
	client.Connect()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case ev := <-events:
			now := time.Now()
			switch ev.Kind {
			case bus.Message:
				ctrl.HandleMessage(now, ev.Topic, ev.Payload, ev.Retained)
			case bus.Connected:
				log.Log("mqtt_connected", "broker", cfg.MQTT.Broker)
				ctrl.Connected(now)
			case bus.Disconnected:
				msg := ""
				if ev.Err != nil {
					msg = ev.Err.Error()
				}
				log.Log("mqtt_disconnected", "error", msg)
			}
		case now := <-tick.C:
			ctrl.Tick(now)
		case r := <-statusReq:
			r <- ctrl.Snapshot()
		case s := <-sigs:
			log.Log("stopping", "signal", s.String())
			closeSock()
			go func() { // keep the client's handlers from blocking while we stop
				for range events {
				}
			}()
			shutdown(ctrl, client)
			return 0
		}
	}
}

// shutdown turns every relay off and disconnects, within 5 s (R5).
func shutdown(ctrl *control.Controller, client *bus.Client) {
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		ctrl.Shutdown(func(topic, payload string) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				client.PublishWait(topic, payload, 3*time.Second)
			}()
		})
		wg.Wait()
		client.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3500 * time.Millisecond):
	}
}

func serveStatus(l net.Listener, req chan chan control.Status) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			r := make(chan control.Status, 1)
			select {
			case req <- r:
			case <-time.After(2 * time.Second):
				return
			}
			select {
			case st := <-r:
				c.SetWriteDeadline(time.Now().Add(2 * time.Second))
				json.NewEncoder(c).Encode(st)
			case <-time.After(2 * time.Second):
			}
		}()
	}
}

func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	sock := fs.String("socket", defaultSocket(), "status socket path")
	asJSON := fs.Bool("json", false, "print JSON")
	if fs.Parse(args) != nil {
		return 2
	}
	c, err := net.DialTimeout("unix", *sock, 2*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "heatctl is not running (cannot reach %s)\n", *sock)
		return 1
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	data, err := io.ReadAll(c)
	if err != nil || len(data) == 0 {
		fmt.Fprintf(os.Stderr, "heatctl is not running (no answer on %s)\n", *sock)
		return 1
	}
	if *asJSON {
		os.Stdout.Write(data)
		return 0
	}
	var st control.Status
	if err := json.Unmarshal(data, &st); err != nil {
		fmt.Fprintln(os.Stderr, "heatctl: bad answer:", err)
		return 1
	}
	printStatus(st)
	return 0
}

func fnum(p *float64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%.2f", *p)
}

func yesno(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func printStatus(st control.Status) {
	state := "running"
	if !st.Started {
		state = "starting (no control cycle yet)"
	}
	fmt.Printf("heatctl %s, unit %s\n", state, st.Unit)
	for _, z := range st.Zones {
		fmt.Printf("zone %-12s desired=%-7s reported=%-7s degraded=%s\n", z.ID, z.Desired, z.Reported, yesno(z.Degraded))
		for _, r := range z.Rooms {
			fmt.Printf("  room %-12s temperature=%-7s target=%-7s demand=%s\n", r.ID, fnum(r.Temperature), fnum(r.Target), yesno(r.Demand))
		}
	}
}
