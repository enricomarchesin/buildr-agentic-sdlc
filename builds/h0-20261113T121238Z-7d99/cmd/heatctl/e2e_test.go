package main

// End-to-end tests: the real heatctl binary against an in-process MQTT
// broker and a simulated Shelly relay that implements the device's own
// behaviour (on,<seconds> timers, status notifications, last will).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "heatctl-e2e")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "heatctl")
	out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// --- broker -----------------------------------------------------------------

type broker struct {
	addr string
	srv  *mochi.Server
	once sync.Once
}

func (b *broker) close() { b.once.Do(func() { b.srv.Close() }) }

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func startBroker(t *testing.T, addr string) *broker {
	t.Helper()
	srv := mochi.New(&mochi.Options{InlineClient: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	if err := srv.AddListener(listeners.NewTCP(listeners.Config{ID: "tcp", Address: addr})); err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	b := &broker{addr: addr, srv: srv}
	t.Cleanup(b.close)
	return b
}

func (b *broker) publish(t *testing.T, topic, payload string, retain bool) {
	t.Helper()
	if err := b.srv.Publish(topic, []byte(payload), retain, 0); err != nil {
		t.Fatal(err)
	}
}

// --- simulated Shelly relay -------------------------------------------------

type shelly struct {
	t      *testing.T
	prefix string
	id     int
	c      paho.Client

	mu       sync.Mutex
	on       bool
	timer    *time.Timer
	history  []string // commands received
	switched []time.Time
}

func startShelly(t *testing.T, addr, prefix string, id int) *shelly {
	t.Helper()
	s := &shelly{t: t, prefix: prefix, id: id}
	opts := paho.NewClientOptions().AddBroker("tcp://" + addr).SetClientID(prefix)
	opts.SetWill(prefix+"/online", "false", 1, true)
	opts.SetAutoReconnect(true).SetConnectRetry(true).SetConnectRetryInterval(200 * time.Millisecond).SetMaxReconnectInterval(time.Second)
	opts.SetOnConnectHandler(func(c paho.Client) {
		c.Subscribe(fmt.Sprintf("%s/command/switch:%d", prefix, id), 1, s.onCommand).Wait()
		c.Publish(prefix+"/online", 1, true, "true").Wait()
		s.publishStatus()
	})
	s.c = paho.NewClient(opts)
	if tok := s.c.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("shelly connect: %v", tok.Error())
	}
	t.Cleanup(func() { s.c.Disconnect(0) })
	return s
}

func (s *shelly) publishStatus() {
	s.mu.Lock()
	on := s.on
	s.mu.Unlock()
	s.c.Publish(fmt.Sprintf("%s/status/switch:%d", s.prefix, s.id), 0, false,
		fmt.Sprintf(`{"id":%d,"source":"mqtt","output":%v,"temperature":{"tC":41.2,"tF":106.2}}`, s.id, on))
}

func (s *shelly) set(on bool, after int) {
	s.mu.Lock()
	changed := s.on != on
	s.on = on
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if after > 0 {
		s.timer = time.AfterFunc(time.Duration(after)*time.Second, func() { s.set(!on, 0) })
	}
	if changed {
		s.switched = append(s.switched, time.Now())
	}
	s.mu.Unlock()
	if changed {
		s.publishStatus()
	}
}

func (s *shelly) onCommand(_ paho.Client, m paho.Message) {
	p := string(m.Payload())
	s.mu.Lock()
	s.history = append(s.history, p)
	s.mu.Unlock()
	switch {
	case p == "status_update":
		s.publishStatus()
	case p == "on":
		s.set(true, 0)
	case p == "off":
		s.set(false, 0)
	case strings.HasPrefix(p, "on,"):
		n, err := strconv.Atoi(strings.TrimPrefix(p, "on,"))
		if err != nil {
			s.t.Errorf("bad command %q", p)
			return
		}
		s.set(true, n)
	default:
		s.t.Errorf("unexpected command %q", p)
	}
}

func (s *shelly) isOn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.on
}

func (s *shelly) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.history...)
}

func (s *shelly) switchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.switched)
}

// --- heatctl process --------------------------------------------------------

type proc struct {
	cmd    *exec.Cmd
	mu     sync.Mutex
	lines  []string
	socket string
	done   chan struct{}
}

