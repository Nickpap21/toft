package server

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRequirementsAGoodServer(t *testing.T) {
	r := parseRequirements("os:Ubuntu 24.04.4 LTS\ndistro:ubuntu debian\nuid:0 root\narch:x86_64\nkernel:6.8.0-146-generic\nsystemd:systemd 255 (255.4-1ubuntu8.17)\n" +
		"cgroup:cgroup2fs\ndockercli:Docker version 29.8.0, build x\ndocker:29.8.0\n" +
		"listen:users:((\"docker-proxy\",pid=3012,fd=8)) users:((\"docker-proxy\",pid=3077,fd=8))\npublished:paas-traefik|traefik:v3.6.10\n")
	require.True(t, r.Met, r.unmet())
	found := map[string]string{}
	for _, c := range r.Checks {
		found[c.ID] = c.Found
	}
	assert.Equal(t, "Ubuntu 24.04.4 LTS · amd64", found["os"])
	assert.Equal(t, "systemd 255", found["systemd"])
	assert.Equal(t, "v2, unified hierarchy", found["cgroup"])
	assert.Equal(t, "29.8.0", found["docker"])
	assert.Equal(t, "used by Traefik", found["ports"])
	assert.Equal(t, "Ubuntu 24.04.4 LTS · amd64, kernel 6.8.0, cgroup v2, Docker 29.8.0", r.summary())
}

func TestParseRequirementsRefusesWhatD29Refuses(t *testing.T) {
	cases := map[string]struct{ out, id string }{
		"old Docker":     {"docker:27.5.1\ndockercli:Docker version 27.5.1", "docker"},
		"Docker stopped": {"docker:\ndockercli:Docker version 29.1.0", "docker"},
		"old kernel":     {"kernel:5.4.0-200-generic", "kernel"},
		"cgroup v1":      {"cgroup:tmpfs", "cgroup"},
		"32-bit":         {"arch:armv7l", "os"},
		"no systemd":     {"systemd:", "systemd"},
		"nginx on 80":    {"listen:users:((\"nginx\",pid=1,fd=6))", "ports"},
		"a container":    {"published:shop-web|nginx:1.27", "ports"},
		"Fedora":         {"distro:fedora", "distro"},
		"a sudo user":    {"uid:1001 deploy", "root"},
	}
	base := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(goodHost), "\n") {
		k, v, _ := strings.Cut(l, ":")
		base[k] = v
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			facts := map[string]string{}
			for k, v := range base {
				facts[k] = v
			}
			for _, l := range strings.Split(c.out, "\n") {
				k, v, _ := strings.Cut(l, ":")
				facts[k] = v
			}
			var b strings.Builder
			for k, v := range facts {
				b.WriteString(k + ":" + v + "\n")
			}
			r := parseRequirements(b.String())
			assert.False(t, r.Met)
			for _, ch := range r.Checks {
				assert.Equal(t, ch.ID != c.id, ch.Met, "%s: %+v", ch.ID, ch)
				if !ch.Met {
					assert.NotEmpty(t, ch.Fix)
				}
			}
		})
	}
}

func TestProvisionRefusesAServerBelowTheRequirementsFirst(t *testing.T) {
	sh := &fakeShell{answer: func(cmd string) string {
		if strings.HasPrefix(cmd, requirementsMarker) {
			return strings.Replace(goodHost, "docker:\n", "docker:27.5.1\n", 1)
		}
		return ""
	}}
	_, err := provision(sh, provisionOpts{SSHPort: 22}, func(string) {})
	require.ErrorIs(t, err, ErrRequirementsNotMet)
	assert.Contains(t, err.Error(), "Docker Engine 29 or newer (27.5.1)")
	assert.Len(t, sh.cmds, 1, "nothing runs after the check")
}
