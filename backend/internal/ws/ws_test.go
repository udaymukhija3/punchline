package ws

import (
	"bufio"
	"encoding/base64"
	"net"
	"strings"
	"testing"
)

func TestReadJSONRejectsUnmaskedClientFrames(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	conn := &Conn{netConn: server, br: bufio.NewReader(server)}

	go func() { _, _ = client.Write([]byte{0x81, 0x02}) }()
	var payload any
	err := conn.ReadJSON(&payload)
	if err == nil || !strings.Contains(err.Error(), "must be masked") {
		t.Fatalf("err = %v, want unmasked-frame error", err)
	}
}

func TestReadJSONRejectsUnsupportedExtensions(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	conn := &Conn{netConn: server, br: bufio.NewReader(server)}

	go func() { _, _ = client.Write([]byte{0xC1, 0x80}) }()
	var payload any
	err := conn.ReadJSON(&payload)
	if err == nil || !strings.Contains(err.Error(), "extensions") {
		t.Fatalf("err = %v, want extension error", err)
	}
}

func TestReadJSONRejectsNewDataFrameBeforeFragmentCompletes(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	conn := &Conn{netConn: server, br: bufio.NewReader(server)}

	go func() {
		_, _ = client.Write(maskedFrame(false, opText, []byte(`{"type"`)))
		_, _ = client.Write(maskedFrame(true, opText, []byte(`:"x"}`)))
	}()
	var payload any
	err := conn.ReadJSON(&payload)
	if err == nil || !strings.Contains(err.Error(), "fragmented message") {
		t.Fatalf("err = %v, want fragmented-message error", err)
	}
}

func TestWriteJSONClosesWhenSendQueueIsFull(t *testing.T) {
	conn := &Conn{
		send: make(chan frame, 1),
		done: make(chan struct{}),
	}
	conn.send <- frame{opcode: opText, payload: []byte(`{"type":"old"}`)}

	err := conn.WriteJSON(map[string]string{"type": "room_state"})
	if err != errConnClosed {
		t.Fatalf("err = %v, want errConnClosed", err)
	}
	select {
	case <-conn.done:
	default:
		t.Fatal("full send queue did not close the connection")
	}
}

func TestValidClientKeyRequiresSixteenDecodedBytes(t *testing.T) {
	valid := base64.StdEncoding.EncodeToString([]byte("1234567890abcdef"))
	if !validClientKey(valid) {
		t.Fatal("valid websocket key was rejected")
	}
	if validClientKey("not-base64") {
		t.Fatal("invalid base64 key was accepted")
	}
	if validClientKey(base64.StdEncoding.EncodeToString([]byte("too-short"))) {
		t.Fatal("wrong-length key was accepted")
	}
}

func maskedFrame(fin bool, opcode byte, payload []byte) []byte {
	first := opcode
	if fin {
		first |= 0x80
	}
	mask := [4]byte{1, 2, 3, 4}
	out := []byte{first, 0x80 | byte(len(payload)), mask[0], mask[1], mask[2], mask[3]}
	for i, b := range payload {
		out = append(out, b^mask[i%len(mask)])
	}
	return out
}
