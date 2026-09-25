package client

import (
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/yoyo/gotunnel/internal/config"
	"github.com/yoyo/gotunnel/internal/protocol"
	"github.com/yoyo/gotunnel/internal/tunnel"
)

// Constants for timeouts and stream management
const (
	ReadTimeoutDuration     = 20 * time.Second
	WriteTimeoutDuration    = 20 * time.Second
	StreamTimeoutDuration   = 60 * time.Second
	KeepaliveInterval       = 10 * time.Second
	MaxConcurrentLocalConns = 50 // Prevent file descriptor exhaustion
	MaxStreamsPerClient     = 100
)

// Client handles the client-side tunnel logic
type Client struct {
	serverAddr        string
	machineID         string
	password          string
	reconnectInterval time.Duration

	conn net.Conn

	// Streams
	streams   map[uint32]*tunnel.Stream
	streamsMu sync.RWMutex

	// Tunnels
	tunnels   []protocol.TunnelMap
	tunnelsMu sync.RWMutex

	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once

	connected bool
	connMu    sync.Mutex
}

// NewClient creates a new client instance
func NewClient(serverAddr, machineID, password string) *Client {
	return &Client{
		serverAddr:        serverAddr,
		machineID:         machineID,
		password:          password,
		reconnectInterval: 5 * time.Second,
		streams:           make(map[uint32]*tunnel.Stream),
		stopCh:            make(chan struct{}),
		doneCh:            make(chan struct{}),
	}
}

// SetReconnectInterval configures how long the client waits between attempts.
func (c *Client) SetReconnectInterval(interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("reconnect interval must be greater than zero")
	}
	c.reconnectInterval = interval
	return nil
}

// Start starts the client and connects to server
func (c *Client) Start() error {
	log.Printf("GoTunnel Client %s", config.Version)
	log.Printf("Connecting to: %s", c.serverAddr)
	log.Printf("Machine ID: %s", c.machineID)

	go c.reconnectLoop()

	return nil
}

// Stop stops the client
func (c *Client) Stop() error {
	c.stopOnce.Do(func() {
		close(c.stopCh)
		c.closeConnection()
	})
	<-c.doneCh
	return nil
}

// reconnectLoop maintains connection to server with automatic reconnection
func (c *Client) reconnectLoop() {
	defer close(c.doneCh)

	for {
		select {
		case <-c.stopCh:
			return
		default:
		}

		if err := c.connect(); err != nil {
			log.Printf("Connection failed: %v", err)
			log.Printf("Retrying in %v...", c.reconnectInterval)

			select {
			case <-c.stopCh:
				return
			case <-time.After(c.reconnectInterval):
			}
			continue
		}

		// Connection successful, handle messages
		c.handleMessages()

		// Connection lost, retry
		log.Printf("Connection lost, reconnecting...")
		c.closeConnection()

		select {
		case <-c.stopCh:
			return
		case <-time.After(c.reconnectInterval):
		}
	}
}

// connect connects to the server and authenticates
func (c *Client) connect() error {
	conn, err := net.DialTimeout("tcp", c.serverAddr, ReadTimeoutDuration)
	if err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}

	// FIX #5: Lock before setting connection state
	c.connMu.Lock()
	c.conn = conn
	c.connected = true
	c.connMu.Unlock()
	if err := conn.SetDeadline(time.Now().Add(ReadTimeoutDuration)); err != nil {
		c.closeConnection()
		return fmt.Errorf("failed to set authentication deadline: %w", err)
	}

	// Send authentication message
	authMsg := &protocol.Message{
		Type: protocol.MessageTypeAuth,
		Payload: protocol.MessageAuth{
			MachineID: c.machineID,
			Password:  c.password,
		},
	}

	if err := c.sendMessage(authMsg); err != nil {
		conn.Close()
		c.connMu.Lock()
		c.conn = nil
		c.connected = false
		c.connMu.Unlock()
		return fmt.Errorf("failed to send auth message: %w", err)
	}

	// Read authentication response
	msg, err := c.readMessage()
	if err != nil {
		conn.Close()
		c.connMu.Lock()
		c.conn = nil
		c.connected = false
		c.connMu.Unlock()
		return fmt.Errorf("failed to read auth response: %w", err)
	}

	if msg.Type != protocol.MessageTypeAuthResponse {
		conn.Close()
		c.connMu.Lock()
		c.conn = nil
		c.connected = false
		c.connMu.Unlock()
		return fmt.Errorf("expected AUTH_RESPONSE, got type %d", msg.Type)
	}

	authResponse, ok := msg.Payload.(protocol.MessageAuthResponse)
	if !ok {
		conn.Close()
		c.connMu.Lock()
		c.conn = nil
		c.connected = false
		c.connMu.Unlock()
		return fmt.Errorf("invalid auth response payload")
	}

	if !authResponse.Success {
		conn.Close()
		c.connMu.Lock()
		c.conn = nil
		c.connected = false
		c.connMu.Unlock()
		return fmt.Errorf("authentication failed: %s", authResponse.Error)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		c.closeConnection()
		return fmt.Errorf("failed to clear authentication deadline: %w", err)
	}

	// Store tunnels
	c.tunnelsMu.Lock()
	c.tunnels = authResponse.Tunnels
	c.tunnelsMu.Unlock()

	log.Printf("Authenticated as: %s", c.machineID)
	if len(authResponse.Tunnels) > 0 {
		log.Printf("Tunnels configured:")
		for _, t := range authResponse.Tunnels {
			log.Printf("   %d → localhost:%d", t.Remote, t.Local)
		}
	}

	return nil
}

