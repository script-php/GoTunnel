package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yoyo/gotunnel/internal/client"
	"github.com/yoyo/gotunnel/internal/config"
	"github.com/yoyo/gotunnel/internal/server"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "server":
		runServer()
	case "client":
		runClient()
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Printf("Unknown command: %s\n", command)
		printUsage()
		os.Exit(1)
	}
}

func isRoot() bool {
	return syscall.Geteuid() == 0
}

func printUsage() {
	fmt.Printf(`GoTunnel %s - Self-hosted reverse TCP tunneling

USAGE:
  gotunnel <command> [options]

COMMANDS:
  server    Run in server mode
  client    Run in client mode
  help      Show this help message

EXAMPLES:
  gotunnel server -password MyPassword
  sudo gotunnel server -password MyPassword -register
  sudo gotunnel server -unregister
  gotunnel client -server example.com:7727 -pass MyPassword -name my-pc
  gotunnel client -help

For detailed help on a command:
  gotunnel server -help
  gotunnel client -help
`, config.Version)
}

func systemdQuote(arg string) string {
	// Percent signs are systemd specifiers even inside quotes.
	return strconv.Quote(strings.ReplaceAll(arg, "%", "%%"))
}

func renderService(serviceName, binaryPath string, args []string) string {
	quoted := make([]string, 0, len(args)+1)
	quoted = append(quoted, systemdQuote(binaryPath))
	for _, arg := range args {
		quoted = append(quoted, systemdQuote(arg))
	}
	return fmt.Sprintf(`[Unit]
Description=GoTunnel %s Service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=10
DynamicUser=yes
StateDirectory=gotunnel
WorkingDirectory=/var/lib/gotunnel
UMask=0077
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=read-only
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`, serviceName, strings.Join(quoted, " "))
}

// registerService creates a systemd service file for the given arguments.
func registerService(serviceName string, args []string) error {
	// Check for root privileges
	if !isRoot() {
		return fmt.Errorf("service registration requires root privileges. Use: sudo gotunnel %s -register", serviceName)
	}

	// Get absolute path to binary
	binaryPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Create system-wide systemd directory if it doesn't exist
	serviceDir := "/etc/systemd/system"
	if err := os.MkdirAll(serviceDir, 0755); err != nil {
		return fmt.Errorf("failed to create systemd directory: %w", err)
	}

	serviceContent := renderService(serviceName, binaryPath, args)

	serviceFile := filepath.Join(serviceDir, fmt.Sprintf("gotunnel-%s.service", serviceName))

	// Write service file
	if err := os.WriteFile(serviceFile, []byte(serviceContent), 0600); err != nil {
		return fmt.Errorf("failed to write service file: %w", err)
	}

	// Reload systemd daemon
	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("failed to reload systemd: %w", err)
	}

	// Enable service
	if err := exec.Command("systemctl", "enable", fmt.Sprintf("gotunnel-%s.service", serviceName)).Run(); err != nil {
		return fmt.Errorf("failed to enable service: %w", err)
	}

	log.Printf("Service registered: gotunnel-%s.service", serviceName)
	log.Printf("Service file: %s", serviceFile)
	log.Printf("Start service with: sudo systemctl start gotunnel-%s.service", serviceName)
	log.Printf("View logs with: journalctl -u gotunnel-%s.service -f", serviceName)
	return nil
}

// unregisterService removes the systemd service file
func unregisterService(serviceName string) error {
	// Check for root privileges
	if !isRoot() {
		return fmt.Errorf("service unregistration requires root privileges. Use: sudo gotunnel %s -unregister", serviceName)
	}

	serviceNameFull := fmt.Sprintf("gotunnel-%s.service", serviceName)

	// Stop service if running
	exec.Command("systemctl", "stop", serviceNameFull).Run()

	// Disable service
	exec.Command("systemctl", "disable", serviceNameFull).Run()

	// Remove service file
	serviceFile := filepath.Join("/etc/systemd/system", serviceNameFull)
	if err := os.Remove(serviceFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove service file: %w", err)
	}

	// Reload systemd daemon
	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("failed to reload systemd: %w", err)
	}

	log.Printf("Service unregistered: %s", serviceNameFull)
	log.Printf("Service file: %s", serviceFile)
	return nil
}

