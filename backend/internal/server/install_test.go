package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/agenthub"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

const testJoinToken = "toft_join_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// On a panel without a domain TLS vouches for nothing, so the join command
// carries the checksum of the script the panel serves for its token, and
// the script the agent's. The token is on no command line (D-123).
func TestJoinWithoutADomainChecksTheScriptAndTheAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, _, _ := agentService(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	svc.agents.keyPEM = string(pem.EncodeToMemory(block))
	require.NoError(t, os.WriteFile(filepath.Join(svc.agents.binDir, "toft-agent-linux-amd64"), []byte("agent"), 0o600))
	const pin = "47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="
	h := NewHandler(svc, nil, zap.NewNop()).WithAgentOrigin(func(r *http.Request) (AgentOrigin, error) {
		return AgentOrigin{URL: "https://" + r.Host, InsecureTLS: true, CertPin: pin}, nil
	})

	request := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/agent/install.sh", nil)
		r.Host = "203.0.113.5"
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("Authorization", "Bearer "+testJoinToken)
		return r
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = request()
	join, err := h.joinResponse(c, &JoinToken{Token: testJoinToken})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = request()
	h.installScript(c)
	require.Equal(t, http.StatusOK, w.Code)
	script := w.Body.String()

	assert.Equal(t, fmt.Sprintf(`f=$(mktemp) && (trap 'rm -f "$f"' EXIT; printf 'Authorization: Bearer %%s\n' %s | curl -fsSLk --pinnedpubkey sha256//%s -H @- https://203.0.113.5/api/v1/agent/install.sh -o "$f" || { [ $? = 90 ] && echo "%s" >&2; exit 1; }; echo "%x  $f" | sha256sum -c && sudo sh "$f")`,
		testJoinToken, pin, certChanged, sha256.Sum256([]byte(script))), join.Command)
	assert.Contains(t, script, "TOKEN='"+testJoinToken+"'")
	// The binary's download and the agent's join are pinned to it too.
	assert.Contains(t, script, "PIN='"+pin+"'")
	assert.Contains(t, script, `[ "$INSECURE" = 1 ] && CURL="$CURL -k --pinnedpubkey sha256//$PIN"`)
	assert.Contains(t, script, `[ "$INSECURE" = 1 ] && set -- "$@" --insecure-tls --panel-cert "$PIN"`)

	// A certificate other than the pinned one (curl's 90) says to make a
	// new command, and runs nothing.
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "curl"), []byte("#!/bin/sh\ncat >/dev/null\nexit 90\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "sudo"), []byte("#!/bin/sh\necho ran >&2\n"), 0o755))
	run := exec.Command("sh", "-c", join.Command)
	run.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := run.CombinedOutput()
	require.Error(t, err)
	assert.Equal(t, certChanged+"\n", string(out))
	assert.Contains(t, script, fmt.Sprintf("amd64) SUM='%x'", sha256.Sum256([]byte("agent"))))
	assert.Contains(t, script, "arm64) SUM=''", "no agent for arm64: the script refuses it")
	assert.NotRegexp(t, `@[A-Z0-9_]+@`, script, "every placeholder is filled")
}

// The script is served with the token its request carries written in, and
// with none without one (`sh -s -- --token` still works; a proxy's basic
// auth is none either), and parses as sh; a bearer token
// that isn't a join token's shape is never written into a shell script.
func TestInstallScriptWritesInOnlyAJoinToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, _, _ := agentService(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	svc.agents.keyPEM = string(pem.EncodeToMemory(block))
	h := NewHandler(svc, nil, zap.NewNop())
	serve := func(auth string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/agent/install.sh", nil)
		c.Request.Host = "panel.example.com"
		if auth != "" {
			c.Request.Header.Set("Authorization", auth)
		}
		h.installScript(c)
		return w
	}

	tok, err := newJoinToken()
	require.NoError(t, err)
	assert.Regexp(t, joinTokenRe, tok, "every token the panel makes can be written in")

	w := serve("Bearer " + testJoinToken)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "TOKEN='"+testJoinToken+"'")

	parse := exec.Command("sh", "-n")
	parse.Stdin = strings.NewReader(w.Body.String())
	out, err := parse.CombinedOutput()
	require.NoError(t, err, string(out))

	for _, none := range []string{"", "Basic dXNlcjpwYXNz"} {
		w = serve(none)
		require.Equal(t, http.StatusOK, w.Code, none)
		assert.Contains(t, w.Body.String(), "TOKEN=''", none)
	}

	for _, bad := range []string{"Bearer toft_join_x", "Bearer " + testJoinToken + "'; reboot; '", "Bearer paas_" + testJoinToken[10:]} {
		w = serve(bad)
		assert.Equal(t, http.StatusBadRequest, w.Code, bad)
		assert.NotContains(t, w.Body.String(), "reboot")
	}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Request.Host = "panel.example.com"
	join, err := h.joinResponse(c, &JoinToken{Token: testJoinToken})
	require.NoError(t, err)
	assert.Equal(t, "printf 'Authorization: Bearer %s\\n' "+testJoinToken+" | curl -fsSL -H @- http://panel.example.com/api/v1/agent/install.sh | sudo sh", join.Command)
}