// handleMessages handles incoming messages from server
func (c *Client) handleMessages() {
	// Enable TCP keepalive to detect half-open connections
	if tcpConn, ok := c.conn.(*net.TCPConn); ok {
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(30 * time.Second)
	}

	// Start PING/PONG keepalive ticker
	ticker := time.NewTicker(KeepaliveInterval)
	defer ticker.Stop()

	// Set read/write deadlines for the connection
	if c.conn != nil {
		c.conn.SetReadDeadline(time.Now().Add(ReadTimeoutDuration))
		c.conn.SetWriteDeadline(time.Now().Add(WriteTimeoutDuration))
	}

	// Message reading goroutine
	msgCh := make(chan *protocol.Message)
	errCh := make(chan error)
	go func() {
		defer close(msgCh) // FIX #3: Close channel when reader exits to prevent leak
		for {
			msg, err := c.readMessage()
			if err != nil {
				errCh <- err
				return
			}
			msgCh <- msg
		}
	}()

	for {
		select {
		case <-c.stopCh:
			return

		case <-ticker.C:
			// Send periodic PING to detect dead connection
			pingMsg := &protocol.Message{
				Type: protocol.MessageTypePing,
				Payload: protocol.MessagePing{
					Timestamp: time.Now().Unix(),
				},
			}
			if err := c.sendMessage(pingMsg); err != nil {
				log.Printf("Failed to send PING: %v", err)
				return
			}
			// Refresh read/write deadlines after each PING
			if c.conn != nil {
				c.conn.SetReadDeadline(time.Now().Add(ReadTimeoutDuration))
				c.conn.SetWriteDeadline(time.Now().Add(WriteTimeoutDuration))
			}

		case err := <-errCh:
			if err != io.EOF {
				log.Printf("Error reading message: %v", err)
			}
			return

		case msg := <-msgCh:

			switch msg.Type {
			case protocol.MessageTypeTunnelConfig:
				payload, ok := msg.Payload.(protocol.MessageTunnelConfig)
				if !ok {
					log.Printf("Invalid TUNNEL_CONFIG payload")
					continue
				}
				c.handleTunnelConfig(payload)

			case protocol.MessageTypeStreamOpen:
				payload, ok := msg.Payload.(protocol.MessageStreamOpen)
				if !ok {
					log.Printf("Invalid STREAM_OPEN payload")
					continue
				}
				c.handleStreamOpen(payload)

			case protocol.MessageTypeStreamClose:
				payload, ok := msg.Payload.(protocol.MessageStreamClose)
				if !ok {
					log.Printf("Invalid STREAM_CLOSE payload")
					continue
				}
				c.handleStreamClose(payload)

			case protocol.MessageTypeStreamData:
				payload, ok := msg.Payload.(protocol.MessageStreamData)
				if !ok {
					log.Printf("Invalid STREAM_DATA payload")
					continue
				}
				c.handleIncomingStreamData(payload)

			case protocol.MessageTypePing:
				payload, ok := msg.Payload.(protocol.MessagePing)
				if ok {
					response := &protocol.Message{
						Type: protocol.MessageTypePong,
						Payload: protocol.MessagePong{
							Timestamp: payload.Timestamp,
						},
					}
					c.sendMessage(response)
					log.Printf("Received PING from server, sending PONG")
				}

			case protocol.MessageTypePong:
				// Server acknowledged our ping, connection is alive
				if _, ok := msg.Payload.(protocol.MessagePong); ok {
					log.Printf("Received PONG from server")
				}

			default:
				log.Printf("Unknown message type: %d", msg.Type)
			}
		}
	}
}

