package protocol

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func TestReadRejectsOversizedHeaderBeforeReadingPayload(t *testing.T) {
	for _, size := range []uint32{MaxMessageSize + 1, 0xffffffff} {
		header := []byte{byte(MessageTypeStreamData), 0, 0, 0, 0}
		binary.BigEndian.PutUint32(header[1:], size)
		_, err := Read(bytes.NewReader(header))
		if err == nil || err == io.EOF || err == io.ErrUnexpectedEOF {
			t.Fatalf("length %d: expected size rejection before payload read, got %v", size, err)
		}
	}
}

func TestReadFramesAndTruncation(t *testing.T) {
	frame, err := Encode(&Message{Type: MessageTypeStreamData, Payload: MessageStreamData{StreamID: 7, Data: []byte("hello")}})
	if err != nil {
		t.Fatal(err)
	}
	reader := bytes.NewReader(append(append([]byte{}, frame...), frame...))
	for i := 0; i < 2; i++ {
		msg, err := Read(reader)
		if err != nil {
			t.Fatal(err)
		}
		payload := msg.Payload.(MessageStreamData)
		if payload.StreamID != 7 || string(payload.Data) != "hello" {
			t.Fatalf("wrong payload: %v", payload)
		}
	}
	for n := 0; n < len(frame); n++ {
		if _, err := Read(bytes.NewReader(frame[:n])); err == nil {
			t.Fatalf("accepted truncated frame of %d bytes", n)
		}
	}
}

func TestHalfCloseRoundTrip(t *testing.T) {
	frame, err := Encode(&Message{Type: MessageTypeStreamClose, Payload: MessageStreamClose{StreamID: 9, HalfClose: true}})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := Read(bytes.NewReader(frame))
	if err != nil {
		t.Fatal(err)
	}
	payload := msg.Payload.(MessageStreamClose)
	if payload.StreamID != 9 || !payload.HalfClose {
		t.Fatalf("wrong half-close payload: %#v", payload)
	}
}
