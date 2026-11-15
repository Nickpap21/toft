package docker

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"

	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProbeVerdict(t *testing.T) {
	healthy, _, ran, answer := probeVerdict(0, "toft-probe 200 4\n")
	assert.True(t, healthy && ran)
	assert.Equal(t, probeAnswer{status: 200, millis: 4}, answer)

	healthy, detail, ran, answer := probeVerdict(1, "toft-probe 500 12\nHTTP 500\n")
	assert.True(t, ran)
	assert.False(t, healthy)
	assert.Equal(t, "HTTP 500", detail)
	assert.Equal(t, probeAnswer{status: 500, millis: 12}, answer)

	_, detail, _, answer = probeVerdict(1, "toft-probe 000 3\nwget: can't connect to remote host (127.0.0.1): Connection refused\n")
	assert.Equal(t, 0, answer.status, "no answer")
	assert.Contains(t, detail, "Connection refused")

	// 126 and 127: the probe could not run (a container from before it was
	// mounted), which is not a failing service.
	for _, code := range []int{126, 127, 2} {
		_, _, ran, _ = probeVerdict(code, "")
		assert.False(t, ran, "exit %d", code)
	}
}

// The probe's time is the request's, in milliseconds, whatever unit the
// kernel's adjtimex reports: an NTP-synced kernel (STA_NANO) gives nanoseconds
// in tv_usec, and the probe showed 1,000 times the real time there. The
// busybox the script calls is wrapped here to report adjtimex as such a
// kernel does, and the endpoint takes 0.2 s.
func TestProbeScriptTimesTheRequestInMilliseconds(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("needs docker")
	}
	const setup = `mkdir -p ` + rollout.ProbeMountPath + `/bin /www/cgi-bin
cat > ` + rollout.ProbeMountPath + `/bin/busybox <<'WRAP'
#!/bin/sh
[ "$1" = adjtimex ] || exec /bin/busybox "$@"
/bin/busybox adjtimex | /bin/busybox awk '/tv_usec/{$2=$2*1000} {print}'
WRAP
cat > /www/cgi-bin/slow <<'CGI'
#!/bin/sh
sleep 0.2
echo "Content-type: text/plain"
echo
echo ok
CGI
chmod +x ` + rollout.ProbeMountPath + `/bin/busybox /www/cgi-bin/slow
httpd -p 8080 -h /www
exec ` + rollout.ProbeMountPath + `/bin/busybox sh -c "$0" toft-probe 8080 /cgi-bin/slow 5`
	out, err := exec.Command("docker", "run", "--rm", rollout.ProbeToolImage, "sh", "-c", setup, probeScript).Output()
	require.NoError(t, err, "%s", out)
	f := strings.Fields(string(out))
	require.Len(t, f, 3, "%s", out)
	assert.Equal(t, "200", f[1])
	ms, err := strconv.Atoi(f[2])
	require.NoError(t, err)
	assert.GreaterOrEqual(t, ms, 200)
	assert.Less(t, ms, 1000)
}

// A container gets the probe's tool on a Docker API with image mounts, and
// is created without it (not refused) on an older one.
func TestContainersGetTheProbeToolWhereDockerCanMountIt(t *testing.T) {
	plan := netplan.LegacyPlan(slotSvcID, "web", "web", "cp-net")
	run := func(t *testing.T, f *fakedocker.Docker, dc *client.Client) []string {
		var warned []string
		require.NoError(t, runContainerBlueGreen(nil, dc, rollout.NetConfig{}, plan, slotSvcID, "web", "", "app:1",
			nil, nil, 3000, "", "", nil, "", "", service.AccessControl{}, rollout.Security{}, func(m string) { warned = append(warned, m) }))
		return warned
	}

	t.Run("API 1.48", func(t *testing.T) {
		f := fakedocker.New(t)
		dc, err := client.NewClientWithOpts(client.WithHost("tcp://"+f.Srv.Listener.Addr().String()), client.WithVersion("1.48"))
		require.NoError(t, err)
		assert.Empty(t, run(t, f, dc))
		c := f.ByName(slotBlue)
		require.NotNil(t, c)
		assert.Contains(t, c.Mounts, mount.Mount{Type: mount.TypeImage, Source: rollout.ProbeToolImage, Target: rollout.ProbeMountPath, ReadOnly: true})
	})

	t.Run("API 1.47", func(t *testing.T) {
		f := fakedocker.New(t)
		warned := run(t, f, f.Client())
		require.Len(t, warned, 1)
		assert.Contains(t, warned[0], "checked at deploy only")
		assert.Empty(t, f.ByName(slotBlue).Mounts)
	})
}

func TestParseHealthTest(t *testing.T) {
	assert.Equal(t, service.HealthTestResult{Status: 200, Millis: 12}, parseHealthTest("200 0.012345\n"))
	assert.Equal(t, service.HealthTestResult{Status: 404, Millis: 3}, parseHealthTest("404 0.003"))
	r := parseHealthTest("000 5.001")
	assert.Equal(t, 0, r.Status)
	assert.Contains(t, r.Detail, "no answer")
	pulled := "Unable to find image 'curlimages/curl@sha256:58ad' locally\n" +
		"docker.io/curlimages/curl@sha256:58ad: Pulling from curlimages/curl\nStatus: Downloaded newer image\n200 0.001"
	assert.Equal(t, service.HealthTestResult{Status: 200, Millis: 1}, parseHealthTest(pulled), "a first test pulls the probe image")
	r = parseHealthTest("docker: Error response from daemon: No such container: web-blue.\n")
	assert.Equal(t, 0, r.Status)
	assert.Contains(t, r.Detail, "No such container")
}

// The path is the user's, and reaches the remote shell quoted.
func TestHealthTestArgsKeepThePathOneArgument(t *testing.T) {
	args := healthTestArgs("container:web-blue", "localhost", "/health; rm -rf /", 8080, 5)
	assert.Equal(t, "http://localhost:8080/health; rm -rf /", args[len(args)-1])
	assert.Equal(t, `'http://localhost:8080/health; rm -rf /'`, shellEscape(args[len(args)-1]))
}
