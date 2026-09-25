package tunnel

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestConcurrentConnectionAccessAndClose(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	stream := NewStream(1, conn, nil)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				stream.Connection()
				stream.IsClosed()
			}
		}()
		go func() {
			defer wg.Done()
			stream.Close()
		}()
	}
	wg.Wait()
	if !stream.IsClosed() {
		t.Fatal("stream remains open")
	}
}

func TestHalfCloseIsOrderedAfterQueuedData(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	stream := NewStream(1, server, nil)
	defer stream.Close()
	stream.StartWriter(time.Second, func(err error) { t.Errorf("writer error: %v", err) })
	if !stream.Enqueue([]byte("final bytes")) || !stream.EnqueueHalfClose() {
		t.Fatal("failed to enqueue data and half-close")
	}
	got, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "final bytes" {
		t.Fatalf("received %q", got)
	}
}

func TestInboundQueueIsBounded(t *testing.T) {
	stream := NewStream(1, nil, nil)
	for i := 0; i < InboundQueueSize; i++ {
		if !stream.Enqueue([]byte{byte(i)}) {
			t.Fatalf("queue rejected item %d", i)
		}
	}
	if stream.Enqueue([]byte("overflow")) {
		t.Fatal("queue accepted data beyond its bound")
	}
}
