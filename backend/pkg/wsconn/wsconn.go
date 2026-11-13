// Package wsconn makes a WebSocket a net.Conn carrying a byte stream in
// binary messages, so SSH can run inside it (D-106). Both toft-agent and the
// panel use it.
package wsconn

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Conn is a net.Conn over a WebSocket. Reads and writes may run
// concurrently with each other; gorilla allows one reader and one writer.
type Conn struct {
	ws *websocket.Conn

	rmu sync.Mutex
	r   io.Reader

	wmu sync.Mutex
}

// New wraps ws. Control frames (ping, pong, close) are handled by gorilla
// while Read runs.
func New(ws *websocket.Conn) *Conn { return &Conn{ws: ws} }

func (c *Conn) Read(p []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	for {
		if c.r == nil {
			typ, r, err := c.ws.NextReader()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					return 0, io.EOF
				}
				return 0, err
			}
			if typ != websocket.BinaryMessage {
				continue
			}
			c.r = r
		}
		n, err := c.r.Read(p)
		if err == io.EOF {
			c.r = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *Conn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Ping sends a ping frame. gorilla allows WriteControl and Close beside a
// writer, so neither waits for a Write stuck on a dead link: Close is what
// unblocks it.
func (c *Conn) Ping(timeout time.Duration) error {
	return c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(timeout))
}

func (c *Conn) Close() error {
	_ = c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	return c.ws.Close()
}

func (c *Conn) LocalAddr() net.Addr  { return c.ws.LocalAddr() }
func (c *Conn) RemoteAddr() net.Addr { return c.ws.RemoteAddr() }

func (c *Conn) SetDeadline(t time.Time) error {
	if err := c.ws.SetReadDeadline(t); err != nil {
		return err
	}
	return c.ws.SetWriteDeadline(t)
}
func (c *Conn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }
