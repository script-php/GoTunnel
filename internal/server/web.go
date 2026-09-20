package server

import (
	"crypto/rand"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Logging constants
const (
	LogBatchSize     = 10           // Batch writes after this many entries
	LogFlushInterval = 1 * time.Second // Or after this duration
)

//go:embed web/*
var webFS embed.FS

// Session represents an authenticated user session
type Session struct {
	Token     string
	ExpiresAt time.Time
}

// RateLimitEntry tracks failed login attempts
type RateLimitEntry struct {
	Attempts    int
	LastAttempt time.Time
	LockedUntil time.Time
}

// LogEntry represents a single log message
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Message   string `json:"message"`
}

// Logger manages in-memory logs and file persistence with buffered writes
type Logger struct {
	mu       sync.RWMutex
	entries  []LogEntry
	maxSize  int
	filePath string
	
	// Buffered file writing
	logCh   chan LogEntry
	doneCh  chan struct{}
	fileHdl *os.File
}

// NewLogger creates a new logger with buffered file writes
func NewLogger(logPath string, maxSize int) *Logger {
	l := &Logger{
		entries:  make([]LogEntry, 0, maxSize),
		maxSize:  maxSize,
		filePath: logPath,
		logCh:    make(chan LogEntry, LogBatchSize*2),
		doneCh:   make(chan struct{}),
	}
	
	// Start background writer goroutine if file path is set
	if logPath != "" {
		go l.fileWriterLoop()
	}
	
	return l
}

// fileWriterLoop batches log writes to file
func (l *Logger) fileWriterLoop() {
	defer close(l.doneCh)
	
	// Ensure directory exists
	if l.filePath != "" {
		dir := filepath.Dir(l.filePath)
		os.MkdirAll(dir, 0755)
	}
	
	// Open file once for appending
	f, err := os.OpenFile(l.filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Warning: failed to open log file: %v", err)
		f = nil
	}
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	
	ticker := time.NewTicker(LogFlushInterval)
	defer ticker.Stop()
	
	var batch []LogEntry
	
	for {
		select {
		case entry, ok := <-l.logCh:
			if !ok {
				// Channel closed, flush remaining batch
				if len(batch) > 0 && f != nil {
					l.writeBatchToFile(f, batch)
				}
				return
			}
			
			batch = append(batch, entry)
			
			// Flush if batch is full
			if len(batch) >= LogBatchSize {
				if f != nil {
					l.writeBatchToFile(f, batch)
				}
				batch = batch[:0]
			}
			
		case <-ticker.C:
			// Flush on timeout
			if len(batch) > 0 && f != nil {
				l.writeBatchToFile(f, batch)
				batch = batch[:0]
			}
		}
	}
}

// writeBatchToFile writes a batch of entries to file
func (l *Logger) writeBatchToFile(f *os.File, entries []LogEntry) {
	for _, entry := range entries {
		logLine := fmt.Sprintf("[%s] %s: %s\n", entry.Timestamp, entry.Level, entry.Message)
		f.WriteString(logLine)
	}
	f.Sync() // Ensure data is written to disk
}

// Add adds a log entry
func (l *Logger) Add(level, message string) {
	entry := LogEntry{
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
		Level:     level,
		Message:   message,
	}
	
	// Add to in-memory buffer
	l.mu.Lock()
	l.entries = append(l.entries, entry)
	if len(l.entries) > l.maxSize {
		l.entries = l.entries[1:]
	}
	l.mu.Unlock()
	
	// Send to file writer if channel exists (non-blocking)
	if l.filePath != "" {
		select {
		case l.logCh <- entry:
		default:
			// Channel full, drop message to prevent blocking
		}
	}
}

// Close closes the logger and flushes remaining logs
func (l *Logger) Close() error {
	if l.filePath != "" {
		close(l.logCh)
		<-l.doneCh
	}
	return nil
}

// Get returns recent log entries
func (l *Logger) Get(limit int) []LogEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if limit <= 0 || limit > len(l.entries) {
		limit = len(l.entries)
	}

	result := make([]LogEntry, limit)
	copy(result, l.entries[len(l.entries)-limit:])

	// Reverse to show newest first
	for i := len(result)/2 - 1; i >= 0; i-- {
		opp := len(result) - 1 - i
		result[i], result[opp] = result[opp], result[i]
	}
	return result
}

// GetFiltered returns log entries filtered by level
func (l *Logger) GetFiltered(level string, limit int) []LogEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var filtered []LogEntry
	for i := len(l.entries) - 1; i >= 0 && len(filtered) < limit; i-- {
		if level == "" || l.entries[i].Level == level {
			filtered = append(filtered, l.entries[i])
		}
	}
	return filtered
}

