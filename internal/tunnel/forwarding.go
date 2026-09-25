package tunnel

import (
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

const InboundQueueSize = 50

type inboundMessage struct {
	data      []byte
	halfClose bool
}

// Stream represents a bidirectional data stream
type Stream struct {
	ID         uint32
	conn1      net.Conn
	conn2      net.Conn
	mu         sync.Mutex
	closed     bool
	closedCh   chan struct{}
	readyCh    chan struct{}
	ready      bool
	inbound    chan inboundMessage
	writerOnce sync.Once
	localEOF   bool
	remoteEOF  bool
}

// NewStream creates a new stream between two connections
func NewStream(id uint32, conn1, conn2 net.Conn) *Stream {
	return &Stream{
		ID:       id,
		conn1:    conn1,
		conn2:    conn2,
		closedCh: make(chan struct{}),
		readyCh:  make(chan struct{}),
		inbound:  make(chan inboundMessage, InboundQueueSize),
	}
}

// Forward starts bidirectional data forwarding
func (s *Stream) Forward() {
	conn1, conn2 := s.Connections()
	if conn1 == nil || conn2 == nil {
		s.Close()
		return
	}
	var wg sync.WaitGroup

	wg.Add(2)

	// Conn1 -> Conn2
	go func() {
		defer wg.Done()
		if err := s.copyData(conn1, conn2, "conn1->conn2"); err != nil {
			log.Printf("Stream %d error (conn1->conn2): %v", s.ID, err)
		}
	}()

	// Conn2 -> Conn1
	go func() {
		defer wg.Done()
		if err := s.copyData(conn2, conn1, "conn2->conn1"); err != nil {
			log.Printf("Stream %d error (conn2->conn1): %v", s.ID, err)
		}
	}()

	wg.Wait()
	s.Close()
}

// copyData copies data from src to dst
func (s *Stream) copyData(src, dst net.Conn, direction string) error {
	// Don't close connections here - Forward() will close them after both goroutines finish
	// This prevents double-close race condition
	buf := make([]byte, 32*1024) // 32KB buffer
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				return fmt.Errorf("write error: %w", err)
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read error: %w", err)
		}
	}
}

// Close closes the stream and both connections
func (s *Stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	conn1, conn2 := s.conn1, s.conn2
	close(s.closedCh)
	s.mu.Unlock()

	// net.Conn permits Close concurrently with reads and writes. Keeping the
	// pointers immutable lets Close interrupt blocked I/O without a data race.
	if conn1 != nil {
		conn1.Close()
	}
	if conn2 != nil {
		conn2.Close()
	}
	return nil
}

// IsClosed returns whether the stream is closed
func (s *Stream) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Connection returns the primary connection and whether the stream is open.
func (s *Stream) Connection() (net.Conn, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn1, !s.closed && s.conn1 != nil
}

// Connections returns the immutable connection pair.
func (s *Stream) Connections() (net.Conn, net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn1, s.conn2
}

// Ready returns a channel closed when the peer confirms stream readiness.
func (s *Stream) Ready() <-chan struct{} {
	return s.readyCh
}

// SignalReady marks the stream ready once. It returns false for closed or
// already-ready streams.
func (s *Stream) SignalReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ready {
		return false
	}
	s.ready = true
	close(s.readyCh)
	return true
}

func (s *Stream) Enqueue(data []byte) bool {
	if s.IsClosed() {
		return false
	}
	copyOfData := append([]byte(nil), data...)
	select {
	case s.inbound <- inboundMessage{data: copyOfData}:
		return true
	default:
		return false
	}
}

func (s *Stream) EnqueueHalfClose() bool {
	if s.IsClosed() {
		return false
	}
	select {
	case s.inbound <- inboundMessage{halfClose: true}:
		return true
	default:
		return false
	}
}

func (s *Stream) StartWriter(timeout time.Duration, onError func(error), onHalfClose ...func(bool, error)) {
	s.writerOnce.Do(func() {
		go func() {
			for {
				select {
				case <-s.closedCh:
					return
				case message := <-s.inbound:
					conn, open := s.Connection()
					if !open {
						return
					}
					if message.halfClose {
						closer, ok := conn.(interface{ CloseWrite() error })
						var err error
						if ok {
							err = closer.CloseWrite()
						} else {
							err = fmt.Errorf("connection does not support half-close")
						}
						complete := s.markRemoteEOF()
						if len(onHalfClose) > 0 {
							onHalfClose[0](complete, err)
						}
						if complete || err != nil {
							return
						}
						continue
					}
					if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
						onError(err)
						return
					}
					for len(message.data) > 0 {
						n, err := conn.Write(message.data)
						if err != nil {
							onError(err)
							return
						}
						if n == 0 {
							onError(io.ErrNoProgress)
							return
						}
						message.data = message.data[n:]
					}
				}
			}
		}()
	})
}

func (s *Stream) MarkLocalEOF() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.localEOF = true
	return s.remoteEOF
}

func (s *Stream) markRemoteEOF() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remoteEOF = true
	return s.localEOF
}
