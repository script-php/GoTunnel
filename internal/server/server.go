package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/yoyo/gotunnel/internal/auth"
	"github.com/yoyo/gotunnel/internal/config"
	"github.com/yoyo/gotunnel/internal/systemmetrics"
)

// Resource limits to prevent DoS attacks
const (
	MaxStreamsPerClient          = 100              // Maximum concurrent streams per client
	MaxBufferedMessagesPerStream = 50               // Maximum pending messages in send buffer
	MaxMessageSize               = 10 * 1024 * 1024 // 10MB max message size
	StreamSendBufferSize         = 32 * 1024        // 32KB per stream send buffer
	AuthenticationTimeout        = 10 * time.Second
	MaxPendingAuthentications    = 64
)

// Server handles the tunnel server logic
type Server struct {
	cfgMgr        *config.Manager
	authenticator *auth.Authenticator
	logger        LoggerInterface
	webServer     *WebServer

	// Clients
	clients   map[string]*ClientConnection
	clientsMu sync.RWMutex

	// Port to machine mapping
	portMap   map[int]string // remote port -> machine ID
	portMapMu sync.RWMutex

	// Connection tracking
	connCount   int32 // Total active connections
	connCountMu sync.Mutex
	pendingAuth chan struct{}
	clientWG    sync.WaitGroup

	// Listener for client connections
	listener net.Listener

	// Listener for incoming tunnel connections
	tunnelListeners   map[int]net.Listener
	tunnelListenersMu sync.Mutex

	stopCh        chan struct{}
	doneCh        chan struct{}
	shuttingDown  bool
	shutdownMu    sync.Mutex
	stopOnce      sync.Once
	stopErr       error
	startedAt     time.Time
	tlsConfig     *tls.Config
	metrics       systemmetrics.Collector
	uploadBytes   atomic.Uint64
	downloadBytes atomic.Uint64
	trafficMu     sync.Mutex
	lastTrafficAt time.Time
	lastUpload    uint64
	lastDownload  uint64
}

// LoggerInterface defines logging methods
type LoggerInterface interface {
	Add(level, message string)
}

// NewServer creates a new server instance
func NewServer(cfgMgr *config.Manager) *Server {
	serverCfg := cfgMgr.GetServerConfig()
	return &Server{
		cfgMgr:          cfgMgr,
		authenticator:   auth.NewAuthenticator(serverCfg.ClientPassword),
		clients:         make(map[string]*ClientConnection),
		portMap:         make(map[int]string),
		tunnelListeners: make(map[int]net.Listener),
		pendingAuth:     make(chan struct{}, MaxPendingAuthentications),
		stopCh:          make(chan struct{}),
		doneCh:          make(chan struct{}),
		startedAt:       time.Now(),
		lastTrafficAt:   time.Now(),
	}
}

func (s *Server) trafficSnapshot() (upload, download uint64, uploadRate, downloadRate float64) {
	upload = s.uploadBytes.Load()
	download = s.downloadBytes.Load()
	now := time.Now()
	s.trafficMu.Lock()
	elapsed := now.Sub(s.lastTrafficAt).Seconds()
	if elapsed > 0 {
		uploadRate = float64(upload-s.lastUpload) / elapsed
		downloadRate = float64(download-s.lastDownload) / elapsed
	}
	s.lastUpload, s.lastDownload, s.lastTrafficAt = upload, download, now
	s.trafficMu.Unlock()
	return
}

// EnableTLS configures encrypted client transport using a certificate pair.
func (s *Server) EnableTLS(certFile, keyFile string) error {
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("load TLS certificate: %w", err)
	}
	s.tlsConfig = &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS13,
	}
	return nil
}

// SetLogger sets the logger for the server
func (s *Server) SetLogger(logger LoggerInterface) {
	s.logger = logger

	// Log startup messages now that logger is available
	if s.cfgMgr.GetConfigPath() == "" {
		s.logEvent("INFO", "Running with default config (no persistence)")
	}
	serverCfg := s.cfgMgr.GetServerConfig()
	addr := fmt.Sprintf("0.0.0.0:%d", serverCfg.Port)
	s.logEvent("INFO", fmt.Sprintf("Server listening for clients on %s", addr))
}

// logEvent logs a message to both standard logger and web server logger
func (s *Server) logEvent(level, message string) {
	log.Print(message)
	if s.logger != nil {
		s.logger.Add(level, message)
	}
}

