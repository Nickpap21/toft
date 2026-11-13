package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// The continuous health check. A service's container gets rollout.ProbeToolImage,
// a static busybox, mounted read-only at rollout.ProbeMountPath (a Docker image
// mount, API 1.48), and the alert monitor runs probeScript in it every minute
// for a web service whose health check is on. It needs nothing from the app's
// image, not even a shell, and asks the app as the deploy's check does, from
// inside its network namespace. It is not a Docker HEALTHCHECK: Traefik drops
// a container that one reports starting or unhealthy, so a flaky endpoint
// would take the service offline; this only reports.

// probeScript requests http://localhost:$1$2 with a $3-second timeout and
// judges it by the first status line, before any redirect, as curl -f does
// at deploy: below 400 is healthy (exit 0); 400 or more, or no answer, is
// unhealthy (exit 1, the reason on the next line). Either way its first
// line is "toft-probe <status> <ms>" (status 000: no answer), timed with
// busybox's microsecond clock. Any other exit code means it did not run (no
// mount: a container from before the probe).
const probeScript = `B=` + rollout.ProbeMountPath + `/bin/busybox
now() { $B adjtimex | $B awk '/tv_sec/{s=$2} /tv_usec/{u=$2} END{printf "%.0f", s*1000+u/1000}'; }
t0=$(now)
out=$($B wget -S -O /dev/null -T "$3" "http://localhost:$1$2" 2>&1)
t1=$(now)
code=$(echo "$out" | $B sed -n 's/^ *HTTP\/[0-9.]* \([0-9][0-9][0-9]\).*/\1/p' | $B head -n 1)
echo "toft-probe ${code:-000} $((t1-t0))"
[ -n "$code" ] && [ "$code" -lt 400 ] && exit 0
if [ -n "$code" ]; then echo "HTTP $code"; else echo "$out" | $B tail -n 1; fi
exit 1`

// probeConcurrency bounds the probes run at once on one server.
const probeConcurrency = 4

// probeAnswer is what one probe run measured: the status (0: no answer)
// and how long the request took.
type probeAnswer struct {
	status, millis int
}

// probeVerdict reads one probe run: its exit code and output.
func probeVerdict(exitCode int, output string) (healthy bool, detail string, ran bool, answer probeAnswer) {
	first, rest, _ := strings.Cut(output, "\n")
	if f := strings.Fields(first); len(f) == 3 && f[0] == "toft-probe" {
		answer.status, _ = strconv.Atoi(f[1])
		answer.millis, _ = strconv.Atoi(f[2])
		output = rest
	}
	switch exitCode {
	case 0:
		return true, "", true, answer
	case 1:
		detail = strings.TrimSpace(output)
		if len(detail) > 120 {
			detail = detail[:120]
		}
		return false, detail, true, answer
	default:
		return false, "", false, probeAnswer{}
	}
}

// probeContainer runs probeScript in a running container.
func probeContainer(ctx context.Context, dc *client.Client, name string, t service.MetricsTarget) (healthy bool, detail string, ran bool, answer probeAnswer) {
	timeout := t.HealthTimeout
	if timeout <= 0 || timeout > 10 {
		timeout = 10
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout+5)*time.Second)
	defer cancel()
	exec, err := dc.ContainerExecCreate(ctx, name, container.ExecOptions{
		Cmd: []string{rollout.ProbeMountPath + "/bin/busybox", "sh", "-c", probeScript, "toft-probe",
			strconv.Itoa(t.Port), t.HealthPath, strconv.Itoa(timeout)},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return false, "", false, probeAnswer{}
	}
	resp, err := dc.ContainerExecAttach(ctx, exec.ID, container.ExecAttachOptions{})
	if err != nil {
		return false, "", false, probeAnswer{}
	}
	var stdout bytes.Buffer
	_, _ = stdcopy.StdCopy(&stdout, io.Discard, io.LimitReader(resp.Reader, 4096))
	resp.Close()
	inspect, err := dc.ContainerExecInspect(ctx, exec.ID)
	if err != nil || inspect.Running {
		return false, "", false, probeAnswer{}
	}
	return probeVerdict(inspect.ExitCode, stdout.String())
}

