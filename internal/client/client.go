package client

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yoyo/gotunnel/internal/config"
	"github.com/yoyo/gotunnel/internal/protocol"
	"github.com/yoyo/gotunnel/internal/systemmetrics"
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
	MaxReconnectInterval    = time.Minute
)

// Client handles the client-side tunnel logic
type Client struct {
	serverAddr        string
	machineID         string
	password          string
	reconnectInterval time.Duration

	conn    net.Conn
	writeMu sync.Mutex

	// Streams
	streams   map[uint32]*tunnel.Stream
	streamsMu sync.RWMutex

	// Tunnels
	tunnels   []protocol.TunnelMap
	tunnelsMu sync.RWMutex

	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once

	connected     bool
	connMu        sync.Mutex
	dialSlots     chan struct{}
	tlsConfig     *tls.Config
	startedAt     time.Time
	metrics       systemmetrics.Collector
	uploadBytes   atomic.Uint64
	downloadBytes atomic.Uint64
}

// EnableTLS enables verified TLS for the control connection. caFile may be
// empty to use the operating system trust store.
func (c *Client) EnableTLS(caFile, serverName string) error {
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if caFile != "" {
		pemData, err := os.ReadFile(caFile)
		if err != nil {
			return fmt.Errorf("read TLS CA: %w", err)
		}
		if !roots.AppendCertsFromPEM(pemData) {
			return fmt.Errorf("TLS CA file contains no certificates")
		}
	}
	if serverName == "" {
		host, _, err := net.SplitHostPort(c.serverAddr)
		if err != nil {
			return fmt.Errorf("derive TLS server name: %w", err)
		}
		serverName = host
	}
	c.tlsConfig = &tls.Config{RootCAs: roots, ServerName: serverName, MinVersion: tls.VersionTLS13}
	return nil
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
		dialSlots:         make(chan struct{}, MaxConcurrentLocalConns),
		startedAt:         time.Now(),
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
	failures := 0

	for {
		select {
		case <-c.stopCh:
			return
		default:
		}

		if err := c.connect(); err != nil {
			log.Printf("Connection failed: %v", err)
			delay := reconnectDelay(c.reconnectInterval, failures)
			failures++
			log.Printf("Retrying in %v...", delay)
			if !c.waitForRetry(delay) {
				return
			}
			continue
		}
		failures = 0

		// Connection successful, handle messages
		c.handleMessages()

		// Connection lost, retry
		log.Printf("Connection lost, reconnecting...")
		c.closeConnection()

		if !c.waitForRetry(reconnectDelay(c.reconnectInterval, 0)) {
			return
		}
	}
}

func (c *Client) waitForRetry(delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-c.stopCh:
		return false
	case <-timer.C:
		return true
	}
}

func reconnectDelay(base time.Duration, failures int) time.Duration {
	delay := base
	for i := 0; i < failures && delay < MaxReconnectInterval; i++ {
		if delay > MaxReconnectInterval/2 {
			delay = MaxReconnectInterval
			break
		}
		delay *= 2
	}
	if delay > MaxReconnectInterval {
		delay = MaxReconnectInterval
	}
	// Up to 20% downward jitter prevents synchronized reconnect storms while
	// preserving the configured interval as the upper bound.
	jitter := delay / 5
	if jitter <= 0 {
		return delay
	}
	return delay - jitter + time.Duration(rand.Int64N(int64(jitter)+1))
}