// handleTunnelConfig processes tunnel configuration update
func (c *Client) handleTunnelConfig(payload protocol.MessageTunnelConfig) {
	c.tunnelsMu.Lock()
	c.tunnels = payload.Tunnels
	c.tunnelsMu.Unlock()

	log.Printf("Tunnels updated:")
	for _, t := range payload.Tunnels {
		log.Printf("   %d → localhost:%d", t.Remote, t.Local)
	}
}

// handleStreamOpen handles server request to open a stream
func (c *Client) handleStreamOpen(payload protocol.MessageStreamOpen) {
	streamID := payload.StreamID
	localPort := payload.LocalPort

	// Check stream limit
	c.streamsMu.RLock()
	streamCount := len(c.streams)
	c.streamsMu.RUnlock()

	if streamCount >= MaxStreamsPerClient {
		log.Printf("Stream limit exceeded: %d/%d streams", streamCount, MaxStreamsPerClient)
		msg := &protocol.Message{
			Type: protocol.MessageTypeStreamClose,
			Payload: protocol.MessageStreamClose{
				StreamID: streamID,
				Reason:   "client stream limit exceeded",
			},
		}
		c.sendMessage(msg)
		return
	}

	// Connect to local service with 5-second timeout
	localAddr := fmt.Sprintf("127.0.0.1:%d", localPort)
	localConn, err := net.DialTimeout("tcp", localAddr, 5*time.Second)
	if err != nil {
		log.Printf("Stream %d: failed to connect to localhost:%d (timeout 5s): %v", streamID, localPort, err)
		// Send stream close
		msg := &protocol.Message{
			Type: protocol.MessageTypeStreamClose,
			Payload: protocol.MessageStreamClose{
				StreamID: streamID,
				Reason:   fmt.Sprintf("failed to connect to localhost:%d", localPort),
			},
		}
		c.sendMessage(msg)
		return
	}

	// Create stream with local connection
	stream := tunnel.NewStream(streamID, localConn, nil)

	// Store stream in map
	c.streamsMu.Lock()
	c.streams[streamID] = stream
	c.streamsMu.Unlock()

	// Only set write deadline to prevent hangs on sends
	// Don't set read deadline - local service may send data slowly
	localConn.SetWriteDeadline(time.Now().Add(StreamTimeoutDuration))

	log.Printf("Stream %d opened (local: %d)", streamID, localPort)

	// Send STREAM_READY
	readyMsg := &protocol.Message{
		Type: protocol.MessageTypeStreamReady,
		Payload: protocol.MessageStreamReady{
			StreamID: streamID,
		},
	}
	if err := c.sendMessage(readyMsg); err != nil {
		log.Printf("Stream %d: failed to send STREAM_READY: %v", streamID, err)
		stream.Close()
		c.streamsMu.Lock()
		delete(c.streams, streamID)
		c.streamsMu.Unlock()
		return
	}

	log.Printf("Stream %d connected to localhost:%d", streamID, localPort)

	// Start handling bidirectional data
	c.handleStreamData(streamID, stream)
}