// probeTargets runs the health check probe in each running container of the
// targets that have one (MetricsTarget.HealthPath), and marks a container
// whose check fails unhealthy, with the reason. A probe that did not run
// leaves the container as Docker reported it.
func probeTargets(ctx context.Context, dc *client.Client, targets []service.MetricsTarget, states map[string][]service.ContainerState) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, probeConcurrency)
	for _, t := range targets {
		if t.HealthPath == "" {
			continue
		}
		for i, cs := range states[t.ID] {
			if cs.State != "running" {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-sem }()
				healthy, detail, ran, answer := probeContainer(ctx, dc, cs.Name, t)
				if !ran {
					return
				}
				mu.Lock()
				defer mu.Unlock()
				states[t.ID][i].HealthAnswer = &service.HealthAnswer{
					Status: answer.status, Millis: answer.millis, Healthy: healthy, Detail: detail,
				}
				if !healthy {
					states[t.ID][i].Health = "unhealthy"
					states[t.ID][i].HealthDetail = fmt.Sprintf("GET %s: %s", t.HealthPath, detail)
				}
			}()
		}
	}
	wg.Wait()
}

// healthTestArgs are the docker arguments of one health-check test request:
// healthProbeArgs' probe container, which prints the status and the time
// instead of failing on an error status. It prints "000" when nothing
// answered. networkMode and host are rollout.ProbeTarget's.
func healthTestArgs(networkMode, host, path string, port, timeoutSec int) []string {
	return []string{
		"run", "--rm", "--network", networkMode, "--memory", "32m",
		"--security-opt", "no-new-privileges:true", "--cap-drop", "NET_RAW", "--pids-limit", "64",
		"--entrypoint", "curl", rollout.HealthProbeImage,
		"-s", "-o", "/dev/null", "-w", "%{http_code} %{time_total}", "--max-time", strconv.Itoa(timeoutSec),
		fmt.Sprintf("http://%s:%d%s", host, port, path),
	}
}

// parseHealthTest reads healthTestArgs' output, "200 0.012", into the
// status and milliseconds. curl writes it last: docker's own lines come
// first (it pulls the probe image on a server that lacks it). Anything else
// (docker's own error) is returned as the detail with status 0.
func parseHealthTest(out string) service.HealthTestResult {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) >= 2 && len(fields[0]) == 3 {
		status, err1 := strconv.Atoi(fields[0])
		secs, err2 := strconv.ParseFloat(fields[1], 64)
		if err1 == nil && err2 == nil {
			r := service.HealthTestResult{Status: status, Millis: int(secs*1000 + 0.5)}
			if status == 0 {
				r.Detail = "no answer on the port (nothing listening, or the timeout passed)"
			}
			return r
		}
	}
	detail := strings.TrimSpace(out)
	if len(detail) > 200 {
		detail = detail[:200]
	}
	return service.HealthTestResult{Detail: detail}
}

// TestHealthCheck asks the container for path once, as the deploy's check
// does, and reports what it answered.
func (o *LocalOrchestrator) TestHealthCheck(ctx context.Context, containerName, path string, port, timeoutSec int) (service.HealthTestResult, error) {
	networkMode, host, err := rollout.ProbeTarget(ctx, o.dockerClient, containerName)
	if err != nil {
		return parseHealthTest(err.Error()), nil
	}
	out, err := exec.CommandContext(ctx, "docker", healthTestArgs(networkMode, host, path, port, timeoutSec)...).CombinedOutput()
	if ctx.Err() != nil {
		return service.HealthTestResult{}, ctx.Err()
	}
	_ = err // curl exits non-zero when nothing answers; the output says what happened
	return parseHealthTest(string(out)), nil
}

// TestHealthCheck asks the container for path once, as the deploy's check
// does, and reports what it answered.
func (o *RemoteOrchestrator) TestHealthCheck(ctx context.Context, containerName, path string, port, timeoutSec int) (service.HealthTestResult, error) {
	dc, err := o.host.DockerClient()
	if err != nil {
		return service.HealthTestResult{}, fmt.Errorf("remote health check test: %w", err)
	}
	networkMode, host, err := rollout.ProbeTarget(ctx, dc, containerName)
	if err != nil {
		return parseHealthTest(err.Error()), nil
	}
	args := healthTestArgs(networkMode, host, path, port, timeoutSec)
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellEscape(a) // path is user input
	}
	// "; true": curl exits non-zero when nothing answers, and the output
	// says what happened either way.
	out, err := o.host.Exec(ctx, "docker "+strings.Join(quoted, " ")+" 2>&1; true")
	if err != nil {
		return service.HealthTestResult{}, fmt.Errorf("remote health check test: %w", err)
	}
	return parseHealthTest(out), nil
}
