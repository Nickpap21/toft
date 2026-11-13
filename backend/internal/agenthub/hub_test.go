package agenthub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/Nickpap21/toft/backend/pkg/sshclient"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"github.com/Nickpap21/toft/backend/pkg/wsconn"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

type admitAll struct{ connected chan string }

func (a admitAll) AdmitAgent(_ context.Context, h Hello) (string, error) {
	if h.ServerID == "revoked" {
		return "", ErrRevoked
	}
	return h.ServerID, nil
}
func (a admitAll) AgentConnected(id string, _ Hello) { a.connected <- id }
func (a admitAll) AgentSeen(string)                  {}

// fakeAgent dials the hub and serves a tiny SSH server inside the
// WebSocket: exec answers "ran: <cmd>".
func fakeAgent(t *testing.T, url, serverID string, hostKey ssh.Signer) *websocket.Conn {
	t.Helper()
	h := http.Header{}
	h.Set(agentproto.HeaderServer, serverID)
	ws, resp, err := websocket.DefaultDialer.Dial(url, h)
	if err != nil {
		t.Fatalf("dial: %v (%v)", err, resp)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: false, PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }}
	cfg.AddHostKey(hostKey)
	go func() {
		_, chans, reqs, err := ssh.NewServerConn(wsconn.New(ws), cfg)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(reqs)
		for nch := range chans {
			ch, creqs, _ := nch.Accept()
			go func() {
				for r := range creqs {
					if r.Type == "exec" {
						_ = r.Reply(true, nil)
						var e struct{ Command string }
						_ = ssh.Unmarshal(r.Payload, &e)
						_, _ = ch.Write([]byte("ran: " + e.Command))
						b := make([]byte, 4)
						binary.BigEndian.PutUint32(b, 0)
						_, _ = ch.SendRequest("exit-status", false, b)
						_ = ch.Close()
					}
				}
			}()
		}
	}()
	return ws
}

func signer(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, _ := ssh.NewSignerFromKey(priv)
	block, _ := ssh.MarshalPrivateKey(priv, "")
	return s, string(pem.EncodeToMemory(block))
}

func TestSSHRunsInsideTheAgentsWebSocket(t *testing.T) {
	adm := admitAll{connected: make(chan string, 4)}
	hub := New(adm, zap.NewNop())
	ts := httptest.NewServer(serve(hub))
	defer ts.Close()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")

	agentKey, _ := signer(t)
	_, panelPEM := signer(t)
	fakeAgent(t, url, "s1", agentKey)
	<-adm.connected

	pool := sshpool.New(sshpool.Options{})
	defer pool.Close()
	h := pool.Handle(sshpool.Target{
		ServerID: "s1", Name: "s1", User: "toft", PrivateKeyPEM: panelPEM,
		PinnedHostKey: sshclient.MarshalHostKey(agentKey.PublicKey()),
		Conn:          func(ctx context.Context) (net.Conn, error) { return hub.Conn(ctx, "s1") },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := range 20 { // many sessions on the one connection
		out, err := h.Exec(ctx, "docker ps")
		if err != nil || out != "ran: docker ps" {
			t.Fatalf("exec %d: %q %v", i, out, err)
		}
	}
	if st, ok := hub.State("s1"); !ok || !st.Connected {
		t.Fatalf("state: %+v %v", st, ok)
	}

	// A live connection that answers keeps its place: a second one for the
	// same server, which has proved nothing yet, is turned away.
	hdr := http.Header{}
	hdr.Set(agentproto.HeaderServer, "s1")
	if _, resp, err := websocket.DefaultDialer.Dial(url, hdr); err == nil || resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("second connection: %v %v", err, resp)
	}
	if _, err := h.Exec(ctx, "id"); err != nil {
		t.Fatalf("the live agent was displaced: %v", err)
	}

	// Another agent key than the pinned one fails the handshake.
	other, _ := signer(t)
	pool.Invalidate("s1")
	hub.Drop("s1")
	fakeAgent(t, url, "s1", other)
	<-adm.connected
	if _, err := h.Exec(ctx, "id"); err == nil {
		t.Fatal("an agent presenting another key must be refused")
	}
}

func TestConnWaitsForAReconnectingAgentAndFailsForAnOfflineOne(t *testing.T) {
	adm := admitAll{connected: make(chan string, 4)}
	hub := New(adm, zap.NewNop())
	ts := httptest.NewServer(serve(hub))
	defer ts.Close()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Long after the panel started, a server never heard from is offline.
	hub.started = time.Now().Add(-time.Hour)
	if _, err := hub.Conn(context.Background(), "s9"); err != ErrOffline {
		t.Fatalf("offline: %v", err)
	}

	// Just after the panel started, Conn waits for the agent.
	hub.started = time.Now()
	key, _ := signer(t)
	got := make(chan error, 1)
	go func() {
		_, err := hub.Conn(context.Background(), "s2")
		got <- err
	}()
	time.Sleep(100 * time.Millisecond)
	fakeAgent(t, url, "s2", key)
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Conn did not return when the agent connected")
	}
}

func TestRevokedAgentIsToldSo(t *testing.T) {
	hub := New(admitAll{connected: make(chan string, 1)}, zap.NewNop())
	ts := httptest.NewServer(serve(hub))
	defer ts.Close()
	h := http.Header{}
	h.Set(agentproto.HeaderServer, "revoked")
	_, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), h)
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden || resp.Header.Get("X-Toft-Error") != "agent_revoked" {
		t.Fatalf("want 403 agent_revoked, got %v %v", resp, err)
	}
}

func serve(hub *Hub) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hub.Connect(w, r, "203.0.113.1") })
}
