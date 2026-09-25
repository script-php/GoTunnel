package server

import (
	"github.com/yoyo/gotunnel/internal/config"
	"github.com/yoyo/gotunnel/internal/protocol"
	"github.com/yoyo/gotunnel/internal/tunnel"
	"net"
	"testing"
	"time"
)

func TestStreamWriteFailureDoesNotDeadlock(t *testing.T) {
	local, peer := net.Pipe()
	defer local.Close()
	peer.Close()
	stream := tunnel.NewStream(1, local, nil)
	c := &ClientConnection{streams: map[uint32]*tunnel.Stream{1: stream}}
	done := make(chan struct{})
	go func() {
		c.handleStreamData(protocol.MessageStreamData{StreamID: 1, Data: []byte("hello")})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("write failure deadlocked cleanup")
	}
	if !stream.IsClosed() {
		t.Fatal("failed stream remains open")
	}
	if len(c.streams) != 0 {
		t.Fatal("failed stream remains registered")
	}
}

func TestOldClientCleanupPreservesReplacement(t *testing.T) {
	old := &ClientConnection{MachineID: "machine"}
	replacement := &ClientConnection{MachineID: "machine"}
	s := &Server{clients: map[string]*ClientConnection{"machine": replacement}}
	s.RemoveClient(old)
	if s.GetClientConnection("machine") != replacement {
		t.Fatal("old connection removed its replacement")
	}
	s.RemoveClient(replacement)
	if s.GetClientConnection("machine") != nil {
		t.Fatal("current connection was not removed")
	}
}

type closedListener struct{ calls int }

func (l *closedListener) Accept() (net.Conn, error) {
	l.calls++
	if l.calls > 1 {
		panic("retried a closed listener")
	}
	return nil, net.ErrClosed
}
func (*closedListener) Close() error   { return nil }
func (*closedListener) Addr() net.Addr { return nil }

func TestClosedTunnelListenerExits(t *testing.T) {
	s := &Server{stopCh: make(chan struct{})}
	listener := &closedListener{}
	s.acceptTunnelConnections(1234, listener)
	if listener.calls != 1 {
		t.Fatalf("Accept called %d times", listener.calls)
	}
}

func newTunnelTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.NewManager("")
	cfg.InitDefault()
	if err := cfg.AddTunnelPorts("machine", 8000, 80); err != nil {
		t.Fatal(err)
	}
	return NewServer(cfg)
}

func TestStreamIsRemovedWhenOpenMessageFails(t *testing.T) {
	s := newTunnelTestServer(t)
	control, controlPeer := net.Pipe()
	controlPeer.Close()
	cc := NewClientConnection(control, s)
	cc.MachineID = "machine"
	external, externalPeer := net.Pipe()
	defer externalPeer.Close()

	cc.HandleIncomingConnection(8000, external)
	cc.streamsMu.RLock()
	count := len(cc.streams)
	cc.streamsMu.RUnlock()
	if count != 0 {
		t.Fatalf("failed stream remains registered: %d streams", count)
	}
}

func TestStreamLimitIsEnforcedDuringReservation(t *testing.T) {
	s := newTunnelTestServer(t)
	control, controlPeer := net.Pipe()
	defer control.Close()
	defer controlPeer.Close()
	cc := NewClientConnection(control, s)
	cc.MachineID = "machine"
	for i := uint32(1); i <= MaxStreamsPerClient; i++ {
		cc.streams[i] = tunnel.NewStream(i, nil, nil)
	}
	external, externalPeer := net.Pipe()
	defer externalPeer.Close()

	cc.HandleIncomingConnection(8000, external)
	cc.streamsMu.RLock()
	count := len(cc.streams)
	cc.streamsMu.RUnlock()
	if count != MaxStreamsPerClient {
		t.Fatalf("stream count = %d, want %d", count, MaxStreamsPerClient)
	}
}

func TestServerStopIsIdempotent(t *testing.T) {
	s := newTunnelTestServer(t)
	close(s.doneCh) // Simulate an accept loop that has already exited.
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestLoggerCloseIsIdempotent(t *testing.T) {
	l := NewLogger("", 10)
	l.Add("INFO", "before close")
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l.Add("INFO", "after close")
	if got := l.Count(); got != 1 {
		t.Fatalf("log count after close = %d, want 1", got)
	}
}
