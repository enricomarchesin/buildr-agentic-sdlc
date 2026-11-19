// Package mqttio connects heatctl to the MQTT broker.
//
// Everything the broker delivers is turned into Events on a channel, so the
// controller can run on a single goroutine.
package mqttio

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/heatctl/heatctl/internal/config"
	"github.com/heatctl/heatctl/internal/logx"
)

// Kind is the type of an Event.
type Kind int

const (
	Message Kind = iota
	Connected
	Disconnected
)

// Event is something that happened on the broker connection.
type Event struct {
	Kind     Kind
	Topic    string
	Payload  []byte
	Retained bool
	Err      error
}

// ErrNotConnected is returned by Publish while the broker is unreachable.
var ErrNotConnected = errors.New("not connected to the broker")

// MaxReconnectInterval caps the reconnect backoff (R2).
const MaxReconnectInterval = 30 * time.Second

// Client is a broker connection.
type Client struct {
	c        mqtt.Client
	log      *logx.Logger
	events   chan<- Event
	topics   []string
	inflight sync.WaitGroup
}

// New prepares a client. It does not connect.
func New(cfg config.MQTT, topics []string, events chan<- Event, log *logx.Logger) *Client {
	cl := &Client{log: log, events: events, topics: topics}
	opts := mqtt.NewClientOptions()
	opts.AddBroker(brokerURL(cfg.Broker))
	opts.SetClientID(cfg.ClientID)
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
	}
	if cfg.Password != "" {
		opts.SetPassword(cfg.Password)
	}
	opts.SetProtocolVersion(4) // MQTT 3.1.1
	opts.SetCleanSession(true)
	opts.SetResumeSubs(false)
	opts.SetKeepAlive(30 * time.Second)
	opts.SetPingTimeout(10 * time.Second)
	opts.SetConnectTimeout(10 * time.Second)
	opts.SetWriteTimeout(10 * time.Second)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(5 * time.Second)
	opts.SetMaxReconnectInterval(MaxReconnectInterval)
	opts.SetOrderMatters(true)
	opts.SetDefaultPublishHandler(func(_ mqtt.Client, m mqtt.Message) {
		cl.events <- Event{Kind: Message, Topic: m.Topic(), Payload: append([]byte(nil), m.Payload()...), Retained: m.Retained()}
	})
	opts.SetOnConnectHandler(cl.onConnect)
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		cl.events <- Event{Kind: Disconnected, Err: err}
	})
	opts.SetConnectionAttemptHandler(func(_ *url.URL, tc *tls.Config) *tls.Config {
		cl.log.Event("mqtt_connecting", "broker", redact(cfg.Broker))
		return tc
	})
	cl.c = mqtt.NewClient(opts)
	return cl
}

// Connect starts connecting in the background; it retries until it succeeds
// or Disconnect is called.
func (cl *Client) Connect() {
	cl.c.Connect()
}

func (cl *Client) onConnect(c mqtt.Client) {
	filters := make(map[string]byte, len(cl.topics))
	for _, t := range cl.topics {
		filters[t] = 1
	}
	tok := c.SubscribeMultiple(filters, nil)
	if !tok.WaitTimeout(15*time.Second) || tok.Error() != nil {
		err := tok.Error()
		if err == nil {
			err = errors.New("subscribe timed out")
		}
		cl.log.Event("mqtt_subscribe_failed", "error", err.Error())
		// Drop the connection; the reconnect logic will try again.
		go c.Disconnect(100)
		return
	}
	cl.events <- Event{Kind: Connected}
}

// Publish sends a non-retained QoS 1 message. It never blocks for long and
// never queues a message for later: a command sent while disconnected is
// dropped, and the controller re-sends desired states after reconnecting.
func (cl *Client) Publish(topic, payload string) error {
	if !cl.c.IsConnectionOpen() {
		return ErrNotConnected
	}
	tok := cl.c.Publish(topic, 1, false, payload)
	cl.inflight.Add(1)
	go func() {
		defer cl.inflight.Done()
		if !tok.WaitTimeout(10 * time.Second) {
			cl.log.Event("mqtt_publish_timeout", "topic", topic, "payload", payload)
			return
		}
		if err := tok.Error(); err != nil {
			cl.log.Event("mqtt_publish_failed", "topic", topic, "payload", payload, "error", err.Error())
		}
	}()
	return nil
}

// Flush waits up to timeout for published messages to be acknowledged.
func (cl *Client) Flush(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() { cl.inflight.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Disconnect closes the connection, waiting at most quiesce.
func (cl *Client) Disconnect(quiesce time.Duration) {
	cl.c.Disconnect(uint(quiesce / time.Millisecond))
}

// brokerURL maps the accepted URL schemes onto the ones paho understands.
func brokerURL(s string) string {
	switch {
	case strings.HasPrefix(s, "mqtt://"):
		return "tcp://" + strings.TrimPrefix(s, "mqtt://")
	case strings.HasPrefix(s, "mqtts://"):
		return "ssl://" + strings.TrimPrefix(s, "mqtts://")
	}
	return s
}

// redact removes credentials from a broker URL for logging.
func redact(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	u.User = nil
	return u.String()
}

// Describe returns a printable broker address.
func Describe(cfg config.MQTT) string {
	return fmt.Sprintf("%s (client id %s)", redact(cfg.Broker), cfg.ClientID)
}
