package server

import (
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yoyo/gotunnel/internal/config"
	"github.com/yoyo/gotunnel/internal/protocol"
	"github.com/yoyo/gotunnel/internal/tunnel"
)

// Constants for stream management
const (
	ReadTimeoutDuration   = 20 * time.Second
	WriteTimeoutDuration  = 20 * time.Second
	StreamTimeoutDuration = 60 * time.Second
	KeepaliveInterval     = 10 * time.Second
	PongTimeoutThreshold  = 3 // Allow 3 missed PONGs before closing
)

// ClientConnection represents a connected client
type ClientConnection struct {
	conn      net.Conn
	server    *Server
	MachineID string

	// Streams
	streams      map[uint32]*tunnel.Stream
	streamsMu    sync.RWMutex
	nextStreamID uint32

	// Keepalive tracking
	lastPongTime int64 // Timestamp of last PONG received
	missedPongs  int32 // Count of consecutive missed PONGs
	pongMu       sync.Mutex

	closed   bool
	closedMu sync.Mutex
}

// NewClientConnection creates a new client connection handler
func NewClientConnection(conn net.Conn, server *Server) *ClientConnection {
	return &ClientConnection{
		conn:         conn,
		server:       server,
		streams:      make(map[uint32]*tunnel.Stream),
		lastPongTime: time.Now().Unix(),
	}
}

// Authenticate authenticates the client
func (cc *ClientConnection) Authenticate() error {
	// Read authentication message
	msg, err := cc.readMessage()
	if err != nil {
		return fmt.Errorf("failed to read auth message: %w", err)
	}

	if msg.Type != protocol.MessageTypeAuth {
		return fmt.Errorf("expected AUTH message, got type %d", msg.Type)
	}

	authMsg, ok := msg.Payload.(protocol.MessageAuth)
	if !ok {
		return fmt.Errorf("invalid auth message payload")
	}

	// Verify password
	if err := cc.server.authenticator.Authenticate(authMsg.Password); err != nil {
		// Send auth failure response
		response := &protocol.Message{
			Type: protocol.MessageTypeAuthResponse,
			Payload: protocol.MessageAuthResponse{
				Success: false,
				Error:   "Invalid password",
			},
		}
		cc.sendMessage(response)
		return err
	}

	cc.MachineID = authMsg.MachineID

	// Get machine config from config manager
	machineConfig := cc.server.cfgMgr.GetMachine(cc.MachineID)
	if machineConfig == nil {
		// Create new machine config
		machineConfig = &config.MachineConfig{
			Tunnels: []config.TunnelConfig{},
		}
	}

	// Send auth success response with tunnels
	tunnels := make([]protocol.TunnelMap, len(machineConfig.Tunnels))
	for i, t := range machineConfig.Tunnels {
		tunnels[i] = protocol.TunnelMap{
			Remote: t.Remote,
			Local:  t.Local,
		}
	}

	response := &protocol.Message{
		Type: protocol.MessageTypeAuthResponse,
		Payload: protocol.MessageAuthResponse{
			Success: true,
			Tunnels: tunnels,
		},
	}
	return cc.sendMessage(response)
}

// SendTunnelConfig sends tunnel configuration to the client
func (cc *ClientConnection) SendTunnelConfig() error {
	machineConfig := cc.server.cfgMgr.GetMachine(cc.MachineID)
	if machineConfig == nil {
		return nil
	}

	tunnels := make([]protocol.TunnelMap, len(machineConfig.Tunnels))
	for i, t := range machineConfig.Tunnels {
		tunnels[i] = protocol.TunnelMap{
			Remote: t.Remote,
			Local:  t.Local,
		}
		// Start listener first before registering port mapping
		if err := cc.server.StartTunnelListener(t.Remote); err != nil {
			log.Printf("Warning: failed to start listener for port %d: %v", t.Remote, err)
			continue // Skip this tunnel if listener fails
		}
		// Register port mapping only if listener started successfully
		cc.server.RegisterPortMapping(t.Remote, cc.MachineID)
	}

	msg := &protocol.Message{
		Type: protocol.MessageTypeTunnelConfig,
		Payload: protocol.MessageTunnelConfig{
			Tunnels: tunnels,
		},
	}
	return cc.sendMessage(msg)
}

