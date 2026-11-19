// Command heatctl controls the heating of a residential home.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/enrico/heatctl/internal/buildinfo"
	"github.com/enrico/heatctl/internal/config"
	"github.com/enrico/heatctl/internal/control"
	"github.com/enrico/heatctl/internal/logx"
	"github.com/enrico/heatctl/internal/mqtt"
	"github.com/enrico/heatctl/internal/status"
)

const usage = `heatctl — control the heating of a home over MQTT

Usage:
  heatctl <operation> [flags]

Operations:
  run        run the controller in the foreground
  validate   check a configuration file and exit
  status     show the state of a running controller
  version    print the version

Flags:
  run       --config PATH   configuration file (required, or as the only argument)
            --socket PATH   status socket (default: $HEATCTL_SOCKET or the runtime dir)
  validate  --config PATH   configuration file (required, or as the only argument)
  status    --socket PATH   status socket (default: same as run)
            --json          print the status as JSON

Examples:
  heatctl validate /etc/heatctl/config.yaml
  heatctl run --config /etc/heatctl/config.yaml
  heatctl status
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:], stdout, stderr)
	case "validate":
		return cmdValidate(args[1:], stdout, stderr)
	case "status":
		return cmdStatus(args[1:], stdout, stderr)
	case "version", "--version", "-version":
		if len(args) > 1 {
			fmt.Fprintf(stderr, "heatctl: version takes no arguments\n")
			return 2
		}
		fmt.Fprintln(stdout, buildinfo.String())
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "heatctl: unknown operation %q\n\n%s", args[0], usage)
		return 2
	}
}

// configPath resolves the --config flag, which may also be given as a single
// positional argument.
func configPath(fs *flag.FlagSet, positional []string) (string, error) {
	path := fs.Lookup("config").Value.String()
	if len(positional) > 1 {
		return "", fmt.Errorf("unexpected arguments: %v", positional[1:])
	}
	if len(positional) == 1 {
		if path != "" && path != positional[0] {
			return "", errors.New("configuration given twice")
		}
		path = positional[0]
	}
	if path == "" {
		return "", errors.New("a configuration file is required (--config PATH)")
	}
	return path, nil
}

func printConfigErrors(w io.Writer, path string, errs []error) {
	fmt.Fprintf(w, "heatctl: configuration %s is not valid:\n", path)
	for _, err := range errs {
		fmt.Fprintf(w, "  %v\n", err)
	}
}

func cmdValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	// The flag package cannot see the positional argument before parsing, so
	// find it after parsing instead of relying on fs.Args during Parse.
	path, err := parseConfigFlag(fs, args)
	if err != nil {
		fmt.Fprintf(stderr, "heatctl: %v\n", err)
		return 2
	}
	cfg, errs := config.Load(path)
	if len(errs) > 0 {
		printConfigErrors(stderr, path, errs)
		return 1
	}
	fmt.Fprintf(stdout, "%s: configuration is valid (unit %s, %d zones, %d rooms, %d sensors)\n",
		path, cfg.Unit, len(cfg.Zones), len(cfg.Rooms()), len(cfg.AllSensors()))
	return 0
}

// parseConfigFlag parses the flags of validate, allowing the configuration as
// the single positional argument.
func parseConfigFlag(fs *flag.FlagSet, args []string) (string, error) {
	fs.String("config", "", "configuration file")
	var positional []string
	var rest []string
	for _, a := range args {
		if a == "--" {
			continue
		}
		rest = append(rest, a)
	}
	if err := fs.Parse(rest); err != nil {
		return "", err
	}
	positional = fs.Args()
	return configPath(fs, positional)
}

func cmdRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.String("config", "", "configuration file")
	socketFlag := fs.String("socket", "", "status socket path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path, err := configPath(fs, fs.Args())
	if err != nil {
		fmt.Fprintf(stderr, "heatctl: %v\n", err)
		return 2
	}

	cfg, errs := config.Load(path)
	if len(errs) > 0 {
		// X2: an invalid configuration is reported without touching the
		// broker.
		printConfigErrors(stderr, path, errs)
		return 1
	}

	// A closed standard output must never affect control (L3, F8.AC2).
	signal.Ignore(syscall.SIGPIPE)

	logger := logx.New(stdout)
	defer logger.Close()

	socketPath := *socketFlag
	if socketPath == "" {
		socketPath = status.DefaultSocketPath()
	}

	ctrl := control.New(cfg, logger, func(h mqtt.Handlers) mqtt.Client {
		return mqtt.New(mqtt.Options{
			Broker:   cfg.MQTT.Broker,
			ClientID: cfg.MQTT.ClientID,
			Username: cfg.MQTT.Username,
			Password: cfg.MQTT.Password,
		}, h)
	})

	server, err := status.Listen(socketPath)
	switch {
	case err == nil:
		defer server.Close()
		go server.Serve(ctrl.Snapshot)
	case errors.Is(err, status.ErrAlreadyRunning):
		fmt.Fprintf(stderr, "heatctl: %v\n", err)
		return 1
	default:
		logger.Event("status_socket_unavailable", logx.Fields{"path": socketPath, "error": err.Error()})
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := ctrl.Run(ctx); err != nil {
		logger.Event("fatal", logx.Fields{"error": err.Error()})
		fmt.Fprintf(stderr, "heatctl: %v\n", err)
		return 1
	}
	return 0
}

func cmdStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socketFlag := fs.String("socket", "", "status socket path")
	jsonFlag := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "heatctl: status takes no arguments, got %v\n", fs.Args())
		return 2
	}
	path := *socketFlag
	if path == "" {
		path = status.DefaultSocketPath()
	}
	snap, err := status.Fetch(path, 2*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "heatctl: %v\n", err)
		return 1
	}
	if *jsonFlag {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(snap); err != nil {
			fmt.Fprintf(stderr, "heatctl: %v\n", err)
			return 1
		}
		return 0
	}
	if err := status.Format(stdout, snap); err != nil {
		fmt.Fprintf(stderr, "heatctl: %v\n", err)
		return 1
	}
	return 0
}
