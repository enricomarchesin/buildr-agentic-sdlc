package mqtt

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

const (
	connectTimeout = 10 * time.Second
	publishTimeout = 2 * time.Second
	subscribeWait  = 5 * time.Second
	keepAlive      = 30 * time.Second
	pingTimeout    = 10 * time.Second
	writeTimeout   = 5 * time.Second
	// maxReconnect is the longest pause between reconnection attempts. The
	// specification allows up to 60s; a shorter cap keeps recovery well inside
	// the 60s requirement of F6.AC1.
	maxReconnect = 30 * time.Second
)

// Options configures a broker connection.
type Options struct {
	Broker   string
	ClientID string
	Username string
	Password string
}

// PahoClient is the production Client, backed by eclipse/paho.mqtt.golang.
type PahoClient struct {
	client paho.Client
	h      Handlers

	mu     sync.Mutex
	first  bool
	everUp bool
}

// New creates a client for the given broker. It does not connect yet.
func New(opts Options, h Handlers) *PahoClient {
	c := &PahoClient{h: h, first: true}
	o := paho.NewClientOptions()
	o.AddBroker(opts.Broker)
	o.SetClientID(opts.ClientID)
	if opts.Username != "" {
		o.SetUsername(opts.Username)
	}
	if opts.Password != "" {
		o.SetPassword(opts.Password)
	}
	o.SetCleanSession(true)
	o.SetAutoReconnect(true)
	o.SetMaxReconnectInterval(maxReconnect)
	o.SetConnectRetry(false)
	o.SetConnectTimeout(connectTimeout)
	o.SetKeepAlive(keepAlive)
	o.SetPingTimeout(pingTimeout)
	o.SetWriteTimeout(writeTimeout)
	// Deliver messages to the handler one at a time, in the order the broker
	// sent them, so that a sensor's last reading is really its last reading.
	o.SetOrderMatters(true)
	// Nothing heatctl publishes is retained, and no last will is configured:
	// the dead-man's switch is the relay lease (I1).
	o.SetDefaultPublishHandler(func(_ paho.Client, m paho.Message) {
		if c.h.OnMessage == nil {
			return
		}
		c.h.OnMessage(Message{
			Topic:    m.Topic(),
			Payload:  append([]byte(nil), m.Payload()...),
			Retained: m.Retained(),
		})
	})
	o.SetOnConnectHandler(func(_ paho.Client) {
		c.mu.Lock()
		first := c.first
		c.first = false
		c.everUp = true
		c.mu.Unlock()
		if c.h.OnConnect != nil {
			c.h.OnConnect(first)
		}
	})
	o.SetConnectionLostHandler(func(_ paho.Client, err error) {
		if c.h.OnConnectionLost != nil {
			c.h.OnConnectionLost(err)
		}
	})
	c.client = paho.NewClient(o)
	return c
}

// Connect implements Client.
func (c *PahoClient) Connect(ctx context.Context) error {
	tok := c.client.Connect()
	deadline := time.Now().Add(connectTimeout)
	for {
		if err := ctx.Err(); err != nil {
			c.client.Disconnect(0)
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			c.client.Disconnect(0)
			return fmt.Errorf("timed out connecting to broker")
		}
		wait := remaining
		if wait > 100*time.Millisecond {
			wait = 100 * time.Millisecond
		}
		if tok.WaitTimeout(wait) {
			break
		}
	}
	if err := tok.Error(); err != nil {
		c.client.Disconnect(0)
		return err
	}
	return nil
}

// Subscribe implements Client.
func (c *PahoClient) Subscribe(topic string, qos byte) error {
	if !c.client.IsConnectionOpen() {
		return ErrNotConnected
	}
	tok := c.client.Subscribe(topic, qos, nil)
	if !tok.WaitTimeout(subscribeWait) {
		return fmt.Errorf("timed out subscribing to %s", topic)
	}
	return tok.Error()
}

// Publish implements Client. Messages are never retained.
func (c *PahoClient) Publish(topic string, payload []byte, qos byte) error {
	if !c.client.IsConnectionOpen() {
		return ErrNotConnected
	}
	tok := c.client.Publish(topic, qos, false, payload)
	if !tok.WaitTimeout(publishTimeout) {
		return fmt.Errorf("timed out publishing to %s", topic)
	}
	return tok.Error()
}

// Disconnect implements Client.
func (c *PahoClient) Disconnect(quiesce time.Duration) {
	c.client.Disconnect(uint(quiesce / time.Millisecond))
}

// IsConnected implements Client.
func (c *PahoClient) IsConnected() bool {
	return c.client.IsConnectionOpen()
}

// ErrClosed reports that the client was closed before it connected.
var ErrClosed = errors.New("mqtt client closed")
