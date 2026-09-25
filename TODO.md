# Stability work tracker

## First stabilization pass
- [x] Fix stream write-error deadlocks on client and server; add regression tests.
- [x] Stop tunnel accept loops when a listener closes; add regression test.
- [x] Preserve replacement clients during old-connection cleanup; add regression test.
- [x] Fix logging vet errors and run tests with the race detector.

## Connection safety
- [x] Validate frame lengths before allocation; add malformed-frame tests.
- [x] Add authentication deadlines and pending-connection limits.
- [x] Register streams before STREAM_OPEN; enforce stream limits atomically.
- [ ] Eliminate stream connection-field races during closure.
- [ ] Make reader workers cancellable and bound to one connection generation.
- [x] Make shutdown idempotent; stop HTTP, flush logger, and wait for workers.
- [ ] Isolate slow streams with bounded queues and bounded concurrent dialing.
- [ ] Correct heartbeat timeout behavior and add reconnect backoff with jitter.
- [ ] Preserve bytes returned alongside read errors; support half-close semantics.

## Configuration and security
- [x] Make tunnel changes transactional and reject duplicate remote-port ownership.
- [x] Validate loaded configuration, initialize missing maps, and return deep copies.
- [x] Roll back failed persistence and improve configuration file durability/permissions.
- [ ] Add encrypted tunnel transport and separate client/admin credentials.
- [ ] Add configurable panel bind address, HTTP limits, and trusted-proxy rate limiting.
- [ ] Remove unsafe frontend interpolation of machine IDs; bound session/rate-limit storage.

## Operations
- [x] Add CI for tests, race detection, vet, and builds.
- [ ] Add integration tests for reconnects, slow services, concurrent traffic, and shutdown.
- [ ] Improve log rotation/error reporting and expose health/resource metrics.
- [ ] Fix systemd argument quoting, working directory, and least-privilege execution.
- [x] Implement or remove ignored CLI flags; align README with verified behavior.

## Verification
- First pass: `go test -race ./...` passes, including stream write failures, replacement-client cleanup, closed listeners, oversized frames, and truncated frames.
- Configuration loading now rejects invalid/duplicate ports, initializes omitted maps, and isolates stored state from caller mutation.
- The reconnect interval flag is active; the unimplemented machine-ID file flag was removed from the CLI.
- Client and server authentication now have deadlines, and the server caps pending handshakes at 64.
- Streams are reserved before `STREAM_OPEN`, with atomic limit enforcement and failed-send cleanup.
- GitHub Actions now checks formatting, vet, race tests, and builds on pushes and pull requests.
- Tunnel mutations validate exclusive ownership and roll back configuration/listener failures.
- Shutdown is idempotent and now stops HTTP, flushes logs, and waits for client workers.
- Config mutations roll back on save failure; writes are synced, atomically renamed, and stored with `0600` permissions.
- Remaining unchecked work is not yet implemented or verified.
