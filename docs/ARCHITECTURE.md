# Architecture

This document records the runtime model and the invariants that keep GoTunnel
stable under disconnects, slow endpoints, and concurrent shutdown.

## Components

- `main.go` parses commands, installs signal handling, configures TLS, and owns
  service registration.
- `internal/server` accepts authenticated client control connections, owns public
  tunnel listeners, multiplexes streams, serves the web panel, and exposes status.
- `internal/client` maintains one control connection, reconnects after failures,
  and maps server stream requests to bounded local TCP dials.
- `internal/protocol` validates length-prefixed JSON control messages.
- `internal/systemmetrics` samples Linux host and process resource counters.
- `internal/tunnel` copies TCP traffic in both directions and preserves ordered
  half-close behavior.
- `internal/config` loads, migrates, validates, and atomically saves persistent
  configuration.

## Connection lifecycle

1. The client dials the control port, optionally with TLS 1.3.
2. It sends an authentication message containing its machine name and client
   password before the server's authentication deadline.
3. The server registers the connection. A new connection with the same name
   replaces and closes the old one. The server sends that machine's configured
   tunnel definitions.
4. A listener accepts a public connection and allocates a stream identifier.
5. The server sends an open request. The client acquires a bounded dial slot and
   connects to the requested local service.
6. Both sides exchange stream data through bounded per-stream queues. A queue
   overflow closes that stream without blocking the control reader.
7. EOF in one direction sends an ordered half-close. The opposite direction may
   continue until it also finishes or an error closes the stream.
8. A heartbeat timeout or control error removes the client and closes all streams.
   Configured public listeners remain available and immediately reject new traffic
   while the machine is offline. The client retries with an exponentially
   increasing, jittered delay.

While connected, the client sends a telemetry message every five seconds. It
contains host and process CPU/memory values, uptime, active stream count, and
tunnel byte totals/rates. The server records receipt time independently of the
client clock and the panel marks telemetry stale after fifteen seconds.

## Ownership and concurrency

The control reader must remain responsive. It validates and dispatches messages;
it does not wait indefinitely for local network operations or stream consumers.
Potentially slow local dials run asynchronously behind a semaphore.

Every stream has a single lifecycle with idempotent close behavior. Closing a
stream releases its socket and queue once, even if errors race from the network,
the control connection, and shutdown. The connection owns its stream registry,
so removing a dead connection also provides a deterministic cleanup boundary.

Writers are serialized per control connection. This prevents concurrent frame
writes from interleaving. Message sizes are checked before allocation and again
when decoding, and stream queues have fixed capacity.

## Shutdown

Signal handling starts coordinated cleanup. The server stops listeners, closes
the HTTP server, then closes client connections and streams. The client closes
its control connection and active local streams. Cleanup methods are safe to call
more than once.

Coordinated shutdown prevents leaked goroutines and half-open listeners. It does
not drain HTTP requests or preserve active application sessions; shutdown or
connection loss can end them immediately.

## Trust boundaries

The control listener receives untrusted framed input and therefore enforces
message-size, authentication-time, and unauthenticated-connection limits. TLS is
the confidentiality and server-authentication boundary when enabled.

The web listener receives untrusted HTTP input and applies session bounds, login
throttling, origin checks, security headers, and strict method handling. It serves
HTTP itself, so external remote access should terminate HTTPS at a loopback proxy.

The local target address in a tunnel definition is trusted administrator
configuration. A compromised administrator can direct a connected client toward
services reachable from that client.

## Persistence

Configuration saves use a temporary file followed by rename, preventing a partial
write from replacing the last complete file. Permissions are restricted to the
owner. In-memory client, stream, traffic, log, and runtime metric state is
ephemeral and is rebuilt after restart.

## Scalability boundary

All coordination is local to one server process. This keeps the implementation
small and understandable but prevents transparent failover or horizontal scaling.
A future clustered design would need shared configuration, ownership/lease rules
for public ports and machine names, protocol version negotiation, and a deliberate
strategy for reconnect routing. Active TCP session migration would be a separate,
substantially harder feature.