// handleStreamData handles bidirectional data transfer for a stream
func (c *Client) handleStreamData(streamID uint32, stream *tunnel.Stream) {
	var wg sync.WaitGroup

	// Goroutine: Read from localhost and send to server
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 32*1024)

		for {
			// Check stop signal and stream validity first
			select {
			case <-c.stopCh:
				return
			default:
			}

			// Lock stream to check if it's still valid
			stream.Mu.Lock()
			if stream.Closed || stream.Conn1 == nil {
				stream.Mu.Unlock()
				return
			}
			stream.Mu.Unlock()

			// Set read timeout to prevent blocking indefinitely on slow local service
			// Also allows for periodic stop signal checks
			stream.Conn1.SetReadDeadline(time.Now().Add(5 * time.Second))

			n, err := stream.Conn1.Read(buf)
			if err != nil {
				// Check if it's a timeout (expected, will retry)
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue // Retry read with stop signal check
				}
				if err != io.EOF {
					// Suppress logging for expected connection closure errors
					errStr := err.Error()
					if !strings.Contains(errStr, "use of closed network connection") &&
						!strings.Contains(errStr, "connection reset") &&
						!strings.Contains(errStr, "broken pipe") {
						log.Printf("Stream %d read error: %v", streamID, err)
					}
				}
				// Send stream close
				msg := &protocol.Message{
					Type: protocol.MessageTypeStreamClose,
					Payload: protocol.MessageStreamClose{
						StreamID: streamID,
						Reason:   "local connection closed",
					},
				}
				c.sendMessage(msg)
				return
			}

			if n > 0 {

				// Refresh write deadline after each successful read
				stream.Conn1.SetWriteDeadline(time.Now().Add(StreamTimeoutDuration))

				// Send data to server
				dataMsg := &protocol.Message{
					Type: protocol.MessageTypeStreamData,
					Payload: protocol.MessageStreamData{
						StreamID: streamID,
						Data:     buf[:n],
					},
				}
				if err := c.sendMessage(dataMsg); err != nil {
					log.Printf("Stream %d send error: %v", streamID, err)
					return
				}
			}
		}
	}()

	// FIX #1: Simplified cleanup - wait for reader to finish, then close stream
	// Cleanup goroutine NOT part of WaitGroup (prevents deadlock)
	go func() {
		wg.Wait() // Wait for reader goroutine to finish
		c.closeStream(streamID)
	}()
}

// handleIncomingStreamData handles data received from server for a stream
func (c *Client) handleIncomingStreamData(payload protocol.MessageStreamData) {
	c.streamsMu.RLock()
	stream, exists := c.streams[payload.StreamID]
	c.streamsMu.RUnlock()

	if !exists {
		log.Printf("Stream %d not found", payload.StreamID)
		return
	}

	if stream == nil {
		log.Printf("Stream %d is nil", payload.StreamID)
		return
	}

	// Snapshot the connection under the lifecycle lock. Closing a net.Conn
	// concurrently with Write is safe and must be able to interrupt blocked I/O.
	stream.Mu.Lock()
	conn := stream.Conn1
	closed := stream.Closed
	stream.Mu.Unlock()
	if closed || conn == nil {
		c.closeStream(payload.StreamID)
		return
	}
	conn.SetWriteDeadline(time.Now().Add(StreamTimeoutDuration))
	if _, err := conn.Write(payload.Data); err != nil {
		log.Printf("Stream %d write error: %v", payload.StreamID, err)
		c.closeStream(payload.StreamID)
	}

}

// handleStreamClose handles server stream close
func (c *Client) handleStreamClose(payload protocol.MessageStreamClose) {
	c.closeStream(payload.StreamID)
	log.Printf("Stream %d closed: %s", payload.StreamID, payload.Reason)
}

// closeStream closes a stream and cleans up resources
func (c *Client) closeStream(streamID uint32) {
	c.streamsMu.Lock()
	stream, exists := c.streams[streamID]
	if exists {
		delete(c.streams, streamID)
	}
	c.streamsMu.Unlock()

	if exists && stream != nil {
		stream.Close()
	}
}

// readMessage reads a message from server
func (c *Client) readMessage() (*protocol.Message, error) {
	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()
	if conn == nil {
		return nil, fmt.Errorf("not connected")
	}
	return protocol.Read(conn)
}

// sendMessage sends a message to server
func (c *Client) sendMessage(msg *protocol.Message) error {
	c.connMu.Lock()
	if !c.connected || c.conn == nil {
		c.connMu.Unlock()
		return fmt.Errorf("not connected to server")
	}
	conn := c.conn
	c.connMu.Unlock()

	data, err := protocol.Encode(msg)
	if err != nil {
		return err
	}

	_, err = conn.Write(data)
	return err
}

// closeConnection closes the connection and cleans up all streams
func (c *Client) closeConnection() {
	c.connMu.Lock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.connected = false
	c.connMu.Unlock()

	// Close all streams
	c.streamsMu.Lock()
	for _, stream := range c.streams {
		stream.Close()
	}
	c.streams = make(map[uint32]*tunnel.Stream)
	c.streamsMu.Unlock()

}

// IsConnected returns whether the client is connected
func (c *Client) IsConnected() bool {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.connected
}