// filterArgs removes -register and -unregister flags from arguments
func filterArgs(args []string) []string {
	var filtered []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-register" || arg == "-unregister" {
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered
}

func runServer() {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config file (optional, for persistence)")
	port := fs.Int("port", 7727, "Server tunnel port")
	panelPort := fs.Int("panel-port", 7726, "Web panel port")
	panelHost := fs.String("panel-host", "0.0.0.0", "Web panel bind address")
	password := fs.String("password", "", "Legacy shared password")
	clientPassword := fs.String("client-password", "", "Tunnel client password")
	adminPassword := fs.String("admin-password", "", "Web panel password")
	tlsCert := fs.String("tls-cert", "", "TLS certificate for client transport")
	tlsKey := fs.String("tls-key", "", "TLS private key for client transport")
	debug := fs.Bool("debug", false, "Enable debug logging")
	register := fs.Bool("register", false, "Register as systemd service")
	unregister := fs.Bool("unregister", false, "Unregister systemd service")
	help := fs.Bool("help", false, "Show help message")

	fs.Usage = func() {
		fmt.Printf(`GoTunnel Server

USAGE:
  gotunnel server [options]

OPTIONS:
  -config string      Path to config file (optional, for persistence)
  -port int           Server tunnel port (default 7727)
  -panel-port int     Web panel port (default 7726)
  -panel-host string  Web panel bind address (default "0.0.0.0")
  -password string    Legacy shared password
  -client-password string  Tunnel client password
  -admin-password string   Web panel password
  -tls-cert string    TLS certificate for client transport
  -tls-key string     TLS private key for client transport
  -debug              Enable debug logging
  -register           Register as systemd service with current options (requires root)
  -unregister         Unregister systemd service (requires root)
  -help               Show this help message

EXAMPLES:
  gotunnel server -password MyPassword
  sudo gotunnel server -port 7727 -password MyPassword -register
  sudo gotunnel server -unregister
  gotunnel server -config /etc/gotunnel/config.json -password MyPassword
  gotunnel server -port 7727 -panel-port 7726 -password MyPassword -config config.json
`)
	}

	fs.Parse(os.Args[2:])

	if *help {
		fs.Usage()
		return
	}

	// Handle unregister first (doesn't need password validation)
	if *unregister {
		if err := unregisterService("server"); err != nil {
			log.Fatalf("Failed to unregister service: %v", err)
		}
		return
	}

	// Handle register (doesn't need password validation)
	if *register {
		filteredArgs := filterArgs(os.Args[2:])
		args := append([]string{"server"}, filteredArgs...)
		if err := registerService("server", args); err != nil {
			log.Fatalf("Failed to register service: %v", err)
		}
		return
	}

	if *password != "" {
		if *clientPassword == "" {
			*clientPassword = *password
		}
		if *adminPassword == "" {
			*adminPassword = *password
		}
	}
	if *clientPassword == "" || *adminPassword == "" {
		log.Fatalf("Error: provide -client-password and -admin-password (or legacy -password)")
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		log.Fatalf("Error: -tls-cert and -tls-key must be provided together")
	}

	if *debug {
		log.Println("Debug logging enabled")
	}

	// Create config manager
	var cfgMgr *config.Manager

	if *configPath != "" {
		// Load/create persistent config
		cfgMgr = config.NewManager(*configPath)
		if err := cfgMgr.Load(); err != nil {
			log.Fatalf("Failed to load config: %v", err)
		}
		log.Printf("Using config file: %s", *configPath)
	} else {
		// Start with fresh in-memory config (no persistence)
		cfgMgr = config.NewManager("")
		cfgMgr.InitDefault()
		log.Println("Running with default config (no persistence)")
	}

	if err := cfgMgr.SetClientPassword(*clientPassword); err != nil {
		log.Fatalf("Failed to set client password: %v", err)
	}
	if err := cfgMgr.SetAdminPassword(*adminPassword); err != nil {
		log.Fatalf("Failed to set admin password: %v", err)
	}

	if *port != 7727 {
		if err := cfgMgr.SetServerPort(*port); err != nil {
			log.Fatalf("Failed to set port: %v", err)
		}
	}

	if *panelPort != 7726 {
		if err := cfgMgr.SetPanelPort(*panelPort); err != nil {
			log.Fatalf("Failed to set panel port: %v", err)
		}
	}
	if *panelHost != "0.0.0.0" {
		if err := cfgMgr.SetPanelHost(*panelHost); err != nil {
			log.Fatalf("Failed to set panel host: %v", err)
		}
	}

	// Create and start server
	srv := server.NewServer(cfgMgr)
	if *tlsCert != "" {
		if err := srv.EnableTLS(*tlsCert, *tlsKey); err != nil {
			log.Fatalf("Failed to enable TLS: %v", err)
		}
	}
	if err := srv.Start(); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

	// Wait for interrupt
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down server...")
	if err := srv.Stop(); err != nil {
		log.Fatalf("Error stopping server: %v", err)
	}

	log.Println("Server stopped")
}

func runClient() {
	fs := flag.NewFlagSet("client", flag.ExitOnError)
	server := fs.String("server", "", "Server address (host:port)")
	pass := fs.String("pass", "", "Server password")
	name := fs.String("name", "", "Machine name/identity")
	debug := fs.Bool("debug", false, "Enable debug logging")
	verbose := fs.Bool("verbose", false, "Enable verbose output")
	reconnectInterval := fs.Int("reconnect-interval", 5, "Reconnect interval in seconds")
	tlsCA := fs.String("tls-ca", "", "CA certificate for encrypted server verification")
	tlsServerName := fs.String("tls-server-name", "", "TLS certificate server name")
	register := fs.Bool("register", false, "Register as systemd service")
	unregister := fs.Bool("unregister", false, "Unregister systemd service")
	help := fs.Bool("help", false, "Show help message")

	fs.Usage = func() {
		fmt.Printf(`GoTunnel Client

USAGE:
  gotunnel client [options]

OPTIONS:
  -server string             Server address (required, e.g., example.com:7727)
  -pass string               Server password (required for normal operation)
  -name string               Machine name/identity (required for normal operation)
	  -debug                     Enable debug logging
  -verbose                   Enable verbose output
  -reconnect-interval int    Reconnect interval in seconds (default 5)
  -tls-ca string             CA certificate for encrypted server verification
  -tls-server-name string    TLS certificate server name
  -register                  Register as systemd service with current options (requires root)
  -unregister                Unregister systemd service (requires root)
  -help                      Show this help message

EXAMPLES:
  gotunnel client -server example.com:7727 -pass MyPassword -name my-pc
  sudo gotunnel client -server example.com:7727 -pass MyPassword -name my-pc -register
  sudo gotunnel client -unregister
  gotunnel client -server 192.168.1.100:7727 -pass MyPassword -name laptop -debug
  gotunnel client -server vps.example.com:7727 -pass MyPassword -name home-server -reconnect-interval 10
`)
	}

	fs.Parse(os.Args[2:])

	if *help {
		fs.Usage()
		return
	}

	// Handle unregister first (doesn't need other flag validation)
	if *unregister {
		if err := unregisterService("client"); err != nil {
			log.Fatalf("Failed to unregister service: %v", err)
		}
		return
	}

	// Handle register (doesn't need other flag validation)
	if *register {
		filteredArgs := filterArgs(os.Args[2:])
		args := append([]string{"client"}, filteredArgs...)
		if err := registerService("client", args); err != nil {
			log.Fatalf("Failed to register service: %v", err)
		}
		return
	}

	// Validate required flags for normal operation
	if *server == "" || *pass == "" || *name == "" {
		fmt.Println("Error: -server, -pass, and -name flags are required")
		fs.Usage()
		os.Exit(1)
	}

	if *debug {
		log.Println("Debug logging enabled")
	}

	if *verbose {
		log.Println("Verbose output enabled")
	}

	// Create client
	cli := client.NewClient(*server, *name, *pass)
	if *tlsCA != "" || *tlsServerName != "" {
		if err := cli.EnableTLS(*tlsCA, *tlsServerName); err != nil {
			log.Fatalf("Failed to configure TLS: %v", err)
		}
	}
	if err := cli.SetReconnectInterval(time.Duration(*reconnectInterval) * time.Second); err != nil {
		log.Fatalf("Invalid reconnect interval: %v", err)
	}

	// Start client
	if err := cli.Start(); err != nil {
		log.Fatalf("Failed to start client: %v", err)
	}

	// Wait for interrupt
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down client...")
	if err := cli.Stop(); err != nil {
		log.Fatalf("Error stopping client: %v", err)
	}

	log.Println("Client stopped")
}
