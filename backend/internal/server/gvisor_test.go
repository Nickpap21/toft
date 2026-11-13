package server

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gvisorHostFake is a server as the install sees it: what it answers, which
// files it holds, and whether Docker lists runsc yet. fail makes a command
// fail.
type gvisorHostFake struct {
	probe      string              // the check step's answer
	files      map[string]string   // path → content
	runsc      string              // the installed runsc version
	registered map[string][]string // runtime → its arguments in Docker; nil: none listed
	pending    map[string][]string // written by runsc install, listed after a reload
	ufw        bool                // ufw is installed
	fail       func(cmd string) bool
	cmds       []string
	firewall   bool // the toft table is loaded
}

func freshGvisorHost() *gvisorHostFake {
	return &gvisorHostFake{probe: "6.8.0-146-generic\namd64\n/usr/bin/apt-get\nkvm\n\nUbuntu 24.04.4 LTS", files: map[string]string{}}
}

func (h *gvisorHostFake) Exec(cmd string) (string, error) {
	h.cmds = append(h.cmds, cmd)
	if h.fail != nil && h.fail(cmd) {
		return "boom", errors.New("exit status 1")
	}
	switch {
	case strings.HasPrefix(cmd, ". /etc/os-release"):
		return h.probe, nil
	case strings.HasPrefix(cmd, "docker version"):
		return "29.4.3", nil
	case strings.HasPrefix(cmd, "sha256sum "):
		path := strings.Fields(cmd)[1]
		if c, ok := h.files[path]; ok {
			sum := sha256.Sum256([]byte(c))
			return hex.EncodeToString(sum[:]), nil
		}
		return "", nil
	case strings.HasPrefix(cmd, "dpkg-query"):
		return h.runsc, nil
	case strings.HasPrefix(cmd, "docker info --format '{{json .Runtimes}}'"):
		rts := map[string]dockerRuntime{"runc": {Path: "runc"}}
		for name, args := range h.registered {
			rts[name] = dockerRuntime{Path: runscPath, Args: args}
		}
		out, _ := json.Marshal(rts)
		return string(out), nil
	case strings.HasPrefix(cmd, runscPath+" install --runtime="):
		head, tail, _ := strings.Cut(cmd, " -- ")
		args := strings.Fields(tail)
		if h.pending == nil {
			h.pending = maps.Clone(h.registered)
			if h.pending == nil {
				h.pending = map[string][]string{}
			}
		}
		h.pending[strings.TrimPrefix(head, runscPath+" install --runtime=")] = args[:len(args)-1] // 2>&1
	case cmd == "systemctl reload docker":
		h.registered, h.pending = h.pending, nil
	case strings.HasPrefix(cmd, "command -v ufw"):
		if h.ufw {
			return "yes", nil
		}
		return "no", nil
	case strings.HasPrefix(cmd, "command -v nft"):
		return "yes", nil
	case strings.HasPrefix(cmd, "systemctl restart toft-firewall"):
		h.firewall = true
	case strings.HasPrefix(cmd, "nft list table ip toft"):
		if h.firewall {
			return "loaded", nil
		}
		return "missing", nil
	}
	return "", nil
}

func (h *gvisorHostFake) ExecInput(cmd string, stdin io.Reader) (string, error) {
	in, _ := io.ReadAll(stdin)
	if _, path, ok := strings.Cut(cmd, "cat > "); ok {
		h.files[path] = string(in)
	}
	return h.Exec(cmd)
}

func (h *gvisorHostFake) ExecStream(cmd string, logFn func(string)) error {
	if strings.Contains(cmd, "apt-get -o") && (h.fail == nil || !h.fail(cmd)) {
		h.runsc = gvisorVersion
	}
	_, err := h.Exec(cmd)
	return err
}

// gvisorChanges are the commands that change a server during the install.
var gvisorChanges = []string{"cat > " + gvisorKeyPath, "cat > " + gvisorListPath, "apt-get -o", runscPath + " install", "systemctl reload docker"}