// connect connects to the server and authenticates
func (c *Client) connect() error {
	dialer := &net.Dialer{Timeout: ReadTimeoutDuration}
	var conn net.Conn
	var err error
	if c.tlsConfig != nil {
		conn, err = tls.DialWithDialer(dialer, "tcp", c.serverAddr, c.tlsConfig)
	} else {
		conn, err = dialer.Dial("tcp", c.serverAddr)
	}
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
	c.connMu.Lock()
	controlConn := c.conn
	c.connMu.Unlock()
	if controlConn == nil {
		return
	}
	// Enable TCP keepalive to detect half-open connections
	if tcpConn, ok := controlConn.(*net.TCPConn); ok {
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(30 * time.Second)
	}

	// The server sends keepalives. This deadline advances only after receiving
	// real traffic, so a silent peer cannot keep itself alive locally.
	controlConn.SetReadDeadline(time.Now().Add(ReadTimeoutDuration))

	// Message reading goroutine
	msgCh := make(chan *protocol.Message)
	errCh := make(chan error, 1)
	readerDone := make(chan struct{})
	readerExited := make(chan struct{})
	defer func() {
		close(readerDone)
		controlConn.SetReadDeadline(time.Now())
		<-readerExited
	}()
	go func() {
		defer close(readerExited)
		for {
			msg, err := protocol.Read(controlConn)
			if err != nil {
				select {
				case errCh <- err:
				case <-readerDone:
				}
				return
			}
			select {
			case msgCh <- msg:
			case <-readerDone:
				return
			}
		}
	}()
	telemetryTicker := time.NewTicker(5 * time.Second)
	defer telemetryTicker.Stop()
	var previousUpload, previousDownload uint64
	previousAt := time.Now()

	for {
		select {
		case <-c.stopCh:
			return

		case now := <-telemetryTicker.C:
			upload := c.uploadBytes.Load()
			download := c.downloadBytes.Load()
			elapsed := now.Sub(previousAt).Seconds()
			c.streamsMu.RLock()
			activeStreams := len(c.streams)
			c.streamsMu.RUnlock()
			host := c.metrics.Collect()
			telemetry := protocol.MessageTelemetry{
				Version: config.Version, Timestamp: systemmetrics.UnixTimeMillis(), UptimeSeconds: int64(time.Since(c.startedAt).Seconds()),
				CPUPercent: host.CPUPercent, ProcessCPUPercent: host.ProcessCPUPercent,
				MemoryUsedBytes: host.MemoryUsedBytes, MemoryTotalBytes: host.MemoryTotalBytes,
				ProcessRSSBytes: host.ProcessRSSBytes, Goroutines: host.Goroutines, ActiveStreams: activeStreams,
				UploadBytes: upload, DownloadBytes: download,
			}
			if elapsed > 0 {
				telemetry.UploadBytesPerSec = float64(upload-previousUpload) / elapsed
				telemetry.DownloadBytesPerSec = float64(download-previousDownload) / elapsed
			}
			if err := c.sendMessageOn(controlConn, &protocol.Message{Type: protocol.MessageTypeTelemetry, Payload: telemetry}); err != nil {
				return
			}
			previousUpload, previousDownload, previousAt = upload, download, now

		case err := <-errCh:
			if err != io.EOF {
				log.Printf("Error reading message: %v", err)
			}
			return

		case msg := <-msgCh:
			controlConn.SetReadDeadline(time.Now().Add(ReadTimeoutDuration))

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
				select {
				case c.dialSlots <- struct{}{}:
					go func() {
						defer func() { <-c.dialSlots }()
						c.handleStreamOpen(controlConn, payload)
					}()
				default:
					c.sendMessageOn(controlConn, &protocol.Message{Type: protocol.MessageTypeStreamClose, Payload: protocol.MessageStreamClose{StreamID: payload.StreamID, Reason: "too many pending local connections"}})
				}

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
					c.sendMessageOn(controlConn, response)
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
func (c *Client) handleStreamOpen(controlConn net.Conn, payload protocol.MessageStreamOpen) {
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
		c.sendMessageOn(controlConn, msg)
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
		c.sendMessageOn(controlConn, msg)
		return
	}

	// Create stream with local connection
	stream := tunnel.NewStream(streamID, localConn, nil)

	// Store stream in map
	c.streamsMu.Lock()
	if len(c.streams) >= MaxStreamsPerClient || c.streams[streamID] != nil {
		c.streamsMu.Unlock()
		localConn.Close()
		c.sendMessageOn(controlConn, &protocol.Message{Type: protocol.MessageTypeStreamClose, Payload: protocol.MessageStreamClose{StreamID: streamID, Reason: "client stream limit exceeded or duplicate stream"}})
		return
	}
	c.streams[streamID] = stream
	c.streamsMu.Unlock()
	stream.StartWriter(StreamTimeoutDuration, func(err error) {
		log.Printf("Stream %d local write error: %v", streamID, err)
		c.sendMessageOn(controlConn, &protocol.Message{Type: protocol.MessageTypeStreamClose, Payload: protocol.MessageStreamClose{StreamID: streamID, Reason: "local service write failed"}})
		c.closeStream(streamID)
	}, func(complete bool, err error) {
		if err != nil || complete {
			c.closeStream(streamID)
		}
	})

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
	if err := c.sendMessageOn(controlConn, readyMsg); err != nil {
		log.Printf("Stream %d: failed to send STREAM_READY: %v", streamID, err)
		stream.Close()
		c.streamsMu.Lock()
		delete(c.streams, streamID)
		c.streamsMu.Unlock()
		return
	}

	log.Printf("Stream %d connected to localhost:%d", streamID, localPort)

	// Start handling bidirectional data
	c.handleStreamData(controlConn, streamID, stream)
}

// handleStreamData handles bidirectional data transfer for a stream
func (c *Client) handleStreamData(controlConn net.Conn, streamID uint32, stream *tunnel.Stream) {
	go func() {
		buf := make([]byte, 32*1024)

		for {
			// Check stop signal and stream validity first
			select {
			case <-c.stopCh:
				return
			default:
			}

			// Lock stream to check if it's still valid
			conn, open := stream.Connection()
			if !open {
				return
			}

			// Set read timeout to prevent blocking indefinitely on slow local service
			// Also allows for periodic stop signal checks
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))

			n, err := conn.Read(buf)
			if n > 0 {
				c.uploadBytes.Add(uint64(n))
				dataMsg := &protocol.Message{Type: protocol.MessageTypeStreamData, Payload: protocol.MessageStreamData{StreamID: streamID, Data: buf[:n]}}
				if sendErr := c.sendMessageOn(controlConn, dataMsg); sendErr != nil {
					log.Printf("Stream %d send error: %v", streamID, sendErr)
					c.closeStream(streamID)
					return
				}
			}
			if err != nil {
				// Check if it's a timeout (expected, will retry)
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue // Retry read with stop signal check
				}
				if err == io.EOF {
					complete := stream.MarkLocalEOF()
					c.sendMessageOn(controlConn, &protocol.Message{Type: protocol.MessageTypeStreamClose, Payload: protocol.MessageStreamClose{StreamID: streamID, Reason: "local read closed", HalfClose: true}})
					if complete {
						c.closeStream(streamID)
					}
					return
				} else {
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
				c.sendMessageOn(controlConn, msg)
				c.closeStream(streamID)
				return
			}
		}
	}()
}