// Handle handles incoming messages from the client
func (cc *ClientConnection) Handle() {
	defer cc.server.RemoveClient(cc)

	// Enable TCP keepalive to detect half-open connections
	if tcpConn, ok := cc.conn.(*net.TCPConn); ok {
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(30 * time.Second)
	}

	// Start PING/PONG keepalive ticker
	ticker := time.NewTicker(KeepaliveInterval)
	defer ticker.Stop()

	// Set read/write deadlines for the connection
	cc.conn.SetReadDeadline(time.Now().Add(ReadTimeoutDuration))
	cc.conn.SetWriteDeadline(time.Now().Add(WriteTimeoutDuration))

	// Message reading goroutine
	msgCh := make(chan *protocol.Message)
	errCh := make(chan error)
	go func() {
		defer close(msgCh) // Close channel when reader exits to prevent goroutine leak
		for {
			msg, err := cc.readMessage()
			if err != nil {
				errCh <- err
				return
			}
			msgCh <- msg
		}
	}()

	for {
		select {
		case <-ticker.C:
			// Send periodic PING to detect dead connection
			pingMsg := &protocol.Message{
				Type: protocol.MessageTypePing,
				Payload: protocol.MessagePing{
					Timestamp: time.Now().Unix(),
				},
			}
			if err := cc.sendMessage(pingMsg); err != nil {
				log.Printf("Failed to send PING to client %s: %v", cc.MachineID, err)
				if cc.server.logger != nil {
					cc.server.logger.Add("ERROR", fmt.Sprintf("Failed to send PING to client %s: %v", cc.MachineID, err))
				}
				cc.Close()
				return
			}

			// Check if we've missed too many PONGs
			cc.pongMu.Lock()
			missedPongs := atomic.LoadInt32(&cc.missedPongs)
			cc.pongMu.Unlock()

			if missedPongs >= PongTimeoutThreshold {
				log.Printf("Client %s not responding to PING (missed %d PONGs), closing connection", cc.MachineID, missedPongs)
				if cc.server.logger != nil {
					cc.server.logger.Add("ERROR", fmt.Sprintf("Client %s not responding to PING (missed %d PONGs)", cc.MachineID, missedPongs))
				}
				cc.Close()
				return
			}

			// Increment missed pongs counter
			atomic.AddInt32(&cc.missedPongs, 1)

			// Refresh read/write deadlines after each PING
			cc.conn.SetReadDeadline(time.Now().Add(ReadTimeoutDuration))
			cc.conn.SetWriteDeadline(time.Now().Add(WriteTimeoutDuration))
			log.Printf("Sent PING to client: %s", cc.MachineID)
			if cc.server.logger != nil {
				cc.server.logger.Add("INFO", fmt.Sprintf("Sent PING to client: %s", cc.MachineID))
			}

		case err := <-errCh:
			if err != io.EOF {
				// Suppress "use of closed network connection" errors during shutdown
				if !cc.server.isShuttingDown() || !strings.Contains(err.Error(), "use of closed network connection") {
					log.Printf("Error reading from client %s: %v", cc.MachineID, err)
				}
			}
			// Reader exited, exit main loop
			cc.Close()
			return

		case msg, ok := <-msgCh:
			if !ok {
				// msgCh closed, reader goroutine exited
				cc.Close()
				return
			}

			switch msg.Type {
			case protocol.MessageTypeStreamReady:
				payload, ok := msg.Payload.(protocol.MessageStreamReady)
				if !ok {
					log.Printf("Invalid STREAM_READY payload")
					continue
				}
				// Signal that stream is ready - server can now start forwarding external data
				// FIX #3: Prevent panic if stream already cleaned up by timeout
				// FIX #7: Check if stream was already closed before trying to signal
				cc.streamsMu.RLock()
				stream, exists := cc.streams[payload.StreamID]
				cc.streamsMu.RUnlock()

				if !exists {
					// Stream already removed (likely by timeout) - ignore this message
					log.Printf("Stream %d READY received but stream no longer exists (likely timed out)", payload.StreamID)
					continue
				}

				if stream == nil {
					log.Printf("Stream %d is nil", payload.StreamID)
					continue
				}

				stream.Mu.Lock()
				if stream.Closed {
					stream.Mu.Unlock()
					log.Printf("Stream %d READY received but stream already closed", payload.StreamID)
					continue
				}

				// Signal readiness to forwardExternalToClient goroutine
				// Use select with default to prevent double-close panic
				select {
				case <-stream.ReadyCh:
					// Already signaled (shouldn't happen but be safe)
					stream.Mu.Unlock()
					log.Printf("Stream %d already had READY signal", payload.StreamID)
				default:
					close(stream.ReadyCh)
					stream.Mu.Unlock()
					log.Printf("Stream %d ready", payload.StreamID)
					if cc.server.logger != nil {
						cc.server.logger.Add("INFO", fmt.Sprintf("Stream %d ready", payload.StreamID))
					}
				}

			case protocol.MessageTypeStreamData:
				payload, ok := msg.Payload.(protocol.MessageStreamData)
				if !ok {
					log.Printf("Invalid STREAM_DATA payload")
					continue
				}
				cc.handleStreamData(payload)

			case protocol.MessageTypeStreamClose:
				payload, ok := msg.Payload.(protocol.MessageStreamClose)
				if !ok {
					log.Printf("Invalid STREAM_CLOSE payload")
					continue
				}
				cc.handleStreamClose(payload)

			case protocol.MessageTypePing:
				// Respond with pong
				payload, ok := msg.Payload.(protocol.MessagePing)
				if ok {
					response := &protocol.Message{
						Type: protocol.MessageTypePong,
						Payload: protocol.MessagePong{
							Timestamp: payload.Timestamp,
						},
					}
					cc.sendMessage(response)
				}

			case protocol.MessageTypePong:
				// Client acknowledged our ping, connection is alive
				if payload, ok := msg.Payload.(protocol.MessagePong); ok {
					cc.pongMu.Lock()
					cc.lastPongTime = payload.Timestamp
					atomic.StoreInt32(&cc.missedPongs, 0) // Reset missed pongs counter
					cc.pongMu.Unlock()
					log.Printf("Received PONG from client: %s", cc.MachineID)
					if cc.server.logger != nil {
						cc.server.logger.Add("INFO", fmt.Sprintf("Received PONG from client: %s", cc.MachineID))
					}
				}

			default:
				log.Printf("Unknown message type: %d", msg.Type)
			}
		}
	}

	cc.Close()
}

// HandleIncomingConnection handles an incoming tunnel connection
func (cc *ClientConnection) HandleIncomingConnection(port int, conn net.Conn) {
	// Get the tunnel mapping for this port
	machineConfig := cc.server.cfgMgr.GetMachine(cc.MachineID)
	if machineConfig == nil {
		conn.Close()
		return
	}

	var localPort int
	for _, t := range machineConfig.Tunnels {
		if t.Remote == port {
			localPort = t.Local
			break
		}
	}

	if localPort == 0 {
		log.Printf("No tunnel mapping found for port %d", port)
		conn.Close()
		return
	}

	// Reserve a stream before advertising it. Hold the lifecycle lock through
	// insertion so shutdown cannot finish before a new stream is registered.
	cc.closedMu.Lock()
	if cc.closed {
		cc.closedMu.Unlock()
		conn.Close()
		return
	}
	cc.streamsMu.Lock()
	if len(cc.streams) >= MaxStreamsPerClient {
		cc.streamsMu.Unlock()
		cc.closedMu.Unlock()
		conn.Close()
		return
	}
	streamID := atomic.AddUint32(&cc.nextStreamID, 1)
	for streamID == 0 || cc.streams[streamID] != nil {
		streamID = atomic.AddUint32(&cc.nextStreamID, 1)
	}
	cc.streams[streamID] = tunnel.NewStream(streamID, conn, nil)
	cc.streamsMu.Unlock()
	cc.closedMu.Unlock()

	// Send STREAM_OPEN message to client
	msg := &protocol.Message{
		Type: protocol.MessageTypeStreamOpen,
		Payload: protocol.MessageStreamOpen{
			StreamID:  streamID,
			LocalPort: localPort,
		},
	}

	if err := cc.sendMessage(msg); err != nil {
		log.Printf("Failed to send STREAM_OPEN: %v", err)
		cc.closeStream(streamID)
		return
	}


	log.Printf("Stream %d opened for port %d (local: %d)", streamID, port, localPort)
	if cc.server.logger != nil {
		cc.server.logger.Add("INFO", fmt.Sprintf("Stream %d opened for port %d (local: %d)", streamID, port, localPort))
	}

	// Start forwarding goroutine (will wait for STREAM_READY signal before reading)
	go cc.forwardExternalToClient(streamID)
}

