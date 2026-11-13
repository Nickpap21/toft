// Package wspty bridges a browser terminal's WebSocket and a PTY: a
// server's over SSH, or the panel's own host (D-68).
//
// Client → server, text JSON: {"type":"input","data":"…"} for keystrokes and
// pastes, {"type":"resize","cols":N,"rows":N}. Server → client: the PTY's
// output as binary messages (xterm.js handles ANSI), or
// {"type":"error","data":"…"} as text when it could not open.
package wspty

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"
)

// ErrStopping is the cause every request is ended with when the backend
// stops (cmd/server ends them through the HTTP server's BaseContext), so a
// shell can tell a restart from a closed tab.
var ErrStopping = errors.New("the backend is stopping")

// Stopping reports whether ctx ended because the backend is stopping.
func Stopping(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), ErrStopping)
}

// PTY is a terminal on a host.
type PTY interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Resize(cols, rows int) error
}

// Upgrader accepts the API's own origin (the panel's page: the same host as
// the request, D-62) and origins (the same list as CORS). A request without
// an Origin header (a CLI) is accepted: it has no browser to protect.
func Upgrader(origins func() []string) websocket.Upgrader {
	return websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		if u, err := url.Parse(origin); err == nil && strings.EqualFold(u.Host, r.Host) {
			return true
		}
		for _, o := range origins() {
			if origin == o {
				return true
			}
		}
		return false
	}}
}

// Size is the terminal's ?cols= and ?rows=, 120 by 30 when unset.
func Size(r *http.Request) (cols, rows int) {
	cols, rows = 120, 30
	if v, err := strconv.Atoi(r.URL.Query().Get("cols")); err == nil && v > 0 {
		cols = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("rows")); err == nil && v > 0 {
		rows = v
	}
	return cols, rows
}

// Fail tells the client why the terminal could not open.
func Fail(conn *websocket.Conn, msg string) {
	b, _ := json.Marshal(map[string]string{"type": "error", "data": msg})
	_ = conn.WriteMessage(websocket.TextMessage, b)
}

type clientMsg struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// Serve copies between conn and p until either side ends or ctx does. It
// closes conn when ctx ends (the session was revoked, say), which unblocks
// the read; the caller closes p.
func Serve(ctx context.Context, conn *websocket.Conn, p PTY) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	// The PTY's output → the browser. It ends with the PTY.
	go func() {
		defer cancel()
		buf := make([]byte, 32*1024)
		for {
			n, err := p.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// The browser → the PTY.
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg clientMsg
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "input":
			if _, err := p.Write([]byte(msg.Data)); err != nil {
				return
			}
		case "resize":
			if msg.Cols > 0 && msg.Rows > 0 {
				_ = p.Resize(msg.Cols, msg.Rows)
			}
		}
	}
}
