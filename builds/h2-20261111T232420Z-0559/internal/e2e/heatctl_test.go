package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestHeatingEndToEnd(t *testing.T) {
	w := newWorld(t)
	app := w.startHeating(19.4, 22.0)

	// The cold room switches its zone on.
	waitFor(t, 5*time.Second, "living relay on", w.relay1.isOn)
	if !w.relay1.hasCommand("on,2") {
		t.Fatalf("commands = %v", w.relay1.sentCommands())
	}
	// The warm room keeps its zone off.
	waitFor(t, 5*time.Second, "bedroom relay off command", func() bool {
		return w.relay2.hasCommand("off")
	})
	if w.relay2.isOn() {
		t.Fatal("bedroom relay is on although the room is warm")
	}

	ev := w.lastCycle(app)
	living := cycleRoom(t, ev, "living")
	if !approx(living["temperature"], 19.4) || !approx(living["target"], 20.0) || living["demand"] != true {
		t.Fatalf("living = %v", living)
	}
	bedroom := cycleRoom(t, ev, "bedroom")
	if bedroom["demand"] != false {
		t.Fatalf("bedroom = %v", bedroom)
	}
	hallway := cycleRoom(t, ev, "hallway")
	if hallway["temperature"] != nil || hallway["demand"] != false {
		t.Fatalf("hallway = %v", hallway)
	}
	if z := cycleZone(t, ev, "ground"); z["desired"] != "on" {
		t.Fatalf("ground = %v", z)
	}
	if z := cycleZone(t, ev, "upstairs"); z["desired"] != "off" {
		t.Fatalf("upstairs = %v", z)
	}
	if unit := ev["unit"]; unit != "C" {
		t.Fatalf("unit = %v", unit)
	}

	// Inside the hysteresis band the relay stays on.
	w.setLiving(20.4)
	time.Sleep(1500 * time.Millisecond)
	if !w.relay1.isOn() {
		t.Fatal("relay switched off inside the hysteresis band")
	}

	// At target+hysteresis it goes off once min_switch_interval allows.
	w.setLiving(20.6)
	waitFor(t, 5*time.Second, "relay off", func() bool { return !w.relay1.isOn() })
	if !w.relay1.hasCommand("off") {
		t.Fatalf("commands = %v", w.relay1.sentCommands())
	}

	// SIGTERM turns everything off and exits successfully (F8.AC1).
	if err := app.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := app.waitExit(5 * time.Second); code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, app.stderrText())
	}
	if w.relay1.isOn() || w.relay2.isOn() {
		t.Fatal("a relay is on after shutdown")
	}
	if app.stderrText() != "" {
		t.Fatalf("stderr = %q", app.stderrText())
	}
}

func TestStartupGraceBeforeAnyCommand(t *testing.T) {
	w := newWorld(t)
	app := w.start()
	// Nothing but status requests before startup_grace has passed (F6.AC3).
	time.Sleep(400 * time.Millisecond)
	for _, c := range w.relay1.sentCommands() {
		if c != "status_update" {
			t.Fatalf("relay command %q before startup_grace", c)
		}
	}
	ev := app.waitEvent("control_started", 5*time.Second)
	if ev["reason"] != "startup_grace_elapsed" {
		t.Fatalf("reason = %v", ev["reason"])
	}
	waitFor(t, 5*time.Second, "relay off command", func() bool {
		return w.relay1.hasCommand("off")
	})
	if w.relay1.isOn() {
		t.Fatal("relay is on before any temperature was read")
	}
}

func TestRetainedReadingsAreIgnored(t *testing.T) {
	w := newWorld(t)
	// A retained reading from before the start must not be used (F6.AC7).
	w.sht1.publishPayload(`{"id":0,"tC":19.4,"tF":66.92}`, true)

	app := w.start()
	app.waitEvent("control_started", 5*time.Second)
	if n := len(app.events("ignored_retained")); n == 0 {
		t.Fatal("no ignored_retained event")
	}
	evs := app.events("control_started")
	if evs[0]["reason"] != "startup_grace_elapsed" {
		t.Fatalf("reason = %v; a retained reading started the controller", evs[0]["reason"])
	}
	ev := w.lastCycle(app)
	for _, room := range ev["rooms"].([]any) {
		room := room.(map[string]any)
		if room["id"] == "living" && room["temperature"] != nil {
			t.Fatalf("retained reading was used: %v", room)
		}
	}

	// A fresh reading is used.
	w.setLiving(19.4)
	waitFor(t, 5*time.Second, "living temperature", func() bool {
		ev := w.lastCycle(app)
		return cycleRoom(t, ev, "living")["temperature"] != nil
	})
	waitFor(t, 5*time.Second, "relay on", w.relay1.isOn)
}