func TestInstallGvisorOnAFreshServer(t *testing.T) {
	h := freshGvisorHost()
	var lines []string
	got, err := installGvisor(h, false, func(l string) { lines = append(lines, l) })
	require.NoError(t, err)
	assert.Equal(t, "systrap", got.platform(), "a KVM guest without /dev/kvm")

	assert.Equal(t, gvisorKey, h.files[gvisorKeyPath])
	assert.Equal(t, "deb [arch=amd64 signed-by="+gvisorKeyPath+"] https://storage.googleapis.com/gvisor/releases 20261012 main\n", h.files[gvisorListPath])
	order := []string{"cat > " + gvisorKeyPath, "cat > " + gvisorListPath, "install -yq --no-install-recommends --allow-downgrades runsc=20261012.0",
		"cp -p /etc/docker/daemon.json", runscPath + " install --runtime=runsc -- --network=host --oci-seccomp",
		runscPath + " install --runtime=runsc-strict -- --oci-seccomp 2>&1", "dockerd --validate", "systemctl reload docker", "base64 -d | sh"}
	last := -1
	for _, frag := range order {
		i := indexOf(h.cmds, frag)
		require.GreaterOrEqual(t, i, 0, "missing %q", frag)
		assert.Greater(t, i, last, "%q out of order", frag)
		last = i
	}
	assert.False(t, issued(h.cmds, "systemctl restart docker"), "Docker is reloaded, never restarted")
	assert.True(t, issued(h.cmds, "Dir::Etc::sourcelist="+gvisorListPath), "only gVisor's list is updated")
	assert.True(t, issued(h.cmds, "NEEDRESTART_SUSPEND=1"), "needrestart never restarts Docker")
	for i, s := range GvisorSteps {
		assert.Contains(t, lines, "["+string(rune('1'+i))+"/6] "+s)
	}
	assert.Equal(t, 1, strings.Count(strings.Join(h.cmds, "\n"), "base64 -d | sh"), "over SSH only the Sandboxed test runs")
	assert.Contains(t, lines, "  · Skipped: the strict network needs Toft's agent on the server to answer names")
	assert.Contains(t, lines, "  ✓ Docker reloaded; no container was restarted")
	assert.Equal(t, hostFirewallRules, h.files[hostFirewallRulesPath], "the host firewall, with the untrusted previews' chain, is loaded too")
	assert.True(t, h.firewall)
}

// On an agent's server the strict network is tested too, through the
// agent's DNS relay, which UFW lets containers reach (D-115).
func TestInstallGvisorTestsTheStrictNetworkWithTheAgent(t *testing.T) {
	h := freshGvisorHost()
	h.ufw = true
	var lines []string
	_, err := installGvisor(h, true, func(l string) { lines = append(lines, l) })
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"runsc": {"--network=host", "--oci-seccomp"}, "runsc-strict": {"--oci-seccomp"}}, h.registered)
	ufw := indexOf(h.cmds, "ufw allow to 169.254.53.53 port 53")
	require.GreaterOrEqual(t, ufw, 0)
	strict := indexOf(h.cmds[ufw:], "base64 -d | sh")
	require.GreaterOrEqual(t, strict, 0, "the strict test runs after the firewall opens")
	assert.Contains(t, lines, "  ✓ A strict container found another by name through the agent, and the health probe reached it")

	h.fail = func(cmd string) bool { return strings.HasPrefix(cmd, "echo ") && cmd == h.cmds[ufw+strict] }
	_, err = installGvisor(h, true, func(string) {})
	require.ErrorContains(t, err, "a strict test container failed")
}