// Clear removes all log entries
func (l *Logger) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = make([]LogEntry, 0, l.maxSize)
}

// Count returns the number of entries
func (l *Logger) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.entries)
}

// WebServer handles the HTTP web panel for the server
type WebServer struct {
	server       *http.Server
	port         string
	password     string
	sessions     map[string]*Session
	sessionsMu   sync.RWMutex
	rateLimits   map[string]*RateLimitEntry
	rateLimitsMu sync.RWMutex
	logger       *Logger
}

// NewWebServer creates a new web server
func NewWebServer(port int, password string) *WebServer {
	return &WebServer{
		port:       fmt.Sprintf("0.0.0.0:%d", port),
		password:   password,
		sessions:   make(map[string]*Session),
		rateLimits: make(map[string]*RateLimitEntry),
		logger:     NewLogger("logs/gotunnel.log", 1000),
	}
}

// Start starts the web server
func (ws *WebServer) Start(s *Server) error {
	mux := http.NewServeMux()

	// Middleware for API endpoints
	apiMiddleware := func(handler func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// Allow login without session
			if strings.HasSuffix(r.URL.Path, "/login") || strings.HasSuffix(r.URL.Path, "/session") {
				handler(w, r)
				return
			}

			// Check session for other endpoints
			if !ws.validateSession(r) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
				return
			}

			handler(w, r)
		}
	}

	// Serve embedded web files
	webDist, err := fs.Sub(webFS, "web")
	if err != nil {
		return fmt.Errorf("failed to create web fs: %w", err)
	}

	// Static files that don't require auth
	mux.HandleFunc("/login.js", func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(webDist, "login.js")
		if err != nil {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/javascript")
		w.Write(data)
	})

	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		// app.js requires auth - check session
		if !ws.validateSession(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		data, err := fs.ReadFile(webDist, "app.js")
		if err != nil {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/javascript")
		w.Write(data)
	})

	mux.HandleFunc("/style.css", func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(webDist, "style.css")
		if err != nil {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/css")
		w.Write(data)
	})

	// Login page (no auth required)
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(webDist, "login.html")
		if err != nil {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})

	// Dashboard home page (auth required)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		// Check if authenticated
		if !ws.validateSession(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		data, err := fs.ReadFile(webDist, "index.html")
		if err != nil {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})

	// Auth endpoints
	mux.HandleFunc("/api/login", apiMiddleware(func(w http.ResponseWriter, r *http.Request) {
		ws.handleLogin(w, r)
	}))

	mux.HandleFunc("/api/logout", apiMiddleware(func(w http.ResponseWriter, r *http.Request) {
		ws.handleLogout(w, r)
	}))

	mux.HandleFunc("/api/session", apiMiddleware(func(w http.ResponseWriter, r *http.Request) {
		ws.handleSession(w, r)
	}))

	// Logs endpoints
	mux.HandleFunc("/api/logs", apiMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			ws.handleGetLogs(w, r)
		} else if r.Method == http.MethodDelete {
			ws.handleClearLogs(w, r)
		} else {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	}))

	// API endpoints
	mux.HandleFunc("/api/status", apiMiddleware(func(w http.ResponseWriter, r *http.Request) {
		ws.handleStatus(w, r, s)
	}))

	mux.HandleFunc("/api/client/", apiMiddleware(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path[len("/api/client/"):]
		parts := splitPath(path)

		if len(parts) == 1 {
			// GET /api/client/{clientId}
			if r.Method == http.MethodGet {
				ws.handleGetClient(w, r, s, parts[0])
			}
		} else if len(parts) == 2 && parts[1] == "tunnel" {
			// POST /api/client/{clientId}/tunnel (add tunnel)
			if r.Method == http.MethodPost {
				ws.handleAddTunnel(w, r, s, parts[0])
			}
		} else if len(parts) == 3 && parts[1] == "tunnel" {
			// DELETE /api/client/{clientId}/tunnel/{remotePort}
			if r.Method == http.MethodDelete {
				ws.handleRemoveTunnel(w, r, s, parts[0], parts[2])
			}
		} else {
			http.Error(w, "Not Found", http.StatusNotFound)
		}
	}))

	ws.server = &http.Server{
		Addr:    ws.port,
		Handler: mux,
	}

	go func() {
		msg := fmt.Sprintf("Web panel listening on http://%s", ws.port)
		log.Printf(msg)
		ws.logger.Add("INFO", msg)
		if err := ws.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Web server error: %v", err)
		}
	}()

	return nil
}

// Stop stops the web server
func (ws *WebServer) Stop() error {
	if ws.server != nil {
		return ws.server.Close()
	}
	return nil
}

