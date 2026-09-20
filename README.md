# GoTunnel

A self-hosted reverse TCP tunneling solution for exposing local services behind firewalls and NAT.

## Overview

GoTunnel is a lightweight, self-contained reverse tunnel server that allows you to expose services running on machines behind firewalls or NAT to the public internet. It uses a persistent client connection to maintain the tunnel, eliminating the need for complex networking configuration.

## How It Works

### Architecture

GoTunnel operates on a client-server model:

- **Server**: Runs on a publicly accessible machine (VPS, cloud instance). It listens for client connections and manages port mappings.
- **Client**: Runs on machines behind firewalls or NAT. It connects to the server and forwards incoming connections to local services.

### Connection Flow

1. Client initiates a persistent TCP connection to the server on port 7727
2. Client authenticates with a password
3. Server sends the list of tunnel configurations to the client
4. When an external user connects to a tunnel port on the server, the server forwards that connection through the existing client connection
5. Client accepts the connection and forwards it to the local service
6. Data flows bidirectionally between the external user and the local service

### Protocol

GoTunnel uses a custom binary protocol with the following message types:

- AUTH (0): Client authentication request
- AUTH_RESPONSE (1): Server authentication response
- TUNNEL_CONFIG (2): Server sends tunnel mappings to client
- STREAM_OPEN (3): Server initiates a new stream for incoming connection
- STREAM_READY (4): Client confirms stream is connected to local service
- STREAM_DATA (5): Bidirectional data transfer
- STREAM_CLOSE (6): Close stream
- PING (7): Keepalive from server
- PONG (8): Keepalive response from client

Keepalive messages (PING/PONG) are sent every 30 seconds to detect dead connections.

## Features

- Single binary for both server and client modes
- Web-based management panel for dynamic tunnel configuration
- Add/remove tunnels without restarting
- Real-time client and tunnel monitoring
- **Production-hardened reliability**:
 - Comprehensive error handling and recovery
 - Automatic client reconnection with 5-second backoff
 - Keepalive detection (PING/PONG every 30 seconds)
 - Resource limits (100 streams per client, 10MB max message size)
 - Proper goroutine lifecycle management
 - Zero data loss on brief disconnects
- **Comprehensive logging system**:
 - Real-time log display in web panel
 - Log filtering by level (Info/Warning/Error)
 - File persistence to `logs/gotunnel.log`
 - In-memory circular buffer (latest 1000 entries)
- **Secure authentication**:
 - Password-protected web panel
 - Session-based authentication
 - Rate limiting (5 failed attempts = 15-minute lockout)
- Persistent connection-based tunneling (no port scanning)
- Thread-safe concurrent stream handling
- Atomic config persistence
- Systemd service integration for auto-start and monitoring

## Installation

### Building from Source

```bash
cd GoTunnel
go build -o gotunnel .
```

Requires Go 1.18 or later.

## Usage

### Server Mode

Start the server:

```bash
./gotunnel server
```

With options:

```bash
./gotunnel server -port 7727 -panel-port 7726 -password MyPassword -config config.json
```

Options:
- `-port`: Server tunnel listening port (default: 7727)
- `-panel-port`: Web management panel port (default: 7726)
- `-password`: Authentication password
- `-config`: Path to config file for persistence (optional)
- `-debug`: Enable debug logging

Note: Without `-config`, the server starts with default in-memory configuration (no persistence). With `-config config.json`, the server will load/create and persist configuration changes.

#### Web Panel

Access the management panel at `http://localhost:7726` after starting the server.

Default credentials:
- Password: Use the `-password` flag value

Features:
- View connected clients and active tunnels
- Add new tunnel mappings
- Remove existing tunnels
- Real-time status updates
- **Server Logs Section**:
 - Real-time view of all server events
 - Filter logs by level (All/Info/Warning/Error)
 - Clear in-memory logs with one click
 - Auto-updates every 2 seconds (no auto-scroll, manual scrolling allowed)
 - Logs show: client connections, PING/PONG keepalive, tunnel events, stream lifecycle

### Client Mode

Connect to a server:

```bash
./gotunnel client -server example.com:7727 -pass MyPassword -name my-pc
```

Options:
- `-server`: Server address in host:port format (required)
- `-pass`: Authentication password (required)
- `-name`: Machine identifier (required)
- `-debug`: Enable debug logging
- `-verbose`: Enable verbose output

The client will automatically reconnect if the connection drops, with a 5-second backoff interval.

## Configuration

### Configuration File Format

When using `-config config.json`, the configuration is stored in JSON format:

```json
{
 "server": {
 "port": 7727,
 "panel_port": 7726,
 "password": "MyPassword"
 },
 "machines": {
 "machine-name": {
 "tunnels": [
 {
 "remote": 8001,
 "local": 3000
 }
 ]
 }
 }
}
```

### Configuration Management

- **Server Port**: Public port where the server listens for client connections
- **Panel Port**: Port for the web management interface
- **Password**: Authentication password for all clients
- **Machines**: Map of connected machines and their tunnel configurations
 - **Remote Port**: Public port on the server
 - **Local Port**: Port on the client machine where the service runs

