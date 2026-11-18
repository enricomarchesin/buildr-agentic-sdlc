package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/heatctl/heatctl/internal/config"
	"github.com/heatctl/heatctl/internal/control"
	"github.com/heatctl/heatctl/internal/logx"
	"github.com/heatctl/heatctl/internal/mqttio"
	"github.com/heatctl/heatctl/internal/statussock"
)

// tickInterval is how often timers in the controller are checked.
const tickInterval = 250 * time.Millisecond

// shutdownBudget bounds the whole stop sequence; R5 requires exiting within 5 s.
const shutdownBudget = 4 * time.Second

func defaultSocket() string {
	if s := os.Getenv("HEATCTL_SOCKET"); s != "" {
		return s
	}
	return statussock.DefaultPath
}

// configArg returns the configuration path from -config or the single
// positional argument.
func configArg(fs *flag.FlagSet, flagValue string) (string, error) {
	rest := fs.Args()
	switch {
	case flagValue != "" && len(rest) == 0:
		return flagValue, nil
	case flagValue == "" && len(rest) == 1:
		return rest[0], nil
	case flagValue == "" && len(rest) == 0:
		return "", fmt.Errorf("missing configuration file")
	}
	return "", fmt.Errorf("give exactly one configuration file")
}

func printIssues(w io.Writer, path string, errs []error) {
	for _, err := range errs {
		fmt.Fprintf(w, "%s: %v\n", path, err)
	}
	noun := "errors"
	if len(errs) == 1 {
		noun = "error"
	}
	fmt.Fprintf(w, "%s: %d %s\n", path, len(errs), noun)
}

func cmdRun(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "configuration file")
	socket := fs.String("socket", defaultSocket(), "status socket path (empty to disable)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: heatctl run [-socket PATH] CONFIG")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path, err := configArg(fs, *cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "heatctl run: %v\n", err)
		fs.Usage()
		return 2
	}
	cfg, errs := config.Load(path)
	if len(errs) > 0 {
		// X2: never connect to the broker with an invalid configuration.
		printIssues(stderr, path, errs)
		return 1
	}
	return run(cfg, path, *socket)
}

func run(cfg *config.Config, path, socket string) int {
	log := logx.New(os.Stdout)
	start := time.Now()

	// Signals first, so a SIGTERM during start-up is handled too.
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	events := make(chan mqttio.Event, 1024)
	topics := control.Topics(cfg)
	client := mqttio.New(cfg.MQTT, topics, events, log)
	ctrl := control.New(cfg, log, client, start)

	log.Event("started", "version", getVersion(), "config", path, "unit", string(cfg.Unit),
		"broker", mqttio.Describe(cfg.MQTT), "zones", len(cfg.Zones), "topics", len(topics),
		"startup_grace", cfg.Control.StartupGrace, "pid", os.Getpid())

	var snapshot atomic.Pointer[[]byte]
	publish := func(now time.Time) {
		data, err := json.Marshal(ctrl.Snapshot(now, getVersion()))
		if err != nil {
			return
		}
		data = append(data, '\n')
		snapshot.Store(&data)
	}
	publish(start)

	if socket != "" {
		server, err := statussock.Listen(socket, func() []byte { return *snapshot.Load() })
		if err != nil {
			// Status is a convenience; control goes on without it.
			log.Event("status_socket_failed", "path", socket, "error", err.Error())
		} else {
			defer server.Close()
			log.Event("status_socket", "path", socket)
		}
	}

	client.Connect()

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case sig := <-sigs:
			return shutdown(ctrl, client, log, events, sig)
		case ev := <-events:
			now := time.Now()
			switch ev.Kind {
			case mqttio.Message:
				ctrl.HandleMessage(now, ev.Topic, ev.Payload, ev.Retained)
			case mqttio.Connected:
				log.Event("mqtt_connected", "broker", mqttio.Describe(cfg.MQTT))
				ctrl.OnConnected(now)
			case mqttio.Disconnected:
				msg := ""
				if ev.Err != nil {
					msg = ev.Err.Error()
				}
				log.Event("mqtt_disconnected", "error", msg)
				ctrl.OnDisconnected(now)
			}
			publish(now)
		case now := <-ticker.C:
			ctrl.Tick(now)
			publish(now)
		}
	}
}

// shutdown turns every relay off and exits 0 within the R5 budget.
func shutdown(ctrl *control.Controller, client *mqttio.Client, log *logx.Logger, events <-chan mqttio.Event, sig os.Signal) int {
	deadline := time.Now().Add(shutdownBudget)
	// Last resort: never take longer than the budget, whatever blocks.
	go func() {
		time.Sleep(shutdownBudget + 500*time.Millisecond)
		os.Exit(0)
	}()
	// Keep the broker's delivery goroutine from blocking on a full queue.
	go func() {
		for range events {
		}
	}()
	log.Event("stopping", "signal", sig.String())
	ctrl.Shutdown(time.Now())
	acked := client.Flush(time.Until(deadline) - time.Second)
	client.Disconnect(250 * time.Millisecond)
	log.Event("stopped", "publishes_flushed", acked)
	log.Close(time.Until(deadline))
	return 0
}
