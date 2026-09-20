package tunnel

import (
	"fmt"
	"io"
	"log"
	"net"
	"sync"
)

// Stream represents a bidirectional data stream
type Stream struct {
	ID             uint32
	Conn1          net.Conn
	Conn2          net.Conn
	Closed         bool
	Mu             sync.Mutex
	closedCh       chan struct{}
	ReadyCh        chan struct{} // Signal when client is ready to receive data
	closeInitiated bool          // FIX #2: Track if close was already initiated to prevent race
}

// NewStream creates a new stream between two connections
func NewStream(id uint32, conn1, conn2 net.Conn) *Stream {
	return &Stream{
		ID:       id,
		Conn1:    conn1,
		Conn2:    conn2,
		closedCh: make(chan struct{}),
		ReadyCh:  make(chan struct{}), // Signal when client is ready
	}
}

// Forward starts bidirectional data forwarding
func (s *Stream) Forward() {
	var wg sync.WaitGroup

	wg.Add(2)

	// Conn1 -> Conn2
	go func() {
		defer wg.Done()
		if err := s.copyData(s.Conn1, s.Conn2, "conn1->conn2"); err != nil {
			log.Printf("Stream %d error (conn1->conn2): %v", s.ID, err)
		}
	}()

	// Conn2 -> Conn1
	go func() {
		defer wg.Done()
		if err := s.copyData(s.Conn2, s.Conn1, "conn2->conn1"); err != nil {
			log.Printf("Stream %d error (conn2->conn1): %v", s.ID, err)
		}
	}()

	wg.Wait()
	// Close both connections after both goroutines complete
	if s.Conn1 != nil {
		s.Conn1.Close()
	}
	if s.Conn2 != nil {
		s.Conn2.Close()
	}
	s.Close()
}

// copyData copies data from src to dst
func (s *Stream) copyData(src, dst net.Conn, direction string) error {
	// Don't close connections here - Forward() will close them after both goroutines finish
	// This prevents double-close race condition
	buf := make([]byte, 32*1024) // 32KB buffer
	for {
		n, err := src.Read(buf)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read error: %w", err)
		}

		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				return fmt.Errorf("write error: %w", err)
			}
		}
	}
}

// Close closes the stream and both connections
func (s *Stream) Close() error {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	// FIX #2: Check if close was already initiated to prevent double-close race
	if s.Closed || s.closeInitiated {
		return nil
	}

	s.closeInitiated = true
	s.Closed = true

	// Close both connections atomically
	if s.Conn1 != nil {
		s.Conn1.Close()
		s.Conn1 = nil
	}
	if s.Conn2 != nil {
		s.Conn2.Close()
		s.Conn2 = nil
	}

	// Signal closure
	select {
	case <-s.closedCh:
		// Already closed
	default:
		close(s.closedCh)
	}

	return nil
}

// IsClosed returns whether the stream is closed
func (s *Stream) IsClosed() bool {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.Closed
}