## Examples

### Example 1: Expose a Local Web Server

Machine: laptop
Local service: Web server running on localhost:3000
Public access: Through server port 8001

Server:
```bash
./gotunnel server -port 7727 -panel-port 7726 -password secure123 -config config.json
```

Client:
```bash
./gotunnel client -server example.com:7727 -pass secure123 -name laptop
```

Then add tunnel via web panel:
- Server Port (public): 8001
- Service Port (private): 3000

External users can access the web server at: `http://example.com:8001`

### Example 2: Expose Multiple Services

Machine: home-server
Local services:
- SSH on localhost:22
- Web UI on localhost:8080
- API on localhost:5000

Client:
```bash
./gotunnel client -server example.com:7727 -pass secure123 -name home-server
```

Add tunnels via web panel:
- Server 2222 -> Service 22 (SSH)
- Server 8080 -> Service 8080 (Web UI)
- Server 5000 -> Service 5000 (API)

External users can:
- SSH: `ssh user@example.com -p 2222`
- Web: `http://example.com:8080`
- API: `http://example.com:5000`

## Logging

GoTunnel maintains comprehensive logs for all server events:

### Log Locations

1. **Terminal Output**: Real-time logs printed to stdout
2. **Web Panel**: Real-time logs visible in the browser at `http://server:7726`
3. **File**: Permanent audit trail at `logs/gotunnel.log`

### Logged Events

- Server startup/shutdown
- Client connections and disconnections
- Tunnel listener start/stop
- Stream lifecycle (open, ready, close)
- PING/PONG keepalive messages
- Failed login attempts (rate limiting triggers)

### Log Levels

- **INFO**: Normal server operations
- **WARNING**: Potentially problematic situations
- **ERROR**: Errors and failures

### Log File Format

Logs are stored in `logs/gotunnel.log` with the format:
```
[2026-09-15 22:39:11] INFO: Stream 1 opened for port 7777 (local: 8090)
```

### Web Panel Log Features

