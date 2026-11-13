package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The app server as the probe read it on 2026-10-20 (D-93): a KVM guest
// without /dev/kvm, runc only.
const appServerProbe = `kernel:6.8.0-146-generic
arch:x86_64
cgroup:2
virt:kvm
kvm:0
docker:29.4.3
security:["name=apparmor","name=seccomp,profile=builtin","name=cgroupns"]
runtime:runc:ok
`

func TestParseCapabilitiesAndLevels(t *testing.T) {
	c, err := parseCapabilities(appServerProbe)
	require.NoError(t, err)
	assert.Equal(t, Capabilities{Kernel: "6.8.0-146-generic", Arch: "x86_64", CgroupV2: true, Virtualization: "kvm", DockerVersion: "29.4.3",
		AppArmor: true, Seccomp: true, Runtimes: []Runtime{{Name: "runc", OK: true}}}, c)
	lv := c.levels()
	require.Len(t, lv, 5)
	assert.Equal(t, []string{"available", "not_installed", "unavailable", "unavailable", "available"}, []string{lv[0].State, lv[1].State, lv[2].State, lv[3].State, lv[4].State})
	assert.Contains(t, lv[1].Reason, "systrap")
	assert.NotContains(t, lv[0].Reason, "is off")
	assert.Contains(t, lv[2].Reason, "Needs Toft's agent")
	assert.Contains(t, lv[3].Reason, "runs under kvm without nested virtualisation")

	c, err = parseCapabilities(appServerProbe + "runtime:runsc:fail:docker: Error response from daemon: unknown runtime\n")
	require.NoError(t, err)
	assert.Equal(t, Runtime{Name: "runsc", Error: "docker: Error response from daemon: unknown runtime"}, c.Runtimes[1])
	assert.Equal(t, "failing", c.levels()[1].State)
	assert.Contains(t, c.levels()[1].Reason, "smoke test failed")

	// Registered by hand, without Toft's arguments: DNS would fail (D-95).
	c, err = parseCapabilities(strings.Replace(appServerProbe, "runtime:runc:ok", "runtime:runc:ok\nruntime:runsc:ok", 1))
	require.NoError(t, err)
	assert.Equal(t, "failing", c.levels()[1].State, "runsc without Toft's settings")
	assert.Contains(t, c.levels()[1].Reason, "Install gVisor again")

	// As Toft registers it: the version and arguments are read too.
	installed := strings.Replace(appServerProbe, "runtime:runc:ok", "runtime:runc:ok\nruntime:runsc:ok", 1) +
		`runtimeargs:{"runc":{"path":"runc"},"runsc":{"path":"/usr/bin/runsc","runtimeArgs":["--network=host","--oci-seccomp"],"status":{"x":"y"}}}` + "\n" +
		"runsc:runsc version release-20261012.0\n"
	c, err = parseCapabilities(installed)
	require.NoError(t, err)
	assert.Equal(t, Runtime{Name: "runsc", OK: true, Version: "20261012.0", Args: []string{"--network=host", "--oci-seccomp"}}, c.Runtimes[1])
	assert.Equal(t, "available", c.levels()[1].State, "sandboxed once runsc passes")
	assert.Contains(t, c.levels()[1].Reason, "systrap")

	// KVM in a virtual machine keeps systrap; bare metal with /dev/kvm wants the KVM platform.
	c.KVM = true
	assert.Equal(t, "available", c.levels()[1].State, "a KVM guest with /dev/kvm keeps systrap")
	c.Virtualization = "none"
	assert.Equal(t, "failing", c.levels()[1].State, "bare metal wants --platform=kvm")
	c.Runtimes[1].Args = GvisorArgs(true)
	assert.Equal(t, "available", c.levels()[1].State)
	assert.Contains(t, c.levels()[1].Reason, "the KVM platform")

	c, err = parseCapabilities(strings.Replace(appServerProbe, "kernel:6.8.0-146-generic", "kernel:5.4.0-200-generic", 1))
	require.NoError(t, err)
	assert.Equal(t, "unavailable", c.levels()[1].State)
	assert.Contains(t, c.levels()[1].Reason, "needs Linux 5.6")

	c, _ = parseCapabilities(strings.Replace(appServerProbe, `"name=apparmor",`, "", 1))
	assert.Contains(t, c.levels()[0].Reason, "AppArmor is off")

	_, err = parseCapabilities("bash: docker: command not found\n")
	assert.Error(t, err)
	c, _ = parseCapabilities("kernel:6.8.0\narch:x86_64\ndocker:\n")
	assert.Equal(t, "Docker isn't answering on this server", c.Error)
	assert.Equal(t, "failing", c.levels()[0].State)
}

