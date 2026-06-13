package ws

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const magicGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Keepalive tuning. The server pings clients periodically; browsers answer
// with a pong automatically, which refreshes the read deadline. A client that
// goes silent past readTimeout is treated as disconnected.
const (
	PingInterval = 25 * time.Second
	readTimeout  = 70 * time.Second
	writeTimeout = 10 * time.Second
)

// sendQueue bounds how many frames may be buffered for a single client before
// it is treated as a slow consumer and dropped. This keeps one stalled socket
// from blocking broadcasts to everyone else and bounds per-connection memory.
const sendQueue = 64

// maxClientMessageBytes is deliberately small: clients only send compact JSON
// commands such as submit_answer and pick_winner. Larger payloads are invalid
// and treated as abusive.
const maxClientMessageBytes = 8 << 10

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

var errConnClosed = errors.New("websocket connection closed")

type frame struct {
	opcode  byte
	payload []byte
}

// Conn is a minimal server-side WebSocket connection. All writes funnel through
// a single writer goroutine (the write pump) fed by a buffered channel, so the
// reader and any number of broadcasters never block on socket I/O and never
// race on the wire.
type Conn struct {
	netConn   net.Conn
	br        *bufio.Reader
	send      chan frame
	done      chan struct{}
	closeOnce sync.Once
}

func Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if r.Method != http.MethodGet {
		return nil, errors.New("websocket upgrade must use GET")
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return nil, errors.New("missing websocket upgrade header")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		return nil, errors.New("unsupported websocket version")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, errors.New("missing websocket key")
	}
	h, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("response writer does not support hijacking")
	}
	nc, rw, err := h.Hijack()
	if err != nil {
		return nil, err
	}
	accept := acceptKey(key)
	_, err = fmt.Fprintf(nc, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept)
	if err != nil {
		_ = nc.Close()
		return nil, err
	}
	c := &Conn{
		netConn: nc,
		br:      rw.Reader,
		send:    make(chan frame, sendQueue),
		done:    make(chan struct{}),
	}
	go c.writePump()
	return c, nil
}

// Close signals the write pump to flush and shut down. Safe to call repeatedly
// and from multiple goroutines.
func (c *Conn) Close() error {
	c.closeInternal()
	return nil
}

func (c *Conn) closeInternal() {
	c.closeOnce.Do(func() { close(c.done) })
}

// writePump is the only goroutine that writes to the socket. It serialises
// application frames, emits keepalive pings, and on shutdown drains buffered
// frames best-effort before sending a close frame.
func (c *Conn) writePump() {
	ticker := time.NewTicker(PingInterval)
	defer ticker.Stop()
	defer c.netConn.Close()

	for {
		select {
		case <-c.done:
			c.drain()
			_ = c.writeFrame(opClose, nil)
			return
		case f := <-c.send:
			if err := c.writeFrame(f.opcode, f.payload); err != nil {
				c.closeInternal()
				return
			}
		case <-ticker.C:
			if err := c.writeFrame(opPing, nil); err != nil {
				c.closeInternal()
				return
			}
		}
	}
}

func (c *Conn) drain() {
	for {
		select {
		case f := <-c.send:
			_ = c.writeFrame(f.opcode, f.payload)
		default:
			return
		}
	}
}

// enqueue hands a frame to the write pump without blocking. If the buffer is
// full the peer is too slow; we drop the connection rather than stall the room.
func (c *Conn) enqueue(f frame) error {
	select {
	case <-c.done:
		return errConnClosed
	default:
	}
	select {
	case c.send <- f:
		return nil
	case <-c.done:
		return errConnClosed
	default:
		c.closeInternal()
		return errConnClosed
	}
}

func (c *Conn) WriteJSON(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.enqueue(frame{opcode: opText, payload: payload})
}

func (c *Conn) ReadJSON(v any) error {
	payload, err := c.readMessage()
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, v)
}

// readMessage returns the next application (text/binary) message, transparently
// handling control frames (ping/pong/close) and fragmentation.
func (c *Conn) readMessage() ([]byte, error) {
	var data []byte
	fragmented := false
	for {
		if err := c.netConn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			return nil, err
		}
		opcode, payload, fin, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case opClose:
			return nil, io.EOF
		case opPing:
			// Answer via the write pump so we never write from two goroutines.
			_ = c.enqueue(frame{opcode: opPong, payload: payload})
		case opPong:
			// keepalive acknowledgement; read deadline already refreshed
		case opText, opBinary:
			if len(data)+len(payload) > maxClientMessageBytes {
				return nil, errors.New("websocket message too large")
			}
			data = append(data, payload...)
			if fin {
				return data, nil
			}
			fragmented = true
		case opContinuation:
			if !fragmented {
				return nil, errors.New("unexpected continuation frame")
			}
			if len(data)+len(payload) > maxClientMessageBytes {
				return nil, errors.New("websocket message too large")
			}
			data = append(data, payload...)
			if fin {
				return data, nil
			}
		default:
			return nil, fmt.Errorf("unsupported websocket opcode %d", opcode)
		}
	}
}

func (c *Conn) readFrame() (opcode byte, payload []byte, fin bool, err error) {
	head := make([]byte, 2)
	if _, err = io.ReadFull(c.br, head); err != nil {
		return 0, nil, false, err
	}
	fin = head[0]&0x80 != 0
	opcode = head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := uint64(head[1] & 0x7F)
	if length == 126 {
		var b [2]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return 0, nil, false, err
		}
		length = uint64(binary.BigEndian.Uint16(b[:]))
	} else if length == 127 {
		var b [8]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return 0, nil, false, err
		}
		length = binary.BigEndian.Uint64(b[:])
	}
	if length > maxClientMessageBytes {
		return 0, nil, false, errors.New("websocket frame too large")
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, mask[:]); err != nil {
			return 0, nil, false, err
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return 0, nil, false, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, fin, nil
}

// writeFrame writes a single, unfragmented frame in one syscall. Only the write
// pump calls this, so no locking is required. Control-frame payloads are capped
// at 125 bytes per the spec.
func (c *Conn) writeFrame(opcode byte, payload []byte) error {
	if opcode == opPing || opcode == opPong || opcode == opClose {
		if len(payload) > 125 {
			payload = payload[:125]
		}
	}
	if err := c.netConn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}

	buf := make([]byte, 0, len(payload)+10)
	buf = append(buf, 0x80|opcode)
	l := len(payload)
	switch {
	case l < 126:
		buf = append(buf, byte(l))
	case l <= 65535:
		buf = append(buf, 126, byte(l>>8), byte(l))
	default:
		buf = append(buf, 127)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(l))
		buf = append(buf, b[:]...)
	}
	buf = append(buf, payload...)
	_, err := c.netConn.Write(buf)
	return err
}

func acceptKey(key string) string {
	h := sha1.Sum([]byte(key + magicGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}