// fileServerWithAuth wraps file server with auth redirect
func (ws *WebServer) fileServerWithAuth(fs http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow static assets without auth (CSS, JS, etc.)
		if r.URL.Path == "/login.js" || r.URL.Path == "/app.js" || r.URL.Path == "/style.css" {
			fs.ServeHTTP(w, r)
			return
		}

		// For other files, require auth
		if !ws.validateSession(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		fs.ServeHTTP(w, r)
	})
}

// generateToken creates a random session token
func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// handleLogin authenticates user and creates session
func (ws *WebServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "Method not allowed"})
		return
	}

	// Rate limiting
	clientIP := r.RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		clientIP = strings.Split(xff, ",")[0]
	}

	ws.rateLimitsMu.Lock()
	rateLimit := ws.rateLimits[clientIP]
	if rateLimit == nil {
		rateLimit = &RateLimitEntry{}
		ws.rateLimits[clientIP] = rateLimit
	}

	// Check if locked out
	if time.Now().Before(rateLimit.LockedUntil) {
		ws.rateLimitsMu.Unlock()
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]string{"error": "Too many attempts. Try again later."})
		return
	}

	// Reset attempts if enough time has passed
	if time.Since(rateLimit.LastAttempt) > 15*time.Minute {
		rateLimit.Attempts = 0
	}
	ws.rateLimitsMu.Unlock()

	var req struct {
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request"})
		return
	}

	// Check password
	if req.Password != ws.password {
		ws.rateLimitsMu.Lock()
		rateLimit := ws.rateLimits[clientIP]
		rateLimit.Attempts++
		rateLimit.LastAttempt = time.Now()

		// Lock out after 5 failed attempts for 15 minutes
		if rateLimit.Attempts >= 5 {
			rateLimit.LockedUntil = time.Now().Add(15 * time.Minute)
			ws.rateLimitsMu.Unlock()
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{"error": "Too many failed attempts. Locked for 15 minutes."})
			return
		}
		ws.rateLimitsMu.Unlock()

		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid password"})
		return
	}

	// Reset rate limit on successful login
	ws.rateLimitsMu.Lock()
	delete(ws.rateLimits, clientIP)
	ws.rateLimitsMu.Unlock()

	// Create session
	token := generateToken()
	session := &Session{
		Token:     token,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}

	ws.sessionsMu.Lock()
	ws.sessions[token] = session
	ws.sessionsMu.Unlock()

	// Set cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "session_token",
		Value:    token,
		Path:     "/",
		Expires:  session.ExpiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"token": token})
}

// handleLogout destroys session
func (ws *WebServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "Method not allowed"})
		return
	}

	// Get token from cookie
	cookie, err := r.Cookie("session_token")
	if err == nil {
		ws.sessionsMu.Lock()
		delete(ws.sessions, cookie.Value)
		ws.sessionsMu.Unlock()
	}

	// Clear cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "session_token",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleSession checks if session is valid
func (ws *WebServer) handleSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if ws.validateSession(r) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "authenticated"})
	} else {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
	}
}

// validateSession checks if request has valid session
func (ws *WebServer) validateSession(r *http.Request) bool {
	cookie, err := r.Cookie("session_token")
	if err != nil {
		return false
	}

	ws.sessionsMu.RLock()
	session, exists := ws.sessions[cookie.Value]
	ws.sessionsMu.RUnlock()

	if !exists {
		return false
	}

	if time.Now().After(session.ExpiresAt) {
		ws.sessionsMu.Lock()
		delete(ws.sessions, cookie.Value)
		ws.sessionsMu.Unlock()
		return false
	}

	return true
}

// handleGetLogs returns recent log entries
func (ws *WebServer) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	limit := 100
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		fmt.Sscanf(limitStr, "%d", &limit)
	}

	level := r.URL.Query().Get("level")
	var logs []LogEntry
	if level != "" {
		logs = ws.logger.GetFiltered(level, limit)
	} else {
		logs = ws.logger.Get(limit)
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"logs":  logs,
		"total": len(logs),
	})
}

// handleClearLogs clears all log entries
func (ws *WebServer) handleClearLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	ws.logger.Clear()

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "Logs cleared"})
}