// The strict network (D-115) needs the agent, runsc-strict as Toft
// registers it, and the agent's DNS relay answering containers.
func TestStrictLevel(t *testing.T) {
	both := strings.Replace(appServerProbe, "runtime:runc:ok", "runtime:runc:ok\nruntime:runsc:ok\nruntime:runsc-strict:ok", 1) +
		`runtimeargs:{"runsc":{"path":"/usr/bin/runsc","runtimeArgs":["--network=host","--oci-seccomp"]},"runsc-strict":{"path":"/usr/bin/runsc","runtimeArgs":["--oci-seccomp"]}}` + "\n" +
		"runsc:runsc version release-20261012.0\ndnsrelay:1\n"
	c, err := parseCapabilities(both)
	require.NoError(t, err)
	assert.True(t, c.DNSRelay)
	assert.Equal(t, "20261012.0", c.runtime("runsc-strict").Version)
	strict := func() Level { return c.levels()[2] }

	assert.Equal(t, "unavailable", strict().State, "an SSH server")
	c.Agent = true
	assert.Equal(t, "available", strict().State)
	assert.Contains(t, strict().Reason, "systrap")

	c.DNSRelay = false
	assert.Equal(t, "failing", strict().State)
	assert.Contains(t, strict().Reason, "Install gVisor again")
	c.DNSRelay = true

	c.runtime("runsc-strict").Args = GvisorArgs(false)
	assert.Equal(t, "failing", strict().State, "registered with the host's network stack")
	c.runtime("runsc-strict").Args = GvisorStrictArgs(false)

	c.runtime("runsc-strict").OK = false
	assert.Equal(t, "failing", strict().State)
	assert.Contains(t, strict().Reason, "smoke test failed")

	c.Runtimes = c.Runtimes[:2]
	assert.Equal(t, "not_installed", strict().State, "gVisor from before the strict network")
	assert.Contains(t, strict().Reason, "Install gVisor again")
	assert.Equal(t, "available", c.levels()[1].State, "Sandboxed still runs")

	c.Runtimes = c.Runtimes[:1]
	assert.Equal(t, "not_installed", strict().State)
	assert.Contains(t, strict().Reason, "Install gVisor adds it")
}