func TestStatusOperation(t *testing.T) {
	w := newWorld(t)
	w.startHeating(19.4, 22.0)
	waitFor(t, 5*time.Second, "relay on", w.relay1.isOn)
	time.Sleep(500 * time.Millisecond)

	out, code := runCLI(t, "status", "--socket", w.socket)
	if code != 0 {
		t.Fatalf("status exit = %d: %s", code, out)
	}
	for _, want := range []string{"ground", "upstairs", "living", "bedroom", "hallway", "on", "off", "°C"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output misses %q:\n%s", want, out)
		}
	}

	out, code = runCLI(t, "status", "--socket", w.socket, "--json")
	if code != 0 {
		t.Fatalf("status --json exit = %d: %s", code, out)
	}
	var snap map[string]any
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		t.Fatalf("status --json is not JSON: %v\n%s", err, out)
	}
	if snap["schema"] != float64(1) || snap["unit"] != "C" {
		t.Fatalf("snapshot = %v", snap)
	}
	if snap["started"] != true || snap["connected"] != true {
		t.Fatalf("snapshot = %v", snap)
	}
	rooms := snap["rooms"].([]any)
	var living map[string]any
	for _, r := range rooms {
		r := r.(map[string]any)
		if r["id"] == "living" {
			living = r
		}
	}
	if living == nil || !approx(living["temperature"], 19.4) || !approx(living["target"], 20.0) || living["demand"] != true {
		t.Fatalf("living = %v", living)
	}

	// SIGTERM, then status has to report that nothing is running.
	if err := w.app.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := w.app.waitExit(5 * time.Second); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	out, code = runCLI(t, "status", "--socket", w.socket)
	if code == 0 {
		t.Fatalf("status succeeded without a controller: %s", out)
	}
	if !strings.Contains(out, "no heatctl controller") {
		t.Fatalf("status message = %q", out)
	}
}

func TestSigkillLeaseTurnsRelayOff(t *testing.T) {
	w := newWorld(t)
	w.startHeating(19.0, 22.0)
	waitFor(t, 5*time.Second, "relay on", w.relay1.isOn)

	// SIGKILL: no shutdown, no last will (F5.AC3).
	if err := w.app.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	w.app.waitExit(5 * time.Second)
	if !w.relay1.isOn() {
		t.Fatal("relay already off before the lease could expire")
	}
	waitFor(t, 5*time.Second, "device lease expires", func() bool { return !w.relay1.isOn() })
}

func TestBrokerGoneLeaseTurnsRelayOff(t *testing.T) {
	w := newWorld(t)
	w.startHeating(19.0, 22.0)
	waitFor(t, 5*time.Second, "relay on", w.relay1.isOn)

	w.br.stop()
	// The device's own timer switches the relay off even though the broker is
	// gone (F5.AC6).
	waitFor(t, 6*time.Second, "device lease expires", func() bool { return !w.relay1.isOn() })
	if w.app.exited() {
		t.Fatal("heatctl exited when the broker went away")
	}
}

func TestBrokerRestartReconnects(t *testing.T) {
	w := newWorld(t)
	app := w.startHeating(19.0, 22.0)
	waitFor(t, 5*time.Second, "relay on", w.relay1.isOn)
	w.relay1.clearCommands()

	w.br.restart()
	// The controller reconnects, subscribes again and re-sends the desired
	// state (R2, F6.AC1) — well inside the 60s the specification allows.
	waitFor(t, 20*time.Second, "reconnect", func() bool {
		return len(app.events("mqtt_connected")) >= 2
	})
	waitFor(t, 10*time.Second, "resend after reconnect", func() bool {
		return w.relay1.hasCommand("on,2")
	})
	waitFor(t, 5*time.Second, "relay back on", w.relay1.isOn)
	if w.app.exited() {
		t.Fatal("heatctl exited during the broker restart")
	}
}

func TestLeaseKeepsRelayOn(t *testing.T) {
	w := newWorld(t)
	w.startHeating(19.0, 22.0)
	waitFor(t, 5*time.Second, "relay on", w.relay1.isOn)
	before := len(w.relay1.sentCommands())

	// The relay must stay on for much longer than its two second lease
	// because heatctl keeps refreshing it (F5.AC5).
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if !w.relay1.isOn() {
			t.Fatalf("relay dropped off before the lease refresh; commands: %v", w.relay1.sentCommands())
		}
		time.Sleep(50 * time.Millisecond)
	}
	after := len(w.relay1.sentCommands())
	if after-before < 2 {
		t.Fatalf("expected lease refreshes, commands before=%d after=%d: %v", before, after, w.relay1.sentCommands())
	}
}