func TestStrictCheckScriptParses(t *testing.T) {
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(strictCheckScript)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func TestInstallGvisorAgainChangesNothing(t *testing.T) {
	h := freshGvisorHost()
	_, err := installGvisor(h, false, func(string) {})
	require.NoError(t, err)

	h.cmds = nil
	var lines []string
	_, err = installGvisor(h, false, func(l string) { lines = append(lines, l) })
	require.NoError(t, err)
	for _, c := range gvisorChanges {
		assert.False(t, issued(h.cmds, c), "a second install must not run %q", c)
	}
	assert.Contains(t, lines, "  ✓ runsc 20261012.0 already installed")
	assert.Contains(t, lines, "  ✓ Docker already runs runsc and runsc-strict with Toft's settings")
}

func TestInstallGvisorReregistersRunscWithoutToftsSettings(t *testing.T) {
	h := freshGvisorHost()
	h.runsc, h.registered = gvisorVersion, map[string][]string{"runsc": {}} // registered by hand: no arguments
	_, err := installGvisor(h, false, func(string) {})
	require.NoError(t, err)
	assert.True(t, issued(h.cmds, runscPath+" install --runtime=runsc -- --network=host --oci-seccomp"))
	assert.False(t, issued(h.cmds, "apt-get -o"))
	assert.Equal(t, map[string][]string{"runsc": {"--network=host", "--oci-seccomp"}, "runsc-strict": {"--oci-seccomp"}}, h.registered)
}

func TestInstallGvisorUsesKVMOnBareMetal(t *testing.T) {
	h := freshGvisorHost()
	h.probe = "6.8.0-146-generic\narm64\n/usr/bin/apt-get\nnone\nkvm\nDebian GNU/Linux 13"
	got, err := installGvisor(h, false, func(string) {})
	require.NoError(t, err)
	assert.Equal(t, "kvm", got.platform())
	assert.True(t, issued(h.cmds, "--runtime=runsc -- --network=host --oci-seccomp --platform=kvm"))
	assert.True(t, issued(h.cmds, "--runtime=runsc-strict -- --oci-seccomp --platform=kvm"))
	assert.Contains(t, h.files[gvisorListPath], "arch=arm64")
}

func TestInstallGvisorPutsDockersConfigurationBack(t *testing.T) {
	h := freshGvisorHost()
	h.fail = func(cmd string) bool { return strings.HasPrefix(cmd, "dockerd --validate") }
	_, err := installGvisor(h, false, func(string) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "put back as it was")
	assert.True(t, issued(h.cmds, "mv "+daemonBackup+" "+daemonConfigPath), "the copy is restored")
	assert.False(t, issued(h.cmds, "systemctl reload docker"), "Docker isn't reloaded with a refused file")
}

func TestInstallGvisorRefusesBeforeChangingAnything(t *testing.T) {
	for name, probe := range map[string]string{
		"old kernel":   "5.4.0-1\namd64\n/usr/bin/apt-get\nkvm\n\nUbuntu 20.04",
		"architecture": "6.8.0-1\narmhf\n/usr/bin/apt-get\nkvm\n\nRaspbian",
		"no apt":       "6.8.0-1\namd64\n\nkvm\n\nFedora",
	} {
		t.Run(name, func(t *testing.T) {
			h := freshGvisorHost()
			h.probe = probe
			_, err := installGvisor(h, false, func(string) {})
			require.Error(t, err)
			for _, c := range gvisorChanges {
				assert.False(t, issued(h.cmds, c), "refused, yet ran %q", c)
			}
		})
	}
}

func TestInstallGvisorStopsWhenAptFails(t *testing.T) {
	h := freshGvisorHost()
	h.fail = func(cmd string) bool { return strings.Contains(cmd, "apt-get -o") }
	_, err := installGvisor(h, false, func(string) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apt-get install runsc=20261012.0")
	assert.False(t, issued(h.cmds, runscPath+" install"), "Docker is left alone")
}

func TestGvisorCheckScriptParses(t *testing.T) {
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(gvisorCheckScript)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

// The built-in key is gVisor's: its OpenPGP v4 fingerprint is the SHA-1 of
// the first public-key packet (RFC 4880 §12.2).
func TestGvisorKeyFingerprint(t *testing.T) {
	var b64 strings.Builder
	body := false
	for _, line := range strings.Split(gvisorKey, "\n") {
		switch {
		case strings.HasPrefix(line, "-----BEGIN"):
			continue
		case !body && strings.TrimSpace(line) == "":
			body = true
		case body && (strings.HasPrefix(line, "=") || strings.HasPrefix(line, "-----END")):
			body = false
		case body:
			b64.WriteString(strings.TrimSpace(line))
		}
	}
	raw, err := base64.StdEncoding.DecodeString(b64.String())
	require.NoError(t, err)
	require.NotEmpty(t, raw)
	var n, off int
	switch tag := raw[0]; {
	case tag&0x40 != 0: // new format
		require.Less(t, int(raw[1]), 192, "one-octet length")
		n, off = int(raw[1]), 2
	default: // old format, length type in the low two bits
		switch tag & 3 {
		case 0:
			n, off = int(raw[1]), 2
		case 1:
			n, off = int(raw[1])<<8|int(raw[2]), 3
		default:
			t.Fatalf("unexpected length type %d", tag&3)
		}
	}
	h := sha1.New()
	h.Write([]byte{0x99, byte(n >> 8), byte(n)})
	h.Write(raw[off : off+n])
	assert.Equal(t, "6f1df85e3a71c24918e727d56fc6d554e32bd943", hex.EncodeToString(h.Sum(nil)))
}