// The app network address pool is the one Docker on the server reports,
// never the panel's: none ([]) when daemon.json has no
// default-address-pools, and unknown (nil) when Docker didn't say.
func TestParseAddressPools(t *testing.T) {
	c, err := parseCapabilities(appServerProbe + "pools:null\n")
	require.NoError(t, err)
	assert.Equal(t, []AddressPool{}, c.AddressPools, "daemon.json without default-address-pools")
	b, err := json.Marshal(c)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"address_pools":[]`)

	c, err = parseCapabilities(appServerProbe + `pools:[{"Base":"10.50.0.0/16","Size":26}]` + "\n")
	require.NoError(t, err)
	assert.Equal(t, []AddressPool{{Base: "10.50.0.0/16", Size: 26}}, c.AddressPools)

	for _, out := range []string{appServerProbe, appServerProbe + "pools:\n", appServerProbe + "pools:garbage\n"} {
		c, err = parseCapabilities(out)
		require.NoError(t, err)
		assert.Nil(t, c.AddressPools, "not read: %q", out)
	}
	assert.Contains(t, capabilityScript(), `docker info --format '{{json .DefaultAddressPools}}'`)
}

func TestKernelAtLeast(t *testing.T) {
	for rel, want := range map[string]bool{"5.6.0": true, "5.5.19-x": false, "6.8.0-146-generic": true, "4.19": false, "": false, "6": false} {
		assert.Equal(t, want, kernelAtLeast(rel, 5, 6), rel)
	}
}

// probeHost answers the probe's one command with out, or fails with err.
type probeHost struct {
	sshpool.RemoteHost
	out  string
	err  error
	cmds []string
}

func (h *probeHost) Exec(_ context.Context, cmd string) (string, error) {
	h.cmds = append(h.cmds, cmd)
	return h.out, h.err
}

type probeConn struct{ h *probeHost }

func (c probeConn) Handle(sshpool.Target) sshpool.RemoteHost { return c.h }
func (probeConn) Invalidate(string)                          {}

// memCaps keeps what the probe read, as the Postgres store does: a failed
// probe keeps the last read, with its error.
type memCaps struct{ c map[string]Capabilities }

func (m *memCaps) SaveCapabilities(_ context.Context, id string, c Capabilities) error {
	c.ProbedAt = time.Now()
	m.c[id] = c
	return nil
}
func (m *memCaps) SetCapabilitiesError(_ context.Context, id, msg string) error {
	c := m.c[id]
	c.Error, c.ProbedAt = msg, time.Now()
	if c.Runtimes == nil {
		c.Runtimes = []Runtime{}
	}
	m.c[id] = c
	return nil
}
func (m *memCaps) GetCapabilities(_ context.Context, id string) (*Capabilities, error) {
	c, ok := m.c[id]
	if !ok {
		return nil, nil
	}
	return &c, nil
}
func (m *memCaps) StaleCapabilityServers(context.Context) ([]string, error) { return nil, nil }

func TestProbeCapabilitiesOverSSH(t *testing.T) {
	host := &probeHost{err: errors.New("could not reach 203.0.113.7:22")}
	s := NewService(newFakeRepo(&Server{ID: testServerID, IPAddress: "203.0.113.7", Port: 22}), "")
	s.WithSSH(probeConn{host}, nil)
	s.WithCapabilities(&memCaps{c: map[string]Capabilities{}})

	// Never read: the error, no levels to judge.
	c, err := s.ProbeCapabilities(context.Background(), testServerID)
	require.NoError(t, err)
	assert.Contains(t, c.Error, "could not reach")
	assert.Empty(t, c.Levels)

	// The one command pipes the script, which sh parses, to sh.
	cmd := host.cmds[0]
	require.True(t, strings.HasPrefix(cmd, "echo ") && strings.HasSuffix(cmd, " | base64 -d | sh"), cmd)
	script, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(cmd, "echo "), " | base64 -d | sh"))
	require.NoError(t, err)
	assert.Equal(t, capabilityScript(), string(script))
	parse := exec.Command("sh", "-n")
	parse.Stdin = strings.NewReader(string(script))
	out, err := parse.CombinedOutput()
	require.NoError(t, err, string(out))

	host.out, host.err = appServerProbe, nil
	c, err = s.ProbeCapabilities(context.Background(), testServerID)
	require.NoError(t, err)
	assert.Equal(t, "6.8.0-146-generic", c.Kernel)
	assert.Empty(t, c.Error)
	require.Len(t, c.Levels, 5)

	// A failed probe keeps the last read, with its error.
	host.out, host.err = "", errors.New("connection reset")
	c, err = s.ProbeCapabilities(context.Background(), testServerID)
	require.NoError(t, err)
	assert.Equal(t, "6.8.0-146-generic", c.Kernel)
	assert.Contains(t, c.Error, "connection reset")
	assert.Equal(t, "available", c.Levels[0].State)
}