func TestRelayOfflineIsDegraded(t *testing.T) {
	w := newWorld(t)
	app := w.startHeating(19.0, 22.0)
	waitFor(t, 5*time.Second, "relay on", w.relay1.isOn)

	w.relay1.setOnline(false)
	waitFor(t, 5*time.Second, "degraded zone", func() bool {
		ev := w.lastCycle(app)
		return cycleZone(t, ev, "ground")["degraded"] == true
	})
	out, code := runCLI(t, "status", "--socket", w.socket, "--json")
	if code != 0 {
		t.Fatalf("status = %d: %s", code, out)
	}
	var snap map[string]any
	_ = json.Unmarshal([]byte(out), &snap)
	zones := snap["zones"].([]any)
	for _, z := range zones {
		z := z.(map[string]any)
		if z["id"] != "ground" {
			continue
		}
		if z["degraded"] != true || z["reported"] != "unknown" || z["online"] != false {
			t.Fatalf("ground = %v", z)
		}
	}
}

func TestBadPayloadsDoNotChangeDecisions(t *testing.T) {
	w := newWorld(t)
	app := w.startHeating(19.0, 22.0)
	waitFor(t, 5*time.Second, "relay on", w.relay1.isOn)
	time.Sleep(1500 * time.Millisecond)
	if !w.relay1.isOn() {
		t.Fatal("relay off before the test started")
	}

	w.sht1.publishPayload(`this is not json`, false)
	w.sht1.publishPayload(`{"id":0,"tC":null,"tF":null,"errors":["read"]}`, false)
	w.sht1.publishPayload(`{"id":0,"tC":120,"tF":248}`, false)
	time.Sleep(1500 * time.Millisecond)

	if !w.relay1.isOn() {
		t.Fatal("a bad payload changed the decision")
	}
	if app.exited() {
		t.Fatal("heatctl exited on a bad payload")
	}
	if n := len(app.events("ignored_payload")); n < 2 {
		t.Fatalf("ignored_payload events = %d", n)
	}
	if n := len(app.events("ignored_reading")); n < 1 {
		t.Fatalf("ignored_reading events = %d", n)
	}
	// The controller still runs control cycles.
	n := len(app.events("control_cycle"))
	time.Sleep(time.Second)
	if len(app.events("control_cycle")) <= n {
		t.Fatal("control cycles stopped")
	}
}

func TestClosedStdoutDoesNotStopControl(t *testing.T) {
	w := newWorld(t)
	app := startAppMode(t, w.cfg, w.socket, true)
	// Without stdout there is no log to wait on, so repeat the readings until
	// the controller has subscribed and switches the relay on.
	deadline := time.Now().Add(8 * time.Second)
	for !w.relay1.isOn() && time.Now().Before(deadline) {
		w.sht1.publish(19.0)
		w.zig.publishZigbee(22.0)
		time.Sleep(300 * time.Millisecond)
	}
	if !w.relay1.isOn() {
		t.Fatalf("relay did not turn on with closed stdout; stderr: %s", app.stderrText())
	}
	// Several lease refreshes write to the closed pipe; control must go on
	// (F8.AC2, L3).
	time.Sleep(3 * time.Second)
	if !w.relay1.isOn() {
		t.Fatal("relay dropped off with a closed stdout")
	}
	if app.exited() {
		t.Fatalf("heatctl exited when stdout was closed; stderr: %s", app.stderrText())
	}
	if err := app.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := app.waitExit(5 * time.Second); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if w.relay1.isOn() {
		t.Fatal("relay on after shutdown with a closed stdout")
	}
}

