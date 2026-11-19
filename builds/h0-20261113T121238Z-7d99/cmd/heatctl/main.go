// Command heatctl controls the heating of a home through MQTT relays and
// temperature sensors.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
)

// version is set at build time with -ldflags "-X main.version=1.2.3".
var version = "dev"

func getVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}

const usage = `heatctl controls home heating zones through MQTT relays and sensors.

Usage:
  heatctl run      [-socket PATH] CONFIG   run the controller in the foreground
  heatctl validate CONFIG                  check a configuration file
  heatctl status   [-socket PATH] [-json]  show the state of the running controller
  heatctl version                          print the version
  heatctl help                             show this help

CONFIG may also be given as -config PATH. The status socket defaults to
` + "$HEATCTL_SOCKET" + ` or /run/heatctl/heatctl.sock.

Run 'heatctl <command> -h' for the flags of a command.
`

func main() {
	ensureStdio()
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}

func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:], stderr)
	case "validate":
		return cmdValidate(args[1:], stdout, stderr)
	case "status":
		return cmdStatus(args[1:], stdout, stderr)
	case "version", "-version", "--version":
		fmt.Fprintf(stdout, "heatctl %s\n", getVersion())
		return 0
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "heatctl: unknown command %q\n\n%s", args[0], usage)
	return 2
}
