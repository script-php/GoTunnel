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
	stream.StartWriter(time.Second, func(error) { c.closeStream(1) })
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
	deadline := time.Now().Add(time.Second)
	for !stream.IsClosed() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !stream.IsClosed() || len(c.streams) != 0 {
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

func TestStopIsIdempotent(t *testing.T) {
	c := NewClient("127.0.0.1:1", "machine", "password")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestSendRejectsReplacedConnectionGeneration(t *testing.T) {
	current, currentPeer := net.Pipe()
	defer current.Close()
	defer currentPeer.Close()
	stale, stalePeer := net.Pipe()
	defer stale.Close()
	defer stalePeer.Close()
	c := NewClient("server", "machine", "password")
	c.conn = current
	c.connected = true

	err := c.sendMessageOn(stale, &protocol.Message{Type: protocol.MessageTypePing})
	if err == nil {
		t.Fatal("stale connection generation accepted a message")
	}
}

func TestReconnectDelayBacksOffAndCapsWithJitter(t *testing.T) {
	base := 5 * time.Second
	for failures, upper := range []time.Duration{
		5 * time.Second,
		10 * time.Second,
		20 * time.Second,
		40 * time.Second,
		60 * time.Second,
		60 * time.Second,
		60 * time.Second,
	} {
		lower := upper - upper/5
		for i := 0; i < 20; i++ {
			got := reconnectDelay(base, failures)
			if got < lower || got > upper {
				t.Fatalf("failures=%d: delay %v outside [%v, %v]", failures, got, lower, upper)
			}
		}
	}
}
