package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
)

// ServerConfig holds server configuration
type ServerConfig struct {
	Port      int    `json:"port"`
	PanelPort int    `json:"panel_port"`
	Password  string `json:"password"`
}

// TunnelConfig represents a single port mapping
type TunnelConfig struct {
	Remote int `json:"remote"`
	Local  int `json:"local"`
}

// MachineConfig holds configuration for a connected machine
type MachineConfig struct {
	Tunnels []TunnelConfig `json:"tunnels"`
}

// Config is the root configuration structure
type Config struct {
	Server   ServerConfig             `json:"server"`
	Machines map[string]MachineConfig `json:"machines"`
}

// Manager handles config loading and saving
type Manager struct {
	path string
	mu   sync.RWMutex
	cfg  *Config
}

// NewManager creates a new config manager
func NewManager(configPath string) *Manager {
	return &Manager{
		path: configPath,
	}
}

// GetConfigPath returns the config file path
func (m *Manager) GetConfigPath() string {
	return m.path
}

// Load loads configuration from file, or creates default if file doesn't exist
func (m *Manager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if file exists
	if _, err := os.Stat(m.path); os.IsNotExist(err) {
		// File doesn't exist, create default config
		m.cfg = m.getDefaultConfig()
		return m.saveLocked()
	}

	// File exists, load it
	data, err := ioutil.ReadFile(m.path)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	m.cfg = cfg
	return nil
}

// Save saves configuration to file (atomic write with temp file)
func (m *Manager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveLocked()
}

// saveLocked saves without acquiring lock (assumes lock is held)
func (m *Manager) saveLocked() error {
	if m.cfg == nil {
		return fmt.Errorf("no configuration to save")
	}

	// If no config path, just keep in memory (no persistence)
	if m.path == "" {
		return nil
	}

	// Marshal to JSON
	data, err := json.MarshalIndent(m.cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Write to temp file first
	tmpPath := m.path + ".tmp"
	if err := ioutil.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp config file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, m.path); err != nil {
		os.Remove(tmpPath) // cleanup temp file
		return fmt.Errorf("failed to save config file: %w", err)
	}

	return nil
}

// GetServerConfig returns the server configuration
func (m *Manager) GetServerConfig() *ServerConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.cfg == nil {
		return nil
	}
	return &m.cfg.Server
}

// SetServerPassword updates the server password
func (m *Manager) SetServerPassword(password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return fmt.Errorf("configuration not initialized")
	}

	m.cfg.Server.Password = password

	// Only save if using a config file
	if m.path != "" {
		return m.saveLocked()
	}
	return nil
}

// GetMachine returns configuration for a specific machine
func (m *Manager) GetMachine(machineID string) *MachineConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.cfg == nil {
		return nil
	}

	if machine, exists := m.cfg.Machines[machineID]; exists {
		return &machine
	}
	return nil
}

// AddOrUpdateMachine adds or updates a machine configuration
func (m *Manager) AddOrUpdateMachine(machineID string, machine *MachineConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return fmt.Errorf("configuration not loaded")
	}

	m.cfg.Machines[machineID] = *machine
	return m.saveLocked()
}

// GetAllMachines returns all machine configurations
func (m *Manager) GetAllMachines() map[string]MachineConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.cfg == nil {
		return map[string]MachineConfig{}
	}

	result := make(map[string]MachineConfig)
	for k, v := range m.cfg.Machines {
		result[k] = v
	}
	return result
}

// AddTunnel adds a tunnel configuration to a machine
func (m *Manager) AddTunnel(machineID string, tunnel TunnelConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return fmt.Errorf("configuration not loaded")
	}

	machine, exists := m.cfg.Machines[machineID]
	if !exists {
		machine = MachineConfig{Tunnels: []TunnelConfig{}}
	}

	machine.Tunnels = append(machine.Tunnels, tunnel)
	m.cfg.Machines[machineID] = machine
	return m.saveLocked()
}

// AddTunnelPorts adds a tunnel configuration to a machine using port values directly
func (m *Manager) AddTunnelPorts(machineID string, remotePort, localPort int) error {
	return m.AddTunnel(machineID, TunnelConfig{Remote: remotePort, Local: localPort})
}

// RemoveTunnel removes a tunnel from a machine
func (m *Manager) RemoveTunnel(machineID string, remotePort int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return fmt.Errorf("configuration not loaded")
	}

	machine, exists := m.cfg.Machines[machineID]
	if !exists {
		return fmt.Errorf("machine not found")
	}

	var newTunnels []TunnelConfig
	for _, t := range machine.Tunnels {
		if t.Remote != remotePort {
			newTunnels = append(newTunnels, t)
		}
	}

	machine.Tunnels = newTunnels
	m.cfg.Machines[machineID] = machine
	return m.saveLocked()
}

// getDefaultConfig returns a default configuration
func (m *Manager) getDefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port:      7727,
			PanelPort: 7726,
			Password:  "",
		},
		Machines: make(map[string]MachineConfig),
	}
}

// InitDefault initializes config with defaults (in-memory, no persistence)
func (m *Manager) InitDefault() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = m.getDefaultConfig()
}

// SetServerPort sets the server port
func (m *Manager) SetServerPort(port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return fmt.Errorf("configuration not initialized")
	}

	m.cfg.Server.Port = port

	// Only save if using a config file
	if m.path != "" {
		return m.saveLocked()
	}
	return nil
}

// SetPanelPort sets the web panel port
func (m *Manager) SetPanelPort(port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return fmt.Errorf("configuration not initialized")
	}

	m.cfg.Server.PanelPort = port

	// Only save if using a config file
	if m.path != "" {
		return m.saveLocked()
	}
	return nil
}