// isShuttingDown returns true if the server is shutting down
func (s *Server) isShuttingDown() bool {
	s.shutdownMu.Lock()
	defer s.shutdownMu.Unlock()
	return s.shuttingDown
}

// Start starts the server
func (s *Server) Start() error {
	// Get server config
	serverCfg := s.cfgMgr.GetServerConfig()

	// Start listening for client connections
	addr := fmt.Sprintf("0.0.0.0:%d", serverCfg.Port)
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			return c.Control(func(fd uintptr) {
				syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			})
		},
	}
	listener, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to start server listener: %w", err)
	}
	if s.tlsConfig != nil {
		listener = tls.NewListener(listener, s.tlsConfig)
	}

	s.listener = listener

	// Start accepting client connections
	go s.acceptClients()

	// Load machines from config and start tunnel listeners
	s.loadMachinesFromConfig()

	// Start web panel
	if serverCfg.PanelPort > 0 {
		if err := s.StartWebPanel(serverCfg.PanelHost, serverCfg.PanelPort); err != nil {
			s.logEvent("ERROR", fmt.Sprintf("Failed to start web panel: %v", err))
		}
	}

	return nil
}

// Stop stops the server
func (s *Server) Stop() error {
	s.stopOnce.Do(func() {
		s.shutdownMu.Lock()
		s.shuttingDown = true
		s.shutdownMu.Unlock()

		close(s.stopCh)
		if s.listener != nil {
			s.listener.Close()
		}
		<-s.doneCh

		s.tunnelListenersMu.Lock()
		for _, listener := range s.tunnelListeners {
			listener.Close()
		}
		s.tunnelListenersMu.Unlock()

		if s.webServer != nil {
			if err := s.webServer.Stop(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.stopErr = err
			}
		}

		s.clientsMu.Lock()
		for _, client := range s.clients {
			client.Close()
		}
		s.clientsMu.Unlock()
		s.clientWG.Wait()

		if s.webServer != nil {
			if err := s.webServer.logger.Close(); err != nil && s.stopErr == nil {
				s.stopErr = err
			}
		}
	})
	return s.stopErr
}

// acceptClients accepts incoming client connections
func (s *Server) acceptClients() {
	defer close(s.doneCh)

	for {
		select {
		case <-s.stopCh:
			return
		default:
		}

		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.stopCh:
				return
			default:
				log.Printf("Error accepting client connection: %v", err)
			}
			continue
		}

		select {
		case s.pendingAuth <- struct{}{}:
			s.clientWG.Add(1)
			go func() {
				defer s.clientWG.Done()
				defer func() { <-s.pendingAuth }()
				s.handleClientConnection(conn)
			}()
		default:
			s.logEvent("WARNING", "Rejected client connection: too many pending authentications")
			conn.Close()
		}
	}
}

// handleClientConnection handles a new client connection
func (s *Server) handleClientConnection(conn net.Conn) {
	// Set linger to 0 to close connection immediately without TIME_WAIT
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetLinger(0)
	}
	if err := conn.SetDeadline(time.Now().Add(AuthenticationTimeout)); err != nil {
		conn.Close()
		return
	}

	clientConn := NewClientConnection(conn, s)
	if err := clientConn.Authenticate(); err != nil {
		s.logEvent("WARNING", fmt.Sprintf("Client authentication failed: %v", err))
		conn.Close()
		return
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return
	}

	// Register client
	machineID := clientConn.MachineID
	s.clientsMu.Lock()
	// Check if client already exists and close old one
	if oldConn, exists := s.clients[machineID]; exists {
		s.logEvent("INFO", fmt.Sprintf("Client %s reconnecting, closing old connection", machineID))
		oldConn.Close()
	}
	s.clients[machineID] = clientConn
	s.clientsMu.Unlock()

	s.logEvent("INFO", fmt.Sprintf("Client connected: %s", machineID))

	// Send tunnel configuration
	clientConn.SendTunnelConfig()

	// Handle client messages
	clientConn.Handle()
}

// RemoveClient removes a disconnected client and cleans up resources
func (s *Server) RemoveClient(client *ClientConnection) {
	machineID := client.MachineID
	s.clientsMu.Lock()
	if s.clients[machineID] != client {
		s.clientsMu.Unlock()
		return
	}
	delete(s.clients, machineID)
	s.clientsMu.Unlock()

	s.logEvent("INFO", fmt.Sprintf("Client disconnected: %s", machineID))
}

// GetClientConnection returns a client connection by machine ID
func (s *Server) GetClientConnection(machineID string) *ClientConnection {
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()
	return s.clients[machineID]
}