func TestOnlyTheSocketIsWritten(t *testing.T) {
	dir := t.TempDir()
	br := startBroker(t)
	w := &world{t: t, br: br, dir: dir, socket: filepath.Join(dir, "heatctl.sock")}
	w.relay1 = newRelay(t, br, "shelly1")
	w.relay2 = newRelay(t, br, "shelly2")
	w.sht1 = newShellyTemp(t, br, "sht1", 0)
	w.zig = newZigbeeTemp(t, br, "zigbee2mqtt", "bedroom-display")
	w.cfg = filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(w.cfg, []byte(strings.Replace(configTemplate, "%s", br.addr, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	w.startHeating(19.0, 22.0)
	waitFor(t, 5*time.Second, "relay on", w.relay1.isOn)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 || names[0] != "config.yaml" || names[1] != "heatctl.sock" {
		t.Fatalf("files in the runtime directory: %v", names)
	}
}

func TestLogLinesAreJSON(t *testing.T) {
	w := newWorld(t)
	app := w.startHeating(19.0, 22.0)
	waitFor(t, 5*time.Second, "relay on", w.relay1.isOn)
	time.Sleep(time.Second)
	if err := app.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := app.waitExit(5 * time.Second); code != 0 {
		t.Fatalf("exit = %d", code)
	}

	lines := app.rawLines()
	if len(lines) < 5 {
		t.Fatalf("only %d log lines", len(lines))
	}
	for _, line := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if _, ok := obj["event"].(string); !ok {
			t.Fatalf("log line has no event: %q", line)
		}
	}
	cycles := app.events("control_cycle")
	if len(cycles) == 0 {
		t.Fatal("no control_cycle events")
	}
	for _, ev := range cycles {
		for _, key := range []string{"schema", "ts", "unit", "rooms", "zones"} {
			if _, ok := ev[key]; !ok {
				t.Fatalf("control_cycle misses %q: %v", key, ev)
			}
		}
		rooms := ev["rooms"].([]any)
		if len(rooms) != 3 {
			t.Fatalf("rooms = %v", rooms)
		}
		for _, r := range rooms {
			r := r.(map[string]any)
			for _, key := range []string{"id", "readings", "temperature", "target", "demand"} {
				if _, ok := r[key]; !ok {
					t.Fatalf("room misses %q: %v", key, r)
				}
			}
		}
		for _, z := range ev["zones"].([]any) {
			z := z.(map[string]any)
			for _, key := range []string{"id", "desired", "reported", "degraded"} {
				if _, ok := z[key]; !ok {
					t.Fatalf("zone misses %q: %v", key, z)
				}
			}
		}
	}
	for _, name := range []string{"startup", "mqtt_connected", "control_started", "sensor_reading", "relay_command", "shutdown"} {
		if len(app.events(name)) == 0 {
			t.Fatalf("no %s event; events: %v", name, eventNames(app))
		}
	}
}

func eventNames(a *appProc) []string {
	var names []string
	seen := map[string]bool{}
	for _, ev := range a.allEvents() {
		name, _ := ev["event"].(string)
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

func runCLI(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("running heatctl %v: %v", args, err)
		}
	}
	return string(out), code
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.yaml")
	if err := os.WriteFile(valid, []byte(strings.Replace(configTemplate, "%s", "127.0.0.1:1883", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, "validate", valid)
	if code != 0 {
		t.Fatalf("validate exit = %d: %s", code, out)
	}
	if !strings.Contains(out, "configuration is valid") || !strings.Contains(out, "2 zones") {
		t.Fatalf("validate output = %q", out)
	}

	invalid := filepath.Join(dir, "invalid.yaml")
	bad := strings.Replace(configTemplate, "target: 20.0", "target: 99", 1)
	if err := os.WriteFile(invalid, []byte(strings.Replace(bad, "%s", "127.0.0.1:1883", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code = runCLI(t, "validate", invalid)
	if code != 1 {
		t.Fatalf("validate exit = %d, want 1: %s", code, out)
	}
	if !strings.Contains(out, "line ") || !strings.Contains(out, "target") || !strings.Contains(out, "between 7 and 25") {
		t.Fatalf("validate output = %q", out)
	}

	// F1.AC2: a null schedule is not an omitted schedule; it has no entry.
	nullSchedule := filepath.Join(dir, "schedule-null.yaml")
	nullCfg := `
unit: C
mqtt: { broker: tcp://127.0.0.1:1883 }
zones:
  - id: z
    relay: { device: d, switch: 0 }
    rooms:
      - id: r
        schedule: null
        sensors: [{ zigbee: s }]
`
	if err := os.WriteFile(nullSchedule, []byte(nullCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code = runCLI(t, "validate", nullSchedule)
	if code != 1 {
		t.Fatalf("validate exit = %d, want 1: %s", code, out)
	}
	if !strings.Contains(out, "zones[0].rooms[0].schedule") || !strings.Contains(out, "at least one entry") {
		t.Fatalf("validate output = %q", out)
	}

	out, code = runCLI(t, "validate", filepath.Join(dir, "missing.yaml"))
	if code != 1 || !strings.Contains(out, "no such file") {
		t.Fatalf("missing file: exit %d, output %q", code, out)
	}
}

func TestRunWithInvalidConfigDoesNotConnect(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("unit: K\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, "run", "--config", bad)
	if code != 1 {
		t.Fatalf("exit = %d: %s", code, out)
	}
	if strings.Contains(out, "mqtt_connecting") {
		t.Fatalf("tried to connect with an invalid configuration: %s", out)
	}
	if !strings.Contains(out, "unit") {
		t.Fatalf("output = %q", out)
	}
}

func TestVersionOperation(t *testing.T) {
	out, code := runCLI(t, "version")
	if code != 0 || !strings.HasPrefix(out, "heatctl ") {
		t.Fatalf("version: exit %d, output %q", code, out)
	}
}

func TestUsageForUnknownOperation(t *testing.T) {
	out, code := runCLI(t, "frobnicate")
	if code != 2 {
		t.Fatalf("exit = %d: %s", code, out)
	}
	if !strings.Contains(out, "unknown operation") {
		t.Fatalf("output = %q", out)
	}
}