const e2eConfig = `unit: C
mqtt:
  broker: tcp://%s
  client_id: heatctl-test
control:
  cycle_interval: 1s
  relay_lease: 3s
  min_switch_interval: 1s
  startup_grace: 3s
  confirm_timeout: 2s
zones:
  - id: ground
    relay: { device: relay1, switch: 0 }
    rooms:
      - id: living
        target: 20
        sensors: [{ zigbee: living }]
`

func writeConfig(t *testing.T, addr string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(fmt.Sprintf(e2eConfig, addr)), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func startHeatctl(t *testing.T, addr string, closedStdout bool) *proc {
	t.Helper()
	cfg := writeConfig(t, addr)
	sock := filepath.Join(t.TempDir(), "heatctl.sock")
	p := &proc{socket: sock, done: make(chan struct{})}
	if closedStdout {
		p.cmd = exec.Command("sh", "-c", `exec "$0" run -socket "$1" "$2" >&-`, binary, sock, cfg)
	} else {
		p.cmd = exec.Command(binary, "run", "-socket", sock, cfg)
	}
	p.cmd.Stderr = os.Stderr
	var out io.ReadCloser
	if !closedStdout {
		var err error
		if out, err = p.cmd.StdoutPipe(); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		if out != nil {
			sc := bufio.NewScanner(out)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			for sc.Scan() {
				p.mu.Lock()
				p.lines = append(p.lines, sc.Text())
				p.mu.Unlock()
			}
		}
		p.cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		p.cmd.Process.Kill()
		<-p.done
	})
	return p
}

func (p *proc) events(name string) []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []map[string]any
	for _, l := range p.lines {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["event"] == name {
			out = append(out, m)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (p *proc) waitConnected(t *testing.T, n int) {
	t.Helper()
	waitFor(t, "mqtt_connected", 10*time.Second, func() bool { return len(p.events("mqtt_connected")) >= n })
}

// --- tests ------------------------------------------------------------------

func TestE2EControlStatusAndStop(t *testing.T) {
	b := startBroker(t, freePort(t))
	relay := startShelly(t, b.addr, "relay1", 0)
	// A retained reading from before the start must be ignored (F6.AC7).
	b.publish(t, "zigbee2mqtt/living", `{"temperature":10}`, true)
	p := startHeatctl(t, b.addr, false)
	p.waitConnected(t, 1)

	time.Sleep(500 * time.Millisecond)
	if len(p.events("control_cycle")) != 0 {
		t.Fatal("control cycle before a temperature or startup_grace")
	}
	b.publish(t, "zigbee2mqtt/living", `{"temperature":19.0}`, false)
	waitFor(t, "relay on", 3*time.Second, relay.isOn)
	cycles := p.events("control_cycle")
	room := cycles[0]["rooms"].([]any)[0].(map[string]any)
	if room["temperature"] != 19.0 {
		t.Errorf("first cycle used %v, want 19.0 (retained 10 ignored)", room["temperature"])
	}

	// status
	out, err := exec.Command(binary, "status", "-socket", p.socket).CombinedOutput()
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	for _, want := range []string{"zone ground", "desired on", "reported on", "degraded no", "living", "19.0", "20.0", "yes"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	out, err = exec.Command(binary, "status", "-json", "-socket", p.socket).Output()
	if err != nil || !json.Valid(out) {
		t.Fatalf("status -json: %v %s", err, out)
	}

	// Stays on for longer than the lease (F5.AC5).
	switches := relay.switchCount()
	time.Sleep(7 * time.Second)
	if !relay.isOn() || relay.switchCount() != switches {
		t.Fatalf("relay turned itself off while heatctl runs; commands %q", relay.commands())
	}

	// SIGTERM: relay off, exit 0, within 5 s (F6.AC4).
	start := time.Now()
	p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("did not exit within 5 s")
	}
	if code := p.cmd.ProcessState.ExitCode(); code != 0 {
		t.Errorf("exit status %d", code)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("too slow")
	}
	waitFor(t, "relay off after SIGTERM", time.Second, func() bool { return !relay.isOn() })

	// No controller: status says so and fails (F7.AC2).
	cmd := exec.Command(binary, "status", "-socket", p.socket)
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "not running") {
		t.Errorf("status without controller: %v %s", err, out)
	}

	// Every log line is a JSON object with an event.
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, l := range p.lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil || m["event"] == nil {
			t.Errorf("bad log line %q", l)
		}
	}
}