// GetMachineForPort returns the machine ID for a given remote port
func (s *Server) GetMachineForPort(port int) string {
	s.portMapMu.RLock()
	defer s.portMapMu.RUnlock()
	return s.portMap[port]
}

// RegisterPortMapping registers a port to machine mapping with validation
func (s *Server) RegisterPortMapping(port int, machineID string) {
	// Validate port number
	if port < 1 || port > 65535 {
		log.Printf("Invalid port number: %d", port)
		return
	}

	s.portMapMu.Lock()
	defer s.portMapMu.Unlock()
	s.portMap[port] = machineID
}

// UnregisterPortMapping unregisters a port mapping
func (s *Server) UnregisterPortMapping(port int) {
	s.portMapMu.Lock()
	defer s.portMapMu.Unlock()
	delete(s.portMap, port)
}

// StartTunnelListener starts listening for connections on a specific port
func (s *Server) StartTunnelListener(port int) error {
	// Validate port number
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid port number: %d", port)
	}

	s.tunnelListenersMu.Lock()
	defer s.tunnelListenersMu.Unlock()

	// Check if already listening
	if _, exists := s.tunnelListeners[port]; exists {
		return nil
	}

	addr := fmt.Sprintf("0.0.0.0:%d", port)
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			return c.Control(func(fd uintptr) {
				syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			})
		},
	}
	listener, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", port, err)
	}

	s.tunnelListeners[port] = listener
	s.logEvent("INFO", fmt.Sprintf("Tunnel listener started on port %d", port))

	// Start accepting connections on this port
	go s.acceptTunnelConnections(port, listener)

	return nil
}

// acceptTunnelConnections accepts incoming tunnel connections
func (s *Server) acceptTunnelConnections(port int, listener net.Listener) {
	for {
		select {
		case <-s.stopCh:
			return
		default:
		}

		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || s.isShuttingDown() {
				return
			}
			log.Printf("Error accepting connection on port %d: %v", port, err)
			// Resource exhaustion may recover; avoid a busy loop while retrying.
			timer := time.NewTimer(time.Second)
			select {
			case <-s.stopCh:
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}

		// Handle the incoming connection
		go s.handleTunnelConnection(port, conn)
	}
}

// handleTunnelConnection handles an incoming tunnel connection
func (s *Server) handleTunnelConnection(port int, conn net.Conn) {
	// Set linger to 0 for immediate closure without TIME_WAIT
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetLinger(0)
	}
	machineID := s.GetMachineForPort(port)
	if machineID == "" {
		log.Printf("No machine found for port %d", port)
		conn.Close()
		return
	}

	clientConn := s.GetClientConnection(machineID)
	if clientConn == nil {
		log.Printf("Machine %s is offline", machineID)
		conn.Close()
		return
	}

	// Open a stream on the client
	clientConn.HandleIncomingConnection(port, conn)
}

// loadMachinesFromConfig loads machines from config and starts tunnel listeners
func (s *Server) loadMachinesFromConfig() {
	machines := s.cfgMgr.GetAllMachines()

	for machineID, machineConfig := range machines {
		for _, tunnel := range machineConfig.Tunnels {
			// Register port mapping
			s.RegisterPortMapping(tunnel.Remote, machineID)

			// Start tunnel listener
			if err := s.StartTunnelListener(tunnel.Remote); err != nil {
				log.Printf("Warning: failed to start tunnel listener for port %d: %v", tunnel.Remote, err)
			}
		}
	}
}

// StopTunnelListener stops listening on a specific port
func (s *Server) StopTunnelListener(portStr string) error {
	// Parse port string to int
	var port int
	_, err := fmt.Sscanf(portStr, "%d", &port)
	if err != nil {
		return fmt.Errorf("invalid port number: %w", err)
	}

	s.tunnelListenersMu.Lock()
	defer s.tunnelListenersMu.Unlock()

	listener, exists := s.tunnelListeners[port]
	if !exists {
		return fmt.Errorf("no listener found for port %d", port)
	}

	// Close the listener
	if err := listener.Close(); err != nil {
		return fmt.Errorf("failed to close listener on port %d: %w", port, err)
	}

	// Remove from map
	delete(s.tunnelListeners, port)

	// Unregister port mapping
	s.UnregisterPortMapping(port)

	s.logEvent("INFO", fmt.Sprintf("Tunnel listener stopped on port %d", port))
	return nil
}
