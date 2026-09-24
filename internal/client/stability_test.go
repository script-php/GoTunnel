package client

import (
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
	c := &Client{streams: map[uint32]*tunnel.Stream{1: stream}}
	done := make(chan struct{})
	go func() {
		c.handleIncomingStreamData(protocol.MessageStreamData{StreamID: 1, Data: []byte("hello")})
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

func TestSetReconnectInterval(t *testing.T) {
	c := NewClient("server", "machine", "password")
	if err := c.SetReconnectInterval(250 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if c.reconnectInterval != 250*time.Millisecond {
		t.Fatalf("reconnect interval = %v", c.reconnectInterval)
	}
	if err := c.SetReconnectInterval(0); err == nil {
		t.Fatal("accepted zero reconnect interval")
	}
}