func TestE2ESigkillLeaseExpires(t *testing.T) {
	b := startBroker(t, freePort(t))
	relay := startShelly(t, b.addr, "relay1", 0)
	p := startHeatctl(t, b.addr, false)
	p.waitConnected(t, 1)
	b.publish(t, "zigbee2mqtt/living", `{"temperature":19.0}`, false)
	waitFor(t, "relay on", 3*time.Second, relay.isOn)
	p.cmd.Process.Kill()
	start := time.Now()
	waitFor(t, "relay off after SIGKILL", 4*time.Second, func() bool { return !relay.isOn() })
	if d := time.Since(start); d > 3*time.Second+500*time.Millisecond {
		t.Errorf("relay off after %s, lease is 3s", d)
	}
}

func TestE2EBrokerLossAndReturn(t *testing.T) {
	addr := freePort(t)
	b := startBroker(t, addr)
	relay := startShelly(t, addr, "relay1", 0)
	p := startHeatctl(t, addr, false)
	p.waitConnected(t, 1)
	b.publish(t, "zigbee2mqtt/living", `{"temperature":19.0}`, false)
	waitFor(t, "relay on", 3*time.Second, relay.isOn)

	// The broker goes away: the relay's own timer turns it off (F5.AC6).
	b.close()
	start := time.Now()
	waitFor(t, "relay off after broker loss", 4*time.Second, func() bool { return !relay.isOn() })
	if d := time.Since(start); d > 3*time.Second+500*time.Millisecond {
		t.Errorf("relay off after %s, lease is 3s", d)
	}
	waitFor(t, "mqtt_disconnected", 5*time.Second, func() bool { return len(p.events("mqtt_disconnected")) > 0 })

	// The broker is back: heatctl reconnects and re-sends (F6.AC1). The
	// sensor is still fresh, so the relay goes back on.
	startBroker(t, addr)
	back := time.Now()
	p.waitConnected(t, 2)
	waitFor(t, "relay on again", 60*time.Second, relay.isOn)
	t.Logf("relay back on %s after the broker returned", time.Since(back).Round(time.Millisecond))
}

func TestE2EClosedStdout(t *testing.T) {
	b := startBroker(t, freePort(t))
	relay := startShelly(t, b.addr, "relay1", 0)
	p := startHeatctl(t, b.addr, true)
	// No log to wait on: wait for the status socket.
	waitFor(t, "status socket", 10*time.Second, func() bool {
		_, err := os.Stat(p.socket)
		return err == nil
	})
	time.Sleep(time.Second)
	b.publish(t, "zigbee2mqtt/living", `{"temperature":19.0}`, false)
	waitFor(t, "relay on with stdout closed", 5*time.Second, relay.isOn)
	b.publish(t, "zigbee2mqtt/living", `{"temperature":21.0}`, false)
	waitFor(t, "relay off with stdout closed", 5*time.Second, func() bool { return !relay.isOn() })
	select {
	case <-p.done:
		t.Fatal("heatctl exited")
	default:
	}
}

func TestE2EInvalidConfigDoesNotConnect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	os.WriteFile(path, []byte("unit: X\nmqtt: {broker: tcp://127.0.0.1:1}\nzones: []\n"), 0o644)
	out, err := exec.Command(binary, "run", "-socket", "", path).CombinedOutput()
	if err == nil {
		t.Fatal("run accepted an invalid configuration")
	}
	if !strings.Contains(string(out), "unit") || strings.Contains(string(out), "mqtt_connected") {
		t.Errorf("output: %s", out)
	}
	out, err = exec.Command(binary, "validate", path).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "zones") {
		t.Errorf("validate: %v %s", err, out)
	}
	out, err = exec.Command(binary, "validate", "../../examples/config.yaml").CombinedOutput()
	if err != nil {
		t.Errorf("validate example: %v %s", err, out)
	}
	out, err = exec.Command(binary, "version").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "heatctl ") {
		t.Errorf("version: %v %s", err, out)
	}
}
