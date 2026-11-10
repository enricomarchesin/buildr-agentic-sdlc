// Package bus wraps the MQTT client. Nothing is ever published retained.
package bus

import (
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"heatctl/internal/config"
)

type Kind int

const (
	Message Kind = iota
	Connected
	Disconnected
)

type Event struct {
	Kind     Kind
	Topic    string
	Payload  []byte
	Retained bool
	Err      error
}

type Client struct {
	c mqtt.Client
}

// New creates a client that connects (and reconnects with backoff capped at
// 60 s) in the background. Events are delivered on out.
func New(cfg config.MQTT, topics []string, out chan<- Event) *Client {
	opts := mqtt.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(cfg.ClientID).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(2 * time.Second).
		SetMaxReconnectInterval(60 * time.Second).
		SetKeepAlive(30 * time.Second).
		SetPingTimeout(10 * time.Second).
		SetConnectTimeout(10 * time.Second).
		SetWriteTimeout(10 * time.Second)
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username).SetPassword(cfg.Password)
	}
	handler := func(_ mqtt.Client, m mqtt.Message) {
		out <- Event{Kind: Message, Topic: m.Topic(), Payload: append([]byte(nil), m.Payload()...), Retained: m.Retained()}
	}
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		out <- Event{Kind: Disconnected, Err: err}
	})
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		filters := map[string]byte{}
		for _, t := range topics {
			filters[t] = 0
		}
		if len(filters) > 0 {
			tok := c.SubscribeMultiple(filters, handler)
			if !tok.WaitTimeout(15*time.Second) || tok.Error() != nil {
				// Subscription failed: drop the connection so that it is retried.
				err := tok.Error()
				c.Disconnect(0)
				out <- Event{Kind: Disconnected, Err: err}
				go func() { time.Sleep(2 * time.Second); c.Connect() }()
				return
			}
		}
		out <- Event{Kind: Connected}
	})
	return &Client{c: mqtt.NewClient(opts)}
}

func (b *Client) Connect() { b.c.Connect() }

// Publish sends a QoS 0, non-retained message without waiting.
func (b *Client) Publish(topic, payload string) {
	b.c.Publish(topic, 0, false, payload)
}

// PublishWait publishes and waits up to d for the write to complete.
func (b *Client) PublishWait(topic, payload string, d time.Duration) {
	b.c.Publish(topic, 1, false, payload).WaitTimeout(d)
}

func (b *Client) Close() {
	b.c.Disconnect(250)
}
