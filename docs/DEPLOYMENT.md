# Deployment guide

This guide describes the recommended security boundary for an Internet-reachable
GoTunnel server.

## Network exposure

Expose only the ports that are required:

| Port | Purpose | Recommended exposure |
| --- | --- | --- |
| `7727/tcp` | Client control connection | Known client networks where possible |
| `7726/tcp` | Web panel | Loopback only |
| Configured tunnel ports | Forwarded application traffic | Intended application users |

Bind the panel to `127.0.0.1` and place an authenticated HTTPS reverse proxy in
front of it. The panel's own login remains required. Firewall rules should block
direct remote access to port 7726.

## Credentials

Generate different high-entropy values for `-client-password` and
`-admin-password`. A leaked client credential should not grant web administration.
The legacy `-password` option removes that separation and is intended only for
compatibility.

Protect `config.json` and generated systemd units: both can contain credentials.
Command-line credentials can be observed by privileged users on the host. For an
environment with stricter secret-handling requirements, maintain a custom service
unit and integrate the host's credential facility.

## Control TLS

Provide both certificate files on the server:

```bash
./gotunnel server \
  -client-password "$CLIENT_SECRET" \
  -admin-password "$ADMIN_SECRET" \
  -tls-cert /absolute/path/server.crt \
  -tls-key /absolute/path/server.key \
  -panel-host 127.0.0.1
```

For a public CA, enable TLS on the client by specifying the expected certificate
name:

```bash
./gotunnel client \
  -server tunnel.example.com:7727 \
  -pass "$CLIENT_SECRET" \
  -name site-a \
  -tls-server-name tunnel.example.com
```

For a private CA, also pass `-tls-ca /absolute/path/ca.crt`. Do not disable name
verification by using an unrelated server name. A TLS client cannot connect to a
plaintext server, and a plaintext client cannot authenticate to a TLS server.

## Reverse proxy

Proxy the administrator hostname to `http://127.0.0.1:7726` and set the forwarded
scheme to HTTPS. GoTunnel accepts that scheme only from a loopback peer and marks
the login cookie secure. Terminate TLS and redirect plain HTTP at the proxy.

The proxy does not protect port 7727 or arbitrary tunnel ports. Configure control
TLS separately, and use the forwarded application's own TLS when end-to-end
application encryption is required.

## systemd registration

The built-in registration writes a hardened unit and enables it immediately.
Review the generated unit in `/etc/systemd/system` after registration. Runtime
state belongs under `/var/lib/gotunnel`; use absolute paths and ensure TLS files
are readable by the dynamic service identity.

Useful commands:

```bash
sudo systemctl status gotunnel-server
sudo journalctl -u gotunnel-server -f
sudo systemctl restart gotunnel-server
```

After configuration or certificate changes, restart the service and verify a TLS
client connection before relying on the tunnel.

## Health checks

Log in to the panel and inspect `/api/status`. Verify that expected clients and
active tunnels appear and that goroutine and allocated-memory values stabilize
after repeated connection tests.

The status endpoint requires an administrator session. If external monitoring is
needed, add it at the reverse proxy or build a narrow exporter rather than making
the whole panel public.

## Upgrade procedure

There is currently no wire-protocol version negotiation or active-stream
migration. Treat upgrades as a brief outage:

1. Back up the configuration file.
2. Build and test the new binary.
3. Stop clients or accept that their active streams will close.
4. Replace and restart the server binary.
5. Replace and restart clients.
6. Confirm reconnection, tunnel listeners, TLS validation, and application traffic.

For rollback, restore the previous binaries and configuration together. Avoid a
long-lived mix of versions unless that exact combination has been tested.
