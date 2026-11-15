// Package mqtt wraps the MQTT client heatctl uses to reach sensors and relays.
package mqtt

import (
	"context"
	"errors"
	"time"
)

// ErrNotConnected is returned when a publish is attempted while the client is
// not connected to the broker.
var ErrNotConnected = errors.New("not connected to the broker")

// Message is one inbound MQTT message.
type Message struct {
	Topic    string
	Payload  []byte
	Retained bool
}

// Handlers receives connection and message callbacks. OnConnect is called
// after every successful connection, including reconnections, with first true
// only for the first one. The callbacks run on the client's goroutines and
// must not block for long.
type Handlers struct {
	OnConnect        func(first bool)
	OnConnectionLost func(err error)
	OnMessage        func(Message)
}

// Client is the subset of MQTT behaviour heatctl needs. MQTT 3.1.1, at most
// QoS 1, nothing retained (I1).
type Client interface {
	// Connect establishes the first connection. Reconnection after a loss is
	// handled internally and reported through Handlers.
	Connect(ctx context.Context) error
	// Subscribe registers the client's message handler for a topic filter.
	// Subscriptions do not survive a reconnect; the controller subscribes
	// again in OnConnect.
	Subscribe(topic string, qos byte) error
	// Publish sends a message. The returned error means the broker did not
	// accept it.
	Publish(topic string, payload []byte, qos byte) error
	// Disconnect closes the connection.
	Disconnect(quiesce time.Duration)
	// IsConnected reports whether the connection is currently open.
	IsConnected() bool
}

// QoSSensor is the QoS used for sensor and relay status subscriptions.
const QoSSensor byte = 1

// QoSCommand is the QoS used for relay commands.
const QoSCommand byte = 1
