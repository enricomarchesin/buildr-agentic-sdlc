package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	mqttserver "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
)

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "heatctl-e2e-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binPath = filepath.Join(dir, "heatctl")
	root, err := filepath.Abs("../..")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/heatctl")
	cmd.Dir = root
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "building heatctl: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// --- broker -----------------------------------------------------------------

type broker struct {
	t    *testing.T
	srv  *mqttserver.Server
	addr string
}

func startBroker(t *testing.T) *broker {
	t.Helper()
	b := &broker{t: t}
	b.start("127.0.0.1:0")
	t.Cleanup(b.stop)
	return b
}

func (b *broker) start(addr string) {
	b.t.Helper()
	srv := mqttserver.New(&mqttserver.Options{})
	if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
		b.t.Fatalf("broker hook: %v", err)
	}
	tcp := listeners.NewTCP(listeners.Config{ID: "tcp", Address: addr})
	if err := srv.AddListener(tcp); err != nil {
		b.t.Fatalf("broker listener: %v", err)
	}
	go func() { _ = srv.Serve() }()
	b.srv = srv
	b.addr = tcp.Address()
	waitFor(b.t, 2*time.Second, "broker listening", func() bool {
		c, err := pahoConnect(b.addr, "probe-"+strconv.FormatInt(time.Now().UnixNano(), 36), "", nil)
		if err != nil {
			return false
		}
		c.Disconnect(20)
		return true
	})
}

// restart brings the broker back on the same address.
func (b *broker) restart() {
	b.t.Helper()
	addr := b.addr
	_ = b.srv.Close()
	b.srv = nil
	time.Sleep(100 * time.Millisecond)
	b.start(addr)
}

// stop shuts the broker down and leaves it down.
func (b *broker) stop() {
	if b.srv != nil {
		_ = b.srv.Close()
		b.srv = nil
	}
}

// --- paho helpers -----------------------------------------------------------

func pahoConnect(addr, clientID string, willTopic string, onMessage func(string, string)) (paho.Client, error) {
	return pahoConnectWith(addr, clientID, willTopic, onMessage, nil)
}

func pahoConnectWith(addr, clientID string, willTopic string, onMessage func(string, string), onConnect func()) (paho.Client, error) {
	o := paho.NewClientOptions()
	o.AddBroker("tcp://" + addr)
	o.SetClientID(clientID)
	o.SetAutoReconnect(true)
	o.SetMaxReconnectInterval(2 * time.Second)
	o.SetConnectRetry(false)
	o.SetKeepAlive(5)
	o.SetConnectTimeout(3 * time.Second)
	o.SetCleanSession(true)
	if willTopic != "" {
		o.SetWill(willTopic, "false", 1, true)
	}
	if onMessage != nil {
		o.SetDefaultPublishHandler(func(_ paho.Client, m paho.Message) {
			onMessage(m.Topic(), string(m.Payload()))
		})
	}
	if onConnect != nil {
		o.SetOnConnectHandler(func(_ paho.Client) { onConnect() })
	}
	c := paho.NewClient(o)
	tok := c.Connect()
	if !tok.WaitTimeout(5 * time.Second) {
		return nil, fmt.Errorf("connect timeout")
	}
	if err := tok.Error(); err != nil {
		return nil, err
	}
	return c, nil
}

// --- devices ----------------------------------------------------------------

type relayDevice struct {
	t   *testing.T
	id  string
	cli paho.Client

	mu       sync.Mutex
	output   bool
	online   bool
	commands []string
	timer    *time.Timer
}

func newRelay(t *testing.T, br *broker, id string) *relayDevice {
	t.Helper()
	r := &relayDevice{t: t, id: id}
	cli, err := pahoConnectWith(br.addr, "relay-"+id, id+"/online", func(_ string, payload string) {
		r.handle(payload)
	}, func() {
		r.announce()
	})
	if err != nil {
		t.Fatalf("relay %s: %v", id, err)
	}
	r.mu.Lock()
	r.cli = cli
	r.mu.Unlock()
	t.Cleanup(func() { cli.Disconnect(50) })
	// The first connect also runs announce, but not synchronously; subscribe
	// here so the device is ready when the test returns.
	r.announce()
	return r
}

