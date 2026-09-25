# GoTunnel

GoTunnel is a self-hosted TCP reverse tunnel written in Go. A client running
behind NAT opens a long-lived control connection to the server. The server then
accepts traffic on public tunnel ports and multiplexes those streams over that
connection to services reachable by the client.

The project is a good fit for personal infrastructure, labs, and small
controlled deployments where a simple executable and web panel are preferable
to a larger networking platform. Its core is now well defended against common
failure modes: bounded queues, timeouts, connection liveness checks, graceful
shutdown, isolated stream failures, optional TLS 1.3, and race-tested integration
coverage.

It is not yet a high-availability tunneling service. A server is a single point
of failure, active streams end when a client reconnects, and configuration is
stored locally. See [Current limits](#current-limits) before using it for a
critical service.

## How it works

```text
public user -> server tunnel port -> multiplexed control connection -> client -> local service
                                      server:7727                    LAN/localhost

administrator -> HTTP or HTTPS reverse proxy -> web panel -> server state
```

Each client authenticates with a client password and registers a unique machine
name. Tunnel definitions map a public TCP port on the server to a loopback port
on that client. Every accepted public connection becomes an independent stream
on the client's control connection.

The control protocol carries authentication, tunnel configuration, stream data,
ordered half-close notifications, close notifications, and heartbeats. A busy or
blocked stream cannot grow memory without limit; overflowing that stream's queue
closes the stream while leaving the client connection and other streams alive.

## Reliability and security features

- Separate client and web-administrator credentials, with a legacy
  `-password` compatibility option
- Optional TLS 1.3 for the server-client control connection
- Authentication deadlines and a cap on unauthenticated connections
- Heartbeat liveness checks and stale-connection cleanup
- Exponential reconnect delay with jitter, capped at one minute
- Limits for streams, queued messages, local dials, and message size
- TCP half-close propagation for protocols that depend on EOF
- Graceful shutdown of listeners, clients, streams, and the web server
- Web login throttling, session limits, secure cookie handling, CSRF/origin
  checks, and security headers
- Bounded in-memory logs and 10 MiB startup rotation for the log file
- Runtime status metrics in the authenticated `/api/status` response
- Hardened systemd units with a dynamic service account and a private unit file

## Build

GoTunnel uses the Go version declared in `go.mod` (currently Go 1.26.7).

```bash
git clone https://github.com/yourusername/gotunnel.git
cd gotunnel
go build -o gotunnel .
```

## Secure quick start

Create independent secrets for clients and administrators. For a public server,
also provide a certificate whose name matches the address used by clients.

```bash
./gotunnel server \
  -client-password "$CLIENT_SECRET" \
  -admin-password "$ADMIN_SECRET" \
  -tls-cert server.crt \
  -tls-key server.key \
  -config config.json \
  -panel-host 127.0.0.1
```

Connect a client with a private CA:

```bash
./gotunnel client \
  -server tunnel.example.com:7727 \
  -pass "$CLIENT_SECRET" \
  -name home-server \
  -tls-ca ca.crt \
  -tls-server-name tunnel.example.com
```

For a certificate issued by a public CA, omit `-tls-ca` and provide
`-tls-server-name`. Supplying either client TLS option enables TLS verification.
The server must receive both `-tls-cert` and `-tls-key` to enable TLS.

Without these TLS flags, the control connection remains plaintext for backward
compatibility. Authentication does not encrypt tunnel traffic by itself.

The legacy form below still works, but gives clients and administrators the same
secret:

```bash
./gotunnel server -password "$SHARED_SECRET"
```

## Server options

```text
-port int                 Control listener port (default 7727)
-panel-host string        Web listener address (default "0.0.0.0")
-panel-port int           Web listener port (default 7726)
-client-password string   Required client credential
-admin-password string    Required web-panel credential
-password string          Legacy fallback for both credentials
-tls-cert string          Control-connection certificate
-tls-key string           Control-connection private key
-config string            Optional configuration file for persistence
-debug                    Enable debug logging
-register                 Register a systemd service
-unregister               Remove the systemd service
```

The server currently requires credentials at startup, even when an existing
configuration file contains stored values. Prefer the separate password flags.

## Client options

```text
-server string            Server control address (default "localhost:7727")
-pass string              Client credential (required)
-name string              Unique machine name (required)
-tls-ca string            Optional PEM CA bundle; enables TLS
-tls-server-name string   Certificate DNS name; enables TLS
-reconnect-interval int   Initial reconnect delay in seconds (default 5)
-debug                    Enable debug logging
-register                 Register a systemd service
-unregister               Remove the systemd service
```

## Configuration

Tunnel changes made through the web panel are persisted to JSON. A representative
file is:

```json
{
  "server": {
    "port": 7727,
    "panel_host": "127.0.0.1",
    "panel_port": 7726,
    "client_password": "replace-with-client-secret",
    "admin_password": "replace-with-admin-secret"
  },
  "machines": {
    "home-server": {
      "tunnels": [
        {
          "remote": 2222,
          "local": 22
        }
      ]
    }
  }
}
```

The program writes configuration atomically and restricts the file to mode
`0600`. Loading an older file with a single `password` field migrates that value
to both credential fields. Startup flags set the active credentials and persist
them on the next save.

Treat the configuration file and service unit as secrets. Command-line arguments
may also be visible to privileged local users through process inspection.

## Web panel

Open `http://127.0.0.1:7726` when running locally and sign in with the
administrator password. The panel can create and remove tunnels and shows
connected clients, active tunnels, runtime health, and recent logs.

The panel serves HTTP directly. For remote administration, bind it to loopback
with `-panel-host 127.0.0.1` and expose it through an HTTPS reverse proxy. The
panel only trusts forwarded HTTPS information from loopback proxy connections;
this allows its session cookie to receive the `Secure` attribute without trusting
arbitrary client headers.

Do not confuse the web reverse proxy with the tunnel transport. A conventional
HTTP proxy protects the panel, while the server-client control port uses its own
optional TLS configuration.

## Operating limits and failure behavior

The default limits are deliberately conservative:

| Resource | Default |
| --- | ---: |
| Active streams per client | 100 |
| Concurrent pending local dials | 50 |
| Queued messages per stream | 50 |
| Maximum control message | 10 MiB |
| Authentication deadline | 10 seconds |
| Simultaneous unauthenticated connections | 64 |
| Server heartbeat interval | 10 seconds |
| Client control-read timeout | 20 seconds |
| Missed heartbeats before server cleanup | 3 |

When a per-stream queue fills, only that stream is closed. When a control
connection becomes stale, all of its streams are closed and the client reconnects.
Existing TCP sessions are not replayed or resumed after reconnect; applications
must open new connections.

A graceful server shutdown stops accepting new work and closes the web server,
clients, streams, and listeners. Active HTTP and tunneled requests can be
interrupted during shutdown; process death, host failure, or a network partition
also interrupts traffic immediately.

## systemd services

Registration requires root because it writes to `/etc/systemd/system`:

```bash
sudo ./gotunnel server \
  -client-password "$CLIENT_SECRET" \
  -admin-password "$ADMIN_SECRET" \
  -config /var/lib/gotunnel/config.json \
  -register

sudo ./gotunnel client \
  -server tunnel.example.com:7727 \
  -pass "$CLIENT_SECRET" \
  -name home-server \
  -tls-ca /var/lib/gotunnel/ca.crt \
  -tls-server-name tunnel.example.com \
  -register
```

The generated unit uses `DynamicUser`, a persistent `/var/lib/gotunnel` state
directory, a restricted filesystem view, a private temporary directory, and only
the capability needed to bind privileged ports. The unit file is mode `0600`
because it contains command-line credentials.

Use absolute paths for configuration and TLS files. Files must be readable by the
dynamic service identity. If your certificate-key policy cannot grant that access,
maintain a custom unit that supplies the key through your platform's credential
facility.

Manage the services normally:

```bash
sudo systemctl status gotunnel-server
sudo journalctl -u gotunnel-server -f
sudo ./gotunnel server -unregister
```

The built-in client unit is named `gotunnel-client`. Register one client per host
with this mechanism; multiple client instances require separately maintained unit
names.

## Monitoring

The authenticated `/api/status` endpoint includes:

- server uptime and Go runtime version
- goroutine count and allocated memory
- connected clients and active tunnels
- dropped in-memory log entries

The metrics are process-local and reset after restart. GoTunnel does not yet
export Prometheus metrics or distributed traces.

## Current limits

- One server instance owns all active clients and tunnel listeners; there is no
  clustering, failover, or shared configuration.
- Active streams do not survive a client reconnect.
- Only TCP forwarding is supported.
- The custom control protocol has no negotiated version for rolling upgrades.
- TLS for the control connection is optional, and the web panel needs an external
  HTTPS proxy for encryption.
- Credentials are supplied through flags/configuration rather than a secret
  manager, and there is no credential rotation protocol.
- Logging and metrics are local; there is no audit-log or alerting integration.

These are product and deployment limitations rather than known data races or
unbounded-resource bugs. They define the main work needed before treating the
project as a multi-tenant or highly available service.

## Development and verification

Run the complete local checks with:

```bash
gofmt -l .
go vet ./...
go test -race ./...
go build ./...
git diff --check
```

The test suite covers protocol validation, configuration migration, bounded
queues and dials, TLS control connections, concurrent traffic, slow local targets,
reconnect cleanup, half-close behavior, web security, service registration, and
graceful shutdown.

## License

MIT