// forwardExternalToClient reads from external connection and sends data via STREAM_DATA to client
func (cc *ClientConnection) forwardExternalToClient(streamID uint32) {
	cc.streamsMu.RLock()
	stream, exists := cc.streams[streamID]
	cc.streamsMu.RUnlock()

	if !exists {
		return
	}

	// Lock stream to safely access Conn1 and verify it's not nil
	if stream == nil {
		return
	}

	// Wait for client to signal STREAM_READY before reading external data
	// This prevents data loss by ensuring client stream is created first
	// Timeout after 30 seconds to prevent orphaned streams
	select {
	case <-stream.ReadyCh:
		// Client is ready, proceed to read
	case <-time.After(30 * time.Second):
		// Client didn't send STREAM_READY in time, close stream to free resources
		log.Printf("Stream %d: client never sent STREAM_READY (timeout 30s), closing", streamID)
		cc.closeStream(streamID)
		return
	}

	stream.Mu.Lock()
	if stream.Conn1 == nil {
		stream.Mu.Unlock()
		return
	}
	stream.Mu.Unlock()

	// Set both read and write deadlines to detect stalled connections
	// Read timeout is 10 minutes (slow clients allowed, but detect hangs)
	// stream.Conn1.SetReadDeadline(time.Now().Add(1 * time.Minute))
	// stream.Conn1.SetWriteDeadline(time.Now().Add(StreamTimeoutDuration))

	buf := make([]byte, 32*1024)
	for {
		// Check if stream is still valid before each read
		stream.Mu.Lock()
		if stream.Closed || stream.Conn1 == nil {
			stream.Mu.Unlock()
			cc.closeStream(streamID)
			return
		}
		stream.Mu.Unlock()

		// Refresh deadlines BEFORE each read to keep timeout active-based (not absolute)
		// This prevents timeout for continuous streams (audio, video) with buffering or silence periods
		// stream.Conn1.SetReadDeadline(time.Now().Add(10 * time.Minute))
		// stream.Conn1.SetWriteDeadline(time.Now().Add(StreamTimeoutDuration))

		n, err := stream.Conn1.Read(buf)
		if err != nil {
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
					Reason:   "external connection closed",
				},
			}
			cc.sendMessage(msg)
			cc.closeStream(streamID)
			return
		}

		if n > 0 {
			dataMsg := &protocol.Message{
				Type: protocol.MessageTypeStreamData,
				Payload: protocol.MessageStreamData{
					StreamID: streamID,
					Data:     buf[:n],
				},
			}
			if err := cc.sendMessage(dataMsg); err != nil {
				log.Printf("Stream %d send error: %v", streamID, err)
				cc.closeStream(streamID)
				return
			}
		}
	}
}

// handleStreamData handles incoming stream data from client
func (cc *ClientConnection) handleStreamData(payload protocol.MessageStreamData) {
	cc.streamsMu.RLock()
	stream, exists := cc.streams[payload.StreamID]
	cc.streamsMu.RUnlock()

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
		cc.closeStream(payload.StreamID)
		return
	}
	conn.SetWriteDeadline(time.Now().Add(StreamTimeoutDuration))
	if _, err := conn.Write(payload.Data); err != nil {
		log.Printf("Stream %d write error: %v", payload.StreamID, err)
		cc.closeStream(payload.StreamID)
	}

}

// handleStreamClose handles stream close from client
func (cc *ClientConnection) handleStreamClose(payload protocol.MessageStreamClose) {
	cc.closeStream(payload.StreamID)
	log.Printf("Stream %d closed: %s", payload.StreamID, payload.Reason)
	if cc.server.logger != nil {
		cc.server.logger.Add("INFO", fmt.Sprintf("Stream %d closed: %s", payload.StreamID, payload.Reason))
	}
}

// closeStream closes a stream and cleans up resources
func (cc *ClientConnection) closeStream(streamID uint32) {
	cc.streamsMu.Lock()
	stream, exists := cc.streams[streamID]
	if exists {
		delete(cc.streams, streamID)
	}
	cc.streamsMu.Unlock()

	if exists && stream != nil {
		stream.Close()
	}
}

// readMessage reads a message from the connection
func (cc *ClientConnection) readMessage() (*protocol.Message, error) {
	return protocol.Read(cc.conn)
}

// sendMessage sends a message to the client
func (cc *ClientConnection) sendMessage(msg *protocol.Message) error {
	cc.closedMu.Lock()
	if cc.closed {
		cc.closedMu.Unlock()
		return fmt.Errorf("connection closed")
	}
	cc.closedMu.Unlock()

	data, err := protocol.Encode(msg)
	if err != nil {
		return err
	}

	_, err = cc.conn.Write(data)
	return err
}

// Close closes the client connection and cleans up all resources
func (cc *ClientConnection) Close() error {
	cc.closedMu.Lock()
	if cc.closed {
		cc.closedMu.Unlock()
		return nil
	}
	cc.closed = true
	cc.closedMu.Unlock()

	// Close all streams
	cc.streamsMu.Lock()
	for _, stream := range cc.streams {
		stream.Close()
	}
	cc.streamsMu.Unlock()

	// Close connection
	return cc.conn.Close()
}