// client returns the paho client, or nil before the first connect returned.
func (r *relayDevice) client() paho.Client {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cli
}

// announce re-publishes availability and re-subscribes after a reconnect.
func (r *relayDevice) announce() {
	cli := r.client()
	if cli == nil {
		return
	}
	tok := cli.Publish(r.id+"/online", 1, true, "true")
	tok.WaitTimeout(2 * time.Second)
	r.subscribe()
}

func (r *relayDevice) subscribe() {
	cli := r.client()
	if cli == nil {
		return
	}
	tok := cli.Subscribe(r.id+"/command/#", 1, nil)
	tok.WaitTimeout(3 * time.Second)
}

func (r *relayDevice) handle(payload string) {
	r.mu.Lock()
	r.commands = append(r.commands, payload)
	r.mu.Unlock()

	switch {
	case payload == "status_update":
		r.mu.Lock()
		state := r.output
		r.mu.Unlock()
		// A device answers with its current state even when unchanged.
		r.publish(state)
	case payload == "off":
		r.setOutput(false)
		r.arm(0)
	case payload == "on":
		r.setOutput(true)
		r.arm(0)
	case strings.HasPrefix(payload, "on,"):
		secs, err := strconv.Atoi(strings.TrimPrefix(payload, "on,"))
		if err != nil {
			r.t.Errorf("relay %s: bad command %q", r.id, payload)
			return
		}
		r.setOutput(true)
		r.arm(time.Duration(secs) * time.Second)
	default:
		r.t.Logf("relay %s: ignoring unknown command %q", r.id, payload)
	}
}

func (r *relayDevice) setOutput(v bool) {
	r.mu.Lock()
	changed := r.output != v
	r.output = v
	r.mu.Unlock()
	if changed {
		r.publish(v)
	}
}

// arm starts the device's own off timer, as on,<seconds> does.
func (r *relayDevice) arm(d time.Duration) {
	r.mu.Lock()
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	if d > 0 {
		r.timer = time.AfterFunc(d, func() {
			r.mu.Lock()
			r.timer = nil
			r.mu.Unlock()
			r.setOutput(false)
		})
	}
	r.mu.Unlock()
}

func (r *relayDevice) publish(output bool) {
	payload := fmt.Sprintf(`{"id":0,"source":"MQTT","output":%t,"temperature":{"tC":41.2,"tF":106.2}}`, output)
	tok := r.cli.Publish(r.id+"/status/switch:0", 1, false, payload)
	tok.WaitTimeout(2 * time.Second)
}

func (r *relayDevice) isOn() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.output
}

func (r *relayDevice) sentCommands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.commands))
	copy(out, r.commands)
	return out
}

func (r *relayDevice) clearCommands() {
	r.mu.Lock()
	r.commands = nil
	r.mu.Unlock()
}