// handleStatus returns the current server status as JSON
func (ws *WebServer) handleStatus(w http.ResponseWriter, r *http.Request, s *Server) {
	w.Header().Set("Content-Type", "application/json")

	// Collect client information
	s.clientsMu.RLock()
	clients := make(map[string]interface{})
	for machineID, clientConn := range s.clients {
		// Get tunnels for this client
		machineConfig := s.cfgMgr.GetMachine(machineID)
		tunnels := make([]interface{}, 0)
		if machineConfig != nil {
			for _, t := range machineConfig.Tunnels {
				tunnels = append(tunnels, map[string]interface{}{
					"remote": t.Remote,
					"local":  t.Local,
				})
			}
		}

		// Get active streams
		clientConn.streamsMu.RLock()
		streamCount := len(clientConn.streams)
		clientConn.streamsMu.RUnlock()

		clients[machineID] = map[string]interface{}{
			"tunnels": tunnels,
			"streams": make([]interface{}, streamCount),
		}
	}
	s.clientsMu.RUnlock()

	// Collect all tunnel mappings - only for CONNECTED clients
	s.portMapMu.RLock()
	allTunnels := make([]interface{}, 0)
	for port, machineID := range s.portMap {
		// Only include tunnels for clients that are currently connected
		if _, isConnected := clients[machineID]; !isConnected {
			continue
		}

		// Get local port for this mapping
		machineConfig := s.cfgMgr.GetMachine(machineID)
		if machineConfig != nil {
			for _, t := range machineConfig.Tunnels {
				if t.Remote == port {
					allTunnels = append(allTunnels, map[string]interface{}{
						"remote": port,
						"local":  t.Local,
						"client": machineID,
					})
					break
				}
			}
		}
	}
	s.portMapMu.RUnlock()

	// Sort tunnels by remote port for consistent ordering
	sort.Slice(allTunnels, func(i, j int) bool {
		return allTunnels[i].(map[string]interface{})["remote"].(int) <
			allTunnels[j].(map[string]interface{})["remote"].(int)
	})

	// Build response
	status := map[string]interface{}{
		"clients": clients,
		"tunnels": allTunnels,
	}

	json.NewEncoder(w).Encode(status)
}

// StartWebPanel starts the web panel for the server
func (s *Server) StartWebPanel(panelPort int) error {
	serverCfg := s.cfgMgr.GetServerConfig()
	ws := NewWebServer(panelPort, serverCfg.Password)

	// Set the server's logger to the web server's logger
	s.SetLogger(ws.logger)

	return ws.Start(s)
}

// handleGetClient returns details for a specific client
func (ws *WebServer) handleGetClient(w http.ResponseWriter, r *http.Request, s *Server, clientId string) {
	w.Header().Set("Content-Type", "application/json")

	s.clientsMu.RLock()
	_, exists := s.clients[clientId]
	s.clientsMu.RUnlock()

	if !exists {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "Client not found"})
		return
	}

	machineConfig := s.cfgMgr.GetMachine(clientId)
	var tunnels []interface{}
	if machineConfig != nil {
		for _, t := range machineConfig.Tunnels {
			tunnels = append(tunnels, map[string]interface{}{
				"remote": t.Remote,
				"local":  t.Local,
			})
		}
	}

	response := map[string]interface{}{
		"client":  clientId,
		"tunnels": tunnels,
	}

	json.NewEncoder(w).Encode(response)
}

// handleAddTunnel adds a new tunnel for a client
func (ws *WebServer) handleAddTunnel(w http.ResponseWriter, r *http.Request, s *Server, clientId string) {
	w.Header().Set("Content-Type", "application/json")

	s.clientsMu.RLock()
	_, exists := s.clients[clientId]
	s.clientsMu.RUnlock()

	if !exists {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "Client not found"})
		return
	}

	var req struct {
		Remote int `json:"remote"`
		Local  int `json:"local"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request"})
		return
	}

	if req.Remote < 1 || req.Remote > 65535 || req.Local < 1 || req.Local > 65535 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid port range"})
		return
	}

	// Add tunnel to config
	if err := s.cfgMgr.AddTunnelPorts(clientId, req.Remote, req.Local); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Register port mapping and start listener
	s.RegisterPortMapping(req.Remote, clientId)
	if err := s.StartTunnelListener(req.Remote); err != nil {
		log.Printf("Warning: failed to start listener for port %d: %v", req.Remote, err)
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleRemoveTunnel removes a tunnel from a client
func (ws *WebServer) handleRemoveTunnel(w http.ResponseWriter, r *http.Request, s *Server, clientId string, remotePort string) {
	w.Header().Set("Content-Type", "application/json")

	s.clientsMu.RLock()
	_, exists := s.clients[clientId]
	s.clientsMu.RUnlock()

	if !exists {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "Client not found"})
		return
	}

	// Parse remote port
	var port int
	_, err := fmt.Sscanf(remotePort, "%d", &port)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid port number"})
		return
	}

	// Remove tunnel from config
	if err := s.cfgMgr.RemoveTunnel(clientId, port); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Stop tunnel listener
	s.StopTunnelListener(remotePort)

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// Helper function to split URL path
func splitPath(path string) []string {
	var parts []string
	var current string
	for _, ch := range path {
		if ch == '/' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(ch)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}