// sshInstallHost records what InstallAgent runs over SSH.
type sshInstallHost struct {
	sshpool.RemoteHost
	cmds  []string
	stdin []string
}

func (h *sshInstallHost) Exec(_ context.Context, cmd string) (string, error) {
	h.cmds = append(h.cmds, cmd)
	if cmd == "uname -m" {
		return "x86_64", nil
	}
	return "", nil
}

func (h *sshInstallHost) ExecInput(_ context.Context, cmd string, stdin io.Reader) (string, error) {
	h.cmds = append(h.cmds, cmd)
	b, _ := io.ReadAll(stdin)
	h.stdin = append(h.stdin, string(b))
	return "", nil
}

func (h *sshInstallHost) ExecStream(_ context.Context, cmd string, _ func(string)) error {
	h.cmds = append(h.cmds, cmd)
	return nil
}

func (h *sshInstallHost) Upload(_ context.Context, path string, _ []byte) error {
	h.cmds = append(h.cmds, "upload "+path)
	return nil
}

func (h *sshInstallHost) Subsystem(context.Context, string, io.Reader) (string, error) {
	return "", io.EOF
}

type sshInstallConn struct{ h *sshInstallHost }

func (c sshInstallConn) Handle(sshpool.Target) sshpool.RemoteHost { return c.h }
func (sshInstallConn) Invalidate(string)                          {}

// "Toft installs it over SSH" hands the join token over on stdin into a
// root-only file: it is on none of the commands, which `ps` shows on the
// server for the whole join (D-123).
func TestInstallOverSSHKeepsTheTokenOffCommandLines(t *testing.T) {
	svc, _, hub := agentService(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	svc.agents.keyPEM = string(pem.EncodeToMemory(block))
	require.NoError(t, os.WriteFile(filepath.Join(svc.agents.binDir, "toft-agent-linux-amd64"), []byte("agent"), 0o600))
	host := &sshInstallHost{}
	svc.WithSSH(sshInstallConn{host}, nil)
	hub.known, hub.state = true, agenthub.State{Connected: true}

	require.NoError(t, svc.InstallAgent(context.Background(), "u1", "s1", AgentOrigin{URL: "https://panel.example.com"}, true, func(string) {}))

	require.Len(t, host.stdin, 1)
	tok := host.stdin[0]
	require.True(t, strings.HasPrefix(tok, "toft_join_"), "the token goes in on stdin")
	for _, c := range host.cmds {
		assert.NotContains(t, c, tok)
	}
	assert.Contains(t, host.cmds, "install -d -m 700 /etc/toft-agent && (umask 077 && cat > /etc/toft-agent/join-token)")
	assert.Contains(t, strings.Join(host.cmds, "\n"), "toft-agent join --panel 'https://panel.example.com' --server 's1' --token-file /etc/toft-agent/join-token")
	assert.NotContains(t, strings.Join(host.cmds, "\n"), "--insecure-tls")
}

// Without a domain, the agent the panel installs over SSH joins pinned to
// the panel's certificate, as one joined by command does.
func TestInstallOverSSHWithoutADomainPinsThePanelsCertificate(t *testing.T) {
	svc, _, hub := agentService(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	svc.agents.keyPEM = string(pem.EncodeToMemory(block))
	require.NoError(t, os.WriteFile(filepath.Join(svc.agents.binDir, "toft-agent-linux-amd64"), []byte("agent"), 0o600))
	host := &sshInstallHost{}
	svc.WithSSH(sshInstallConn{host}, nil)
	hub.known, hub.state = true, agenthub.State{Connected: true}

	require.NoError(t, svc.InstallAgent(context.Background(), "u1", "s1", AgentOrigin{URL: "https://203.0.113.5", InsecureTLS: true, CertPin: "pin+/="}, true, func(string) {}))
	assert.Contains(t, strings.Join(host.cmds, "\n"), " --insecure-tls --panel-cert 'pin+/='")
}