- Real-time display (updates every 2 seconds)
- Filter by log level without affecting file logs
- Clear in-memory logs (file logs persist)
- Manual scrolling (logs don't auto-scroll)
- Recent 1000 entries kept in memory

## Production Deployment

GoTunnel has been thoroughly tested and is **production-ready** for self-hosted reverse tunneling scenarios.

### Deployment Checklist

```
□ Use strong password (20+ characters, mix of uppercase/lowercase/numbers/symbols)
□ Deploy on a dedicated VPS or cloud instance
□ Set up Cloudflare or reverse proxy for TLS encryption
□ Enable firewall rules to restrict access to tunnel port
□ Configure systemd service for auto-restart: ./gotunnel server -register
□ Set up log rotation for logs/gotunnel.log
□ Monitor Cloudflare/proxy logs for suspicious activity
□ Test reconnection scenarios before full deployment
□ Set up automated backups of config.json
□ Consider redundant server setup for high-availability
```

### Recommended Use Cases

 **Well-Suited For**:
- Internal corporate reverse tunneling
- Exposing services on machines behind NAT/firewalls
- Low-to-moderate traffic scenarios (<1000 concurrent connections)
- Intranet services (SSH, web apps, APIs)
- Development and staging environments
- Remote office access to internal services

 **Requires Additional Setup**:
- High-traffic scenarios (100K+ concurrent streams): consider load balancing
- Mission-critical 24/7 systems: implement health checks and failover
- Large file transfers: test throughput and adjust message size limits

### Performance Expectations

- **Latency**: <50ms with Cloudflare, <10ms on same network
- **Throughput**: Limited by bandwidth and connection limits
- **Concurrent Streams**: Up to 100 per client (configurable)
- **Memory**: ~100KB per active stream

### Monitoring

Monitor these metrics in production:
- Server logs for connection errors or stream failures
- Cloudflare analytics for DDoS/bot activity
- System resources (CPU, memory, file descriptors)
- Failed login attempts in web panel
- Client reconnection frequency (indicates network instability)

## Security Considerations

### Basic Security

- Always use a strong password (20+ characters recommended)
- Run the server on a machine with restricted firewall rules
- Regularly update the password if multiple users have access
- Monitor the web panel for unauthorized tunnel additions
- Monitor logs for failed login attempts and suspicious activity
- Use rate limiting on the server side to prevent brute force attacks

### Production Deployment with Cloudflare (Recommended)

GoTunnel is **production-ready** when deployed behind Cloudflare for maximum security:

**Setup**:
```bash
# Server runs on internal network
./gotunnel server -port 7727 -panel-port 7726 -password "strong-password" -config config.json

# Point your domain to Cloudflare
# example.com CNAME → your-tunnel.pages.cloudflare.com
```

**Cloudflare Configuration**:
- Enable **Full (strict)** SSL/TLS mode
- Enable **HSTS** (Strict-Transport-Security)
- Set **Security Level: High**
- Enable **Bot Management** to prevent automated attacks
- Add **WAF rules** to block automated tools on `/api/*` paths
- Configure **Rate Limiting**: 100 requests/minute per IP

**Benefits**:
- TLS 1.3 / HTTP/3 encryption (client Cloudflare)
- DDoS protection and bot detection
- WAF rules prevent malicious patterns
- Geographic routing and performance optimization
- Defense-in-depth security (password + CF auth)

### Alternative: Reverse Proxy Setup

If not using Cloudflare, run behind a reverse proxy (nginx, caddy, Traefik):
```nginx
server {
 listen 443 ssl http2;
 server_name example.com;

 ssl_certificate /path/to/cert.pem;
 ssl_certificate_key /path/to/key.pem;
 ssl_protocols TLSv1.2 TLSv1.3;

 location / {
 proxy_pass http://localhost:7726;
 proxy_set_header Host $host;
 }
}
```

## Troubleshooting

### Client Connection Issues

**Problem**: Client fails to connect
- Check server is running: `netstat -tuln | grep 7727`
- Verify password matches
- Check firewall rules on server machine
- Verify correct server address and port
- Check server logs for authentication errors

**Problem**: Keepalive timeout messages
- These are normal every 30 seconds
- Indicates PING/PONG keepalive is working
- Check logs for "Sent PING" and "Received PONG" messages

### Web Panel Issues

**Problem**: Web panel shows "Server Offline"
- Check server is running
- Verify panel port (default 7726)
- Check firewall allows access to panel port
- Try refreshing the browser

**Problem**: Web panel login fails
- Verify password is correct (case-sensitive)
- Check for rate limiting: 5 failed attempts lock out IP for 15 minutes
- Check server logs for failed login attempts

**Problem**: Logs not appearing in web panel
- Logs update every 2 seconds - wait briefly
- Check server logs (terminal or file) to verify events are being logged
- Try clearing logs and creating new events (add tunnel, connect client)

### Tunnel Not Working

**Problem**: External connection to tunnel port fails
- Verify tunnel was added through web panel
- Check local service is running on the specified port
- Verify client is connected (shows in web panel)
- Check server firewall allows traffic to tunnel port
- Check logs for stream open/close events to diagnose issues

## Service Management

### Running as a System Service

GoTunnel can be registered as a system-wide systemd service for automatic startup and management. **Service registration requires root privileges.**

#### Registering the Server as a Service

```bash
sudo ./gotunnel server -port 7727 -panel-port 7726 -password "YourPassword" -register
```

This will:
- Create `/etc/systemd/system/gotunnel-server.service` with your exact configuration
- Preserve all command-line arguments
- Enable automatic startup on system boot
- Enable automatic restart on failure

#### Registering the Client as a Service

```bash
sudo ./gotunnel client -server example.com:7727 -pass "YourPassword" -name "my-machine" -register
```

#### Service Management Commands

After registration, manage the service with:

```bash
# Start the service
sudo systemctl start gotunnel-server.service

# Stop the service
sudo systemctl stop gotunnel-server.service

# Restart the service
sudo systemctl restart gotunnel-server.service

# Check service status
sudo systemctl status gotunnel-server.service

# View live logs
journalctl -u gotunnel-server.service -f

# Enable/disable auto-start
sudo systemctl enable gotunnel-server.service
sudo systemctl disable gotunnel-server.service
```

#### Unregistering a Service

```bash
sudo ./gotunnel server -unregister
# or
sudo ./gotunnel client -unregister
```

This will:
- Stop the service if running
- Disable auto-start
- Remove the service file from `/etc/systemd/system/`

**Note**: Direct terminal usage is always available. Registration creates a system-wide service; you can still run the binary directly from the terminal at any time:

```bash
./gotunnel server -port 7727 -password "MyPassword"
./gotunnel client -server example.com:7727 -pass "MyPassword" -name "my-pc"
```

## Performance

GoTunnel is designed for moderate traffic volumes. Each active connection consumes:
- One goroutine for reading
- One goroutine for each stream
- Memory for buffering data between connections

Typical memory usage: 10-50MB per 100 concurrent streams.

## Project Structure

```
GoTunnel/
 main.go - Entry point, CLI parsing
 internal/
  server/
      server.go - Main server logic
      connection.go - Client connection handling
      web.go - Web API and panel serving
  web/
      index.html - Web panel UI
      style.css - Styling
      app.js - Frontend logic
  client/
      client.go - Client connection logic
  config/
      config.go - Configuration management
  auth/
      auth.go - Authentication logic
  protocol/
      protocol.go - Message encoding/decoding
```

## Development

### Building

```bash
go build -o gotunnel .
```

### Running with Debug Output

```bash
./gotunnel server -debug
./gotunnel client -server localhost:7727 -pass MyPassword -name test -debug
```

### Testing

```bash
go test ./...
```

## License

MIT

## Support

For issues, questions, or contributions, please refer to the project repository.
