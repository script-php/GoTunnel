package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const MaxMessageSize = 10 * 1024 * 1024

// Read reads one bounded frame, validating its length before allocating payload memory.
func Read(r io.Reader) (*Message, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > MaxMessageSize {
		return nil, fmt.Errorf("message size %d exceeds maximum %d", length, MaxMessageSize)
	}
	frame := make([]byte, 5+int(length))
	copy(frame, header[:])
	if _, err := io.ReadFull(r, frame[5:]); err != nil {
		return nil, err
	}
	return Decode(frame)
}

// MessageType represents the type of message
type MessageType byte

const (
	// Authentication messages
	MessageTypeAuth MessageType = iota
	MessageTypeAuthResponse

	// Tunnel configuration
	MessageTypeTunnelConfig

	// Stream management
	MessageTypeStreamOpen
	MessageTypeStreamData
	MessageTypeStreamClose
	MessageTypeStreamReady

	// Keep-alive
	MessageTypePing
	MessageTypePong
)

// Message is the base structure for all protocol messages
type Message struct {
	Type    MessageType
	Payload interface{}
}

// MessageAuth - Client authenticates to server
type MessageAuth struct {
	MachineID string `json:"machine_id"`
	Password  string `json:"password"` // Will be hashed
}

// MessageAuthResponse - Server responds to auth
type MessageAuthResponse struct {
	Success bool        `json:"success"`
	Error   string      `json:"error,omitempty"`
	Tunnels []TunnelMap `json:"tunnels,omitempty"`
}

// TunnelMap represents a port mapping
type TunnelMap struct {
	Remote int `json:"remote"`
	Local  int `json:"local"`
}

// MessageTunnelConfig - Server sends tunnel configuration to client
type MessageTunnelConfig struct {
	Tunnels []TunnelMap `json:"tunnels"`
}

// MessageStreamOpen - Server requests client to open a stream
type MessageStreamOpen struct {
	StreamID  uint32 `json:"stream_id"`
	LocalPort int    `json:"local_port"`
}

// MessageStreamData - Data transferred between client and server
type MessageStreamData struct {
	StreamID uint32 `json:"stream_id"`
	Data     []byte `json:"data"`
}

// MessageStreamClose - Close a stream
type MessageStreamClose struct {
	StreamID uint32 `json:"stream_id"`
	Reason   string `json:"reason,omitempty"`
}

// MessageStreamReady - Client acknowledges stream is ready
type MessageStreamReady struct {
	StreamID uint32 `json:"stream_id"`
}

// MessagePing - Keep-alive ping
type MessagePing struct {
	Timestamp int64 `json:"timestamp"`
}

// MessagePong - Keep-alive pong
type MessagePong struct {
	Timestamp int64 `json:"timestamp"`
}

// Encode serializes a message into bytes
func Encode(msg *Message) ([]byte, error) {
	// First, serialize the payload to JSON
	var payloadBytes []byte
	var err error

	if msg.Payload != nil {
		payloadBytes, err = json.Marshal(msg.Payload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal payload: %w", err)
		}
	}

	// Create buffer: [type:1byte] [length:4bytes] [payload:variable]
	buf := new(bytes.Buffer)

	// Write message type
	if err := binary.Write(buf, binary.BigEndian, msg.Type); err != nil {
		return nil, err
	}

	// Write payload length
	if err := binary.Write(buf, binary.BigEndian, int32(len(payloadBytes))); err != nil {
		return nil, err
	}

	// Write payload
	if len(payloadBytes) > 0 {
		if err := binary.Write(buf, binary.BigEndian, payloadBytes); err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}

// Decode deserializes bytes into a message
func Decode(data []byte) (*Message, error) {
	if len(data) < 5 { // minimum: 1 byte type + 4 bytes length
		return nil, fmt.Errorf("message too short")
	}

	buf := bytes.NewReader(data)

	// Read message type
	var msgType MessageType
	if err := binary.Read(buf, binary.BigEndian, &msgType); err != nil {
		return nil, err
	}

	// Read payload length
	var length int32
	if err := binary.Read(buf, binary.BigEndian, &length); err != nil {
		return nil, err
	}

	if length < 0 {
		return nil, fmt.Errorf("invalid payload length: %d", length)
	}

	// Enforce maximum message size (10MB) to prevent DoS attacks
	if length > MaxMessageSize {
		return nil, fmt.Errorf("message size %d exceeds maximum %d", length, MaxMessageSize)
	}

	// Read payload
	payloadBytes := make([]byte, length)
	if length > 0 {
		if err := binary.Read(buf, binary.BigEndian, payloadBytes); err != nil {
			return nil, err
		}
	}

	// Deserialize payload based on message type
	var payload interface{}
	var err error

	if length > 0 {
		switch msgType {
		case MessageTypeAuth:
			var msg MessageAuth
			err = json.Unmarshal(payloadBytes, &msg)
			payload = msg
		case MessageTypeAuthResponse:
			var msg MessageAuthResponse
			err = json.Unmarshal(payloadBytes, &msg)
			payload = msg
		case MessageTypeTunnelConfig:
			var msg MessageTunnelConfig
			err = json.Unmarshal(payloadBytes, &msg)
			payload = msg
		case MessageTypeStreamOpen:
			var msg MessageStreamOpen
			err = json.Unmarshal(payloadBytes, &msg)
			payload = msg
		case MessageTypeStreamData:
			var msg MessageStreamData
			err = json.Unmarshal(payloadBytes, &msg)
			payload = msg
		case MessageTypeStreamClose:
			var msg MessageStreamClose
			err = json.Unmarshal(payloadBytes, &msg)
			payload = msg
		case MessageTypeStreamReady:
			var msg MessageStreamReady
			err = json.Unmarshal(payloadBytes, &msg)
			payload = msg
		case MessageTypePing:
			var msg MessagePing
			err = json.Unmarshal(payloadBytes, &msg)
			payload = msg
		case MessageTypePong:
			var msg MessagePong
			err = json.Unmarshal(payloadBytes, &msg)
			payload = msg
		default:
			return nil, fmt.Errorf("unknown message type: %d", msgType)
		}

		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal payload: %w", err)
		}
	}

	return &Message{
		Type:    msgType,
		Payload: payload,
	}, nil
}