func (r *relayDevice) hasCommand(sub string) bool {
	for _, c := range r.sentCommands() {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

// setOnline publishes the availability a Shelly device announces.
func (r *relayDevice) setOnline(v bool) {
	payload := "false"
	if v {
		payload = "true"
	}
	tok := r.cli.Publish(r.id+"/online", 1, true, payload)
	tok.WaitTimeout(2 * time.Second)
	r.mu.Lock()
	r.online = v
	r.mu.Unlock()
}

type tempSensor struct {
	t     *testing.T
	topic string
	cli   paho.Client
}

func newShellyTemp(t *testing.T, br *broker, device string, component int) *tempSensor {
	t.Helper()
	cli, err := pahoConnect(br.addr, "temp-"+device, "", nil)
	if err != nil {
		t.Fatalf("temp sensor %s: %v", device, err)
	}
	t.Cleanup(func() { cli.Disconnect(50) })
	return &tempSensor{t: t, topic: fmt.Sprintf("%s/status/temperature:%d", device, component), cli: cli}
}

func newZigbeeTemp(t *testing.T, br *broker, base, name string) *tempSensor {
	t.Helper()
	cli, err := pahoConnect(br.addr, "zigbee-"+name, "", nil)
	if err != nil {
		t.Fatalf("zigbee sensor %s: %v", name, err)
	}
	t.Cleanup(func() { cli.Disconnect(50) })
	return &tempSensor{t: t, topic: base + "/" + name, cli: cli}
}

func (s *tempSensor) publish(celsius float64) {
	s.publishPayload(fmt.Sprintf(`{"id":0,"tC":%g,"tF":%g}`, celsius, celsius*9/5+32), false)
}

func (s *tempSensor) publishZigbee(celsius float64) {
	s.publishPayload(fmt.Sprintf(`{"temperature":%g,"humidity":48.2,"battery":95,"linkquality":120}`, celsius), false)
}

func (s *tempSensor) publishPayload(payload string, retained bool) {
	tok := s.cli.Publish(s.topic, 1, retained, payload)
	if !tok.WaitTimeout(2 * time.Second) {
		s.t.Fatalf("publish to %s timed out", s.topic)
	}
	if err := tok.Error(); err != nil {
		s.t.Fatalf("publish to %s: %v", s.topic, err)
	}
}

// --- the application under test ---------------------------------------------

type appProc struct {
	t      *testing.T
	cmd    *exec.Cmd
	mu     sync.Mutex
	lines  []map[string]any
	raw    []string
	stderr strings.Builder
	done   chan struct{}
	werr   error
}

func startApp(t *testing.T, cfgPath, socketPath string) *appProc {
	t.Helper()
	return startAppMode(t, cfgPath, socketPath, false)
}

func startAppMode(t *testing.T, cfgPath, socketPath string, closedStdout bool) *appProc {
	t.Helper()
	a := &appProc{t: t, done: make(chan struct{})}
	args := []string{"run", "--config", cfgPath}
	if socketPath != "" {
		args = append(args, "--socket", socketPath)
	}
	a.cmd = exec.Command(binPath, args...)

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if closedStdout {
		// No reader: every write into the pipe fails with EPIPE.
		_ = pr.Close()
	}
	a.cmd.Stdout = pw
	a.cmd.Stderr = &a.stderr
	if err := a.cmd.Start(); err != nil {
		t.Fatalf("starting heatctl: %v", err)
	}
	// The child holds its own copy; closing ours makes the reader see EOF when
	// the child exits.
	_ = pw.Close()

	go func() {
		if !closedStdout {
			a.read(pr)
			_ = pr.Close()
		}
		err := a.cmd.Wait()
		a.mu.Lock()
		a.werr = err
		a.mu.Unlock()
		close(a.done)
	}()
	t.Cleanup(func() {
		if !a.exited() {
			_ = a.cmd.Process.Kill()
			<-a.done
		}
		if t.Failed() {
			t.Logf("heatctl stdout:\n%s", strings.Join(a.rawLines(), "\n"))
			t.Logf("heatctl stderr:\n%s", a.stderrText())
		}
	})
	return a
}

func (a *appProc) read(r io.ReadCloser) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		a.mu.Lock()
		a.raw = append(a.raw, line)
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err == nil {
			a.lines = append(a.lines, obj)
		}
		a.mu.Unlock()
	}
}

func (a *appProc) exited() bool {
	select {
	case <-a.done:
		return true
	default:
		return false
	}
}

func (a *appProc) events(name string) []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []map[string]any
	for _, l := range a.lines {
		if l["event"] == name {
			out = append(out, deepCopy(l))
		}
	}
	return out
}

func (a *appProc) allEvents() []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]map[string]any, len(a.lines))
	for i, l := range a.lines {
		out[i] = deepCopy(l)
	}
	return out
}

func (a *appProc) rawLines() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.raw))
	copy(out, a.raw)
	return out
}

