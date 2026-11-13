package gitstore

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Egress is the backend's side of import and mirrors (D-79): the only way
// git in paas-git reaches another host. It serves SOCKS5 on a Unix socket
// both containers mount, accepting nothing but CONNECT egress.toft:80, and
// speaks plain HTTP on that stream: each request's first path segment is a
// token Open registered, mapped to one remote repository, its credentials
// and one git service. The request goes on to the remote over TLS through
// the transport given (public addresses only, in production), with the
// credentials added here, so git never holds them, never learns the real
// URL, and can reach nothing else. Pack data passes through unparsed.
type Egress struct {
	transport http.RoundTripper
	mu        sync.Mutex
	ops       map[string]*EgressOp
	conns     chan net.Conn
	done      chan struct{}
}

// EgressOp is one registered operation, and what the remote answered.
type EgressOp struct {
	base     *url.URL
	user     string
	password string
	service  string // git-upload-pack or git-receive-pack

	mu       sync.Mutex
	status   int    // the remote's last status
	location string // where a redirect pointed
	err      error  // why the remote couldn't be reached
}

// NewEgress forwards through transport: netcheck's public-only one in
// production, so a remote that resolves to a private address is refused
// at connect time.
func NewEgress(transport http.RoundTripper) *Egress {
	return &Egress{transport: transport, ops: map[string]*EgressOp{}, conns: make(chan net.Conn), done: make(chan struct{})}
}

// Serve listens on socket until ctx ends. Only uid 10001 (git) and root
// connect: the socket is theirs, mode 0600.
func (e *Egress) Serve(ctx context.Context, socket string) error {
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove old egress socket: %w", err)
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("egress: listen: %w", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("egress: chmod: %w", err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(socket, gitUID, gitUID); err != nil {
			ln.Close()
			return fmt.Errorf("egress: chown: %w", err)
		}
	}
	srv := &http.Server{Handler: e, ReadHeaderTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		close(e.done)
		ln.Close()
		_ = srv.Close()
	}()
	go func() { _ = srv.Serve(chanListener{e}) }()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("egress: accept: %w", err)
		}
		go e.handshake(c)
	}
}

// gitUID is the uid paas-git runs as (docker-compose.git.yml).
const gitUID = 10001

// Open registers an operation against remote (an https URL) with its
// credentials (user and password may be empty) for one git service, and
// returns the URL git is given and a function that ends it.
func (e *Egress) Open(remote, user, password, service string) (string, *EgressOp, func(), error) {
	base, err := url.Parse(strings.TrimSuffix(remote, "/"))
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil {
		return "", nil, nil, errors.New("egress: the remote must be an https URL with no user in it")
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", nil, nil, fmt.Errorf("egress: token: %w", err)
	}
	token := hex.EncodeToString(b)
	op := &EgressOp{base: base, user: user, password: password, service: service}
	e.mu.Lock()
	e.ops[token] = op
	e.mu.Unlock()
	end := func() {
		e.mu.Lock()
		delete(e.ops, token)
		e.mu.Unlock()
	}
	return "http://egress.toft/" + token + "/", op, end, nil
}

// Result is the remote's last HTTP status, where a redirect pointed, and
// why it couldn't be reached, for an error in words.
func (o *EgressOp) Result() (status int, location string, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.status, o.location, o.err
}

func (o *EgressOp) note(status int, location string, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if status != 0 {
		o.status, o.location = status, location
	}
	if err != nil {
		o.err = err
	}
}

// handshake is SOCKS5 (RFC 1928) with no authentication, CONNECT to the
// domain egress.toft port 80 only; the connection then carries HTTP.
func (e *Egress) handshake(c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(c)
	fail := func(code byte) {
		_, _ = c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
		c.Close()
	}
	var hdr [2]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil || hdr[0] != 5 {
		c.Close()
		return
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(br, methods); err != nil || !strings.ContainsRune(string(methods), 0) {
		_, _ = c.Write([]byte{5, 0xff})
		c.Close()
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		c.Close()
		return
	}
	var req [4]byte
	if _, err := io.ReadFull(br, req[:]); err != nil || req[0] != 5 {
		c.Close()
		return
	}
	if req[1] != 1 { // CONNECT
		fail(7)
		return
	}
	if req[3] != 3 { // a domain name
		fail(8)
		return
	}
	n, err := br.ReadByte()
	if err != nil {
		c.Close()
		return
	}
	host := make([]byte, int(n)+2)
	if _, err := io.ReadFull(br, host); err != nil {
		c.Close()
		return
	}
	if string(host[:n]) != "egress.toft" || host[n] != 0 || host[n+1] != 80 {
		fail(2) // not allowed by the ruleset
		return
	}
	if _, err := c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		c.Close()
		return
	}
	_ = c.SetDeadline(time.Time{})
	select {
	case e.conns <- &bufConn{Conn: c, r: br}:
	case <-e.done:
		c.Close()
	}
}

// ServeHTTP forwards git's request to the operation's remote: only its
// service's ref advertisement and RPC, only the headers git's smart HTTP
// needs, with the credentials added.
func (e *Egress) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	e.mu.Lock()
	op := e.ops[token]
	e.mu.Unlock()
	if op == nil {
		http.Error(w, "unknown operation", http.StatusNotFound)
		return
	}
	switch {
	case rest == "info/refs" && r.Method == http.MethodGet && r.URL.Query().Get("service") == op.service:
	case rest == op.service && r.Method == http.MethodPost:
	default:
		http.Error(w, "not this operation's", http.StatusForbidden)
		return
	}
	target := *op.base
	target.Path = strings.TrimSuffix(op.base.Path, "/") + "/" + rest
	target.RawPath = ""
	target.RawQuery = ""
	if rest == "info/refs" {
		target.RawQuery = "service=" + op.service
	}
	rp := &httputil.ReverseProxy{
		Transport:     e.transport,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			out.URL = &target
			out.Host = target.Host
			hdr := http.Header{}
			for _, k := range []string{"Accept", "Accept-Encoding", "Content-Type", "Content-Encoding", "Git-Protocol", "User-Agent"} {
				if v, ok := pr.In.Header[k]; ok {
					hdr[k] = v
				}
			}
			out.Header = hdr
			if op.user != "" || op.password != "" {
				out.SetBasicAuth(op.user, op.password)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			op.note(resp.StatusCode, resp.Header.Get("Location"), nil)
			for _, k := range []string{"Set-Cookie", "Location", "Www-Authenticate"} {
				resp.Header.Del(k)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			op.note(0, "", err)
			http.Error(w, "the remote could not be reached", http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

// chanListener hands the HTTP server the connections the SOCKS handshake
// accepted.
type chanListener struct{ e *Egress }

func (l chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.e.conns:
		return c, nil
	case <-l.e.done:
		return nil, net.ErrClosed
	}
}
func (l chanListener) Close() error   { return nil }
func (l chanListener) Addr() net.Addr { return &net.UnixAddr{Name: "egress", Net: "unix"} }

// bufConn reads what the handshake's reader already buffered first.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }
