package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The plugin's architecture comes from uname -m, which every distribution
// has: dpkg doesn't exist on Rocky and AlmaLinux, and a container started
// with the loki driver fails there. The command ensureLokiDriver sends runs
// in a real sh with docker, uname and dpkg stubbed (dpkg absent, as on an
// RPM system).
func TestEnsureLokiDriverPicksThePluginWithoutDpkg(t *testing.T) {
	sh := &fakeShell{}
	require.NoError(t, ensureLokiDriver(sh, false, func(string) {}))
	cmd := sh.cmds[len(sh.cmds)-1]
	require.Contains(t, cmd, "docker plugin install")

	bin := t.TempDir()
	stub := func(name, script string) {
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755))
	}
	stub("docker", `echo "docker $*"`)
	stub("dpkg", `echo "dpkg: command not found" >&2; exit 127`)

	for machine, tag := range map[string]string{
		"x86_64":  "3.7.9-amd64@sha256:88f98af1",
		"aarch64": "3.7.9-arm64@sha256:c78bdbb2",
		"arm64":   "3.7.9-arm64@sha256:c78bdbb2",
		"riscv64": "",
	} {
		stub("uname", `echo `+machine)
		c := exec.Command("sh", "-c", cmd)
		c.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
		out, err := c.CombinedOutput()
		require.NoError(t, err, "%s: %s", machine, out)
		if tag == "" {
			assert.NotContains(t, string(out), "docker plugin install", machine)
			continue
		}
		assert.Contains(t, string(out), "docker plugin install grafana/loki-docker-driver:"+tag, machine)
	}
}
