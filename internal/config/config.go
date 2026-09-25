package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ServerConfig holds server configuration
type ServerConfig struct {
	Port           int    `json:"port"`
	PanelPort      int    `json:"panel_port"`
	PanelHost      string `json:"panel_host,omitempty"`
	Password       string `json:"password,omitempty"` // Legacy migration only.
	ClientPassword string `json:"client_password,omitempty"`
	AdminPassword  string `json:"admin_password,omitempty"`
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
	data, err := os.ReadFile(m.path)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := m.getDefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	if err := validate(cfg); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	if cfg.Machines == nil {
		cfg.Machines = make(map[string]MachineConfig)
	}
	if cfg.Server.ClientPassword == "" {
		cfg.Server.ClientPassword = cfg.Server.Password
	}
	if cfg.Server.AdminPassword == "" {
		cfg.Server.AdminPassword = cfg.Server.Password
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

	// Create the temporary file in the same directory so rename is atomic.
	tmp, err := os.CreateTemp(dir, ".gotunnel-config-*")
	if err != nil {
		return fmt.Errorf("failed to create temp config file: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		tmp.Close()
		if !committed {
			os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return fmt.Errorf("failed to protect temp config file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("failed to write temp config file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("failed to sync temp config file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close temp config file: %w", err)
	}
	if err := os.Rename(tmpPath, m.path); err != nil {
		return fmt.Errorf("failed to save config file: %w", err)
	}
	committed = true

	// Sync the directory entry so the rename survives an abrupt power loss.
	dirHandle, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("failed to open config directory for sync: %w", err)
	}
	defer dirHandle.Close()
	if err := dirHandle.Sync(); err != nil {
		return fmt.Errorf("failed to sync config directory: %w", err)
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
	server := m.cfg.Server
	return &server
}

// SetServerPassword updates the server password
func (m *Manager) SetServerPassword(password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return fmt.Errorf("configuration not initialized")
	}

	m.cfg.Server.Password = password
	m.cfg.Server.ClientPassword = password
	m.cfg.Server.AdminPassword = password

	// Only save if using a config file
	if m.path != "" {
		return m.saveLocked()
	}
	return nil
}

func (m *Manager) SetClientPassword(password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg == nil {
		return fmt.Errorf("configuration not initialized")
	}
	previous := m.cfg.Server.ClientPassword
	m.cfg.Server.ClientPassword = password
	if err := m.saveLocked(); err != nil {
		m.cfg.Server.ClientPassword = previous
		return err
	}
	return nil
}

func (m *Manager) SetAdminPassword(password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg == nil {
		return fmt.Errorf("configuration not initialized")
	}
	previous := m.cfg.Server.AdminPassword
	m.cfg.Server.AdminPassword = password
	if err := m.saveLocked(); err != nil {
		m.cfg.Server.AdminPassword = previous
		return err
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
		machine = cloneMachine(machine)
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

	if machine == nil {
		return fmt.Errorf("machine configuration is nil")
	}
	if machineID == "" {
		return fmt.Errorf("machine ID cannot be empty")
	}
	owners := m.remotePortOwners(machineID)
	if err := validateMachine(machineID, *machine, m.cfg.Server, owners); err != nil {
		return err
	}
	previous, existed := m.cfg.Machines[machineID]
	m.cfg.Machines[machineID] = cloneMachine(*machine)
	if err := m.saveLocked(); err != nil {
		if existed {
			m.cfg.Machines[machineID] = previous
		} else {
			delete(m.cfg.Machines, machineID)
		}
		return err
	}
	return nil
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
		result[k] = cloneMachine(v)
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

	owners := m.remotePortOwners("")
	if err := validateMachine(machineID, MachineConfig{Tunnels: []TunnelConfig{tunnel}}, m.cfg.Server, owners); err != nil {
		return err
	}
	previous := cloneMachine(machine)
	machine.Tunnels = append(machine.Tunnels, tunnel)
	m.cfg.Machines[machineID] = machine
	if err := m.saveLocked(); err != nil {
		if exists {
			m.cfg.Machines[machineID] = previous
		} else {
			delete(m.cfg.Machines, machineID)
		}
		return err
	}
	return nil
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

	previous := cloneMachine(machine)
	newTunnels := make([]TunnelConfig, 0, len(machine.Tunnels))
	found := false
	for _, t := range machine.Tunnels {
		if t.Remote != remotePort {
			newTunnels = append(newTunnels, t)
		} else {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("tunnel on remote port %d not found", remotePort)
	}

	machine.Tunnels = newTunnels
	m.cfg.Machines[machineID] = machine
	if err := m.saveLocked(); err != nil {
		m.cfg.Machines[machineID] = previous
		return err
	}
	return nil
}

// getDefaultConfig returns a default configuration
func (m *Manager) getDefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port:      7727,
			PanelPort: 7726,
			PanelHost: "0.0.0.0",
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

// SetPanelHost sets the address used by the management panel listener.
func (m *Manager) SetPanelHost(host string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg == nil {
		return fmt.Errorf("configuration not initialized")
	}
	if host == "" {
		return fmt.Errorf("panel host cannot be empty")
	}
	previous := m.cfg.Server.PanelHost
	m.cfg.Server.PanelHost = host
	if err := m.saveLocked(); err != nil {
		m.cfg.Server.PanelHost = previous
		return err
	}
	return nil
}

// cloneMachine prevents callers from mutating configuration outside the manager lock.
func cloneMachine(machine MachineConfig) MachineConfig {
	if machine.Tunnels != nil {
		machine.Tunnels = append([]TunnelConfig{}, machine.Tunnels...)
	}
	return machine
}

func validate(cfg *Config) error {
	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return fmt.Errorf("invalid server port: %d", cfg.Server.Port)
	}
	if cfg.Server.PanelPort < 0 || cfg.Server.PanelPort > 65535 {
		return fmt.Errorf("invalid panel port: %d", cfg.Server.PanelPort)
	}
	if cfg.Server.PanelPort == cfg.Server.Port {
		return fmt.Errorf("server and panel ports must differ")
	}
	if cfg.Server.PanelHost == "" {
		return fmt.Errorf("panel host cannot be empty")
	}
	owners := make(map[int]string)
	for id, machine := range cfg.Machines {
		if err := validateMachine(id, machine, cfg.Server, owners); err != nil {
			return err
		}
	}
	return nil
}

func validateMachine(id string, machine MachineConfig, server ServerConfig, owners map[int]string) error {
	if err := ValidateMachineID(id); err != nil {
		return err
	}
	for _, tunnel := range machine.Tunnels {
		if tunnel.Remote < 1 || tunnel.Remote > 65535 || tunnel.Local < 1 || tunnel.Local > 65535 {
			return fmt.Errorf("invalid tunnel ports for machine %q", id)
		}
		if tunnel.Remote == server.Port || tunnel.Remote == server.PanelPort {
			return fmt.Errorf("tunnel port %d conflicts with a server port", tunnel.Remote)
		}
		if owners != nil {
			if owner, exists := owners[tunnel.Remote]; exists {
				return fmt.Errorf("duplicate remote port %d for machines %q and %q", tunnel.Remote, owner, id)
			}
			owners[tunnel.Remote] = id
		}
	}
	return nil
}

// ValidateMachineID keeps identifiers safe for logs and URL path segments.
func ValidateMachineID(id string) error {
	if id == "" || len(id) > 128 {
		return fmt.Errorf("machine ID must contain 1 to 128 characters")
	}
	for _, char := range id {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return fmt.Errorf("machine ID contains unsupported character %q", char)
	}
	return nil
}

func (m *Manager) remotePortOwners(excludeMachine string) map[int]string {
	owners := make(map[int]string)
	for id, machine := range m.cfg.Machines {
		if id == excludeMachine {
			continue
		}
		for _, tunnel := range machine.Tunnels {
			owners[tunnel.Remote] = id
		}
	}
	return owners
}