func deepCopy(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func (a *appProc) waitEvent(name string, timeout time.Duration) map[string]any {
	a.t.Helper()
	var found map[string]any
	waitFor(a.t, timeout, "event "+name, func() bool {
		evs := a.events(name)
		if len(evs) == 0 {
			return false
		}
		found = evs[len(evs)-1]
		return true
	})
	return found
}

func (a *appProc) waitExit(timeout time.Duration) int {
	a.t.Helper()
	select {
	case <-a.done:
	case <-time.After(timeout):
		a.t.Fatalf("heatctl did not exit within %s; stdout:\n%s\nstderr:\n%s",
			timeout, strings.Join(a.rawLines(), "\n"), a.stderr.String())
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.werr == nil {
		return 0
	}
	if ee, ok := a.werr.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	a.t.Fatalf("wait: %v", a.werr)
	return -1
}

func (a *appProc) stderrText() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stderr.String()
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// --- world ------------------------------------------------------------------

const configTemplate = `
unit: C
mqtt:
  broker: tcp://%s
control:
  cycle_interval: 300ms
  hysteresis: 0.5
  stale_after: 30s
  min_switch_interval: 1s
  relay_lease: 2s
  startup_grace: 1s
  confirm_timeout: 1s
zones:
  - id: ground
    relay: { device: shelly1, switch: 0 }
    rooms:
      - id: living
        target: 20.0
        sensors: [{ device: sht1, component: 0 }]
  - id: upstairs
    relay: { device: shelly2, switch: 1 }
    rooms:
      - id: bedroom
        target: 19.0
        sensors: [{ zigbee: bedroom-display }]
      - id: hallway
`

type world struct {
	t      *testing.T
	br     *broker
	dir    string
	cfg    string
	socket string
	app    *appProc
	relay1 *relayDevice
	relay2 *relayDevice
	sht1   *tempSensor
	zig    *tempSensor
}

func newWorld(t *testing.T) *world {
	t.Helper()
	br := startBroker(t)
	dir := t.TempDir()
	w := &world{t: t, br: br, dir: dir, socket: filepath.Join(dir, "heatctl.sock")}
	w.relay1 = newRelay(t, br, "shelly1")
	w.relay2 = newRelay(t, br, "shelly2")
	w.sht1 = newShellyTemp(t, br, "sht1", 0)
	w.zig = newZigbeeTemp(t, br, "zigbee2mqtt", "bedroom-display")
	w.cfg = filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(w.cfg, []byte(fmt.Sprintf(configTemplate, br.addr)), 0o644); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) start() *appProc {
	w.t.Helper()
	w.app = startApp(w.t, w.cfg, w.socket)
	w.app.waitEvent("mqtt_connected", 5*time.Second)
	return w.app
}

// startHeating starts the app and gives both sensor rooms a temperature, which
// ends the startup grace early.
func (w *world) startHeating(living, bedroom float64) *appProc {
	w.t.Helper()
	app := w.start()
	w.sht1.publish(living)
	w.zig.publishZigbee(bedroom)
	app.waitEvent("control_started", 5*time.Second)
	return app
}

func (w *world) setLiving(celsius float64) {
	w.t.Helper()
	w.sht1.publish(celsius)
}

func (w *world) setBedroom(celsius float64) {
	w.t.Helper()
	w.zig.publishZigbee(celsius)
}

func (w *world) lastCycle(app *appProc) map[string]any {
	w.t.Helper()
	app.waitEvent("control_cycle", 5*time.Second)
	evs := app.events("control_cycle")
	return evs[len(evs)-1]
}

func cycleRoom(t *testing.T, ev map[string]any, id string) map[string]any {
	t.Helper()
	rooms, _ := ev["rooms"].([]any)
	for _, r := range rooms {
		room, _ := r.(map[string]any)
		if room["id"] == id {
			return room
		}
	}
	t.Fatalf("room %s not in cycle event: %v", id, ev)
	return nil
}

func cycleZone(t *testing.T, ev map[string]any, id string) map[string]any {
	t.Helper()
	zones, _ := ev["zones"].([]any)
	for _, z := range zones {
		zone, _ := z.(map[string]any)
		if zone["id"] == id {
			return zone
		}
	}
	t.Fatalf("zone %s not in cycle event: %v", id, ev)
	return nil
}

func approx(v any, want float64) bool {
	f, ok := v.(float64)
	return ok && f > want-0.001 && f < want+0.001
}