// handleIncomingStreamData handles data received from server for a stream
func (c *Client) handleIncomingStreamData(payload protocol.MessageStreamData) {
	c.downloadBytes.Add(uint64(len(payload.Data)))
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

	if !stream.Enqueue(payload.Data) {
		log.Printf("Stream %d inbound queue is full", payload.StreamID)
		c.sendMessage(&protocol.Message{Type: protocol.MessageTypeStreamClose, Payload: protocol.MessageStreamClose{StreamID: payload.StreamID, Reason: "client stream queue full"}})
		c.closeStream(payload.StreamID)
	}
}

// handleStreamClose handles server stream close
func (c *Client) handleStreamClose(payload protocol.MessageStreamClose) {
	if payload.HalfClose {
		c.streamsMu.RLock()
		stream := c.streams[payload.StreamID]
		c.streamsMu.RUnlock()
		if stream != nil && !stream.EnqueueHalfClose() {
			c.closeStream(payload.StreamID)
		}
		return
	}
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
	return c.sendMessageOn(conn, msg)
}

func (c *Client) sendMessageOn(conn net.Conn, msg *protocol.Message) error {
	c.connMu.Lock()
	current := c.connected && c.conn == conn
	c.connMu.Unlock()
	if !current {
		return fmt.Errorf("connection generation is no longer active")
	}

	data, err := protocol.Encode(msg)
	if err != nil {
		return err
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := conn.SetWriteDeadline(time.Now().Add(WriteTimeoutDuration)); err != nil {
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
