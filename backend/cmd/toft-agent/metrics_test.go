package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/hoststats"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"golang.org/x/crypto/ssh"
)

// sampleOut is a sample's output with the kill window ending at until.
func sampleOut(body string, until int64) string {
	return body + "\n" + hoststats.Separator + "\n" + hoststats.Separator + "\n" + hoststats.Separator + "\nuntil\t" + strconv.FormatInt(until, 10) + "\n"
}

func ats(samples []heldSample) []int64 {
	out := make([]int64, 0, len(samples))
	for _, s := range samples {
		out = append(out, s.At)
	}
	return out
}

// askMetrics serves one request on m and returns the answer's samples.
func askMetrics(t *testing.T, m *metrics, since int64) []agentproto.MetricsSample {
	t.Helper()
	var out bytes.Buffer
	ch := struct {
		io.Reader
		io.Writer
	}{strings.NewReader(`{"since":` + strconv.FormatInt(since, 10) + "}\n"), &out}
	if err := m.serve(ch); err != nil {
		t.Fatal(err)
	}
	return decodeSamples(t, out.String())
}

func decodeSamples(t *testing.T, out string) []agentproto.MetricsSample {
	t.Helper()
	got := make([]agentproto.MetricsSample, 0)
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l == "" {
			continue
		}
		var s agentproto.MetricsSample
		if err := json.Unmarshal([]byte(l), &s); err != nil {
			t.Fatalf("%q: %v", l, err)
		}
		got = append(got, s)
	}
	return got
}

// The buffer keeps 48 h and at most maxMetricsBytes, survives a restart
// with the kill window's start, and answers what came after since, a
// bounded answer at a time.
func TestMetricsBufferCapsPersistsAndAnswersSince(t *testing.T) {
	stateDir = t.TempDir()
	now := time.Now()
	m := newMetrics()
	m.add(agentproto.MetricsSample{At: now.Add(-49 * time.Hour).Unix(), Out: sampleOut("old", 100)}, now)
	for i := int64(3); i >= 1; i-- {
		m.add(agentproto.MetricsSample{At: now.Add(-time.Duration(i) * time.Minute).Unix(), Out: sampleOut("s"+strconv.FormatInt(i, 10), 200-i)}, now)
	}
	want := []int64{now.Add(-3 * time.Minute).Unix(), now.Add(-2 * time.Minute).Unix(), now.Add(-time.Minute).Unix()}
	if got := ats(m.samples); !slices.Equal(got, want) {
		t.Fatalf("held %v, want %v: the 49 h old sample goes", got, want)
	}
	if m.since != 199 {
		t.Fatalf("since %d: the last sample's window end", m.since)
	}

	again := newMetrics()
	if got := ats(again.samples); !slices.Equal(got, want) || again.since != 199 {
		t.Fatalf("after a restart: %v since %d", got, again.since)
	}
	if got := askMetrics(t, again, want[0]); len(got) != 2 || got[0].At != want[1] || got[1].Out != sampleOut("s1", 199) {
		t.Fatalf("after %d: %+v", want[0], got)
	}
	if got := askMetrics(t, again, 0); len(got) != 3 || got[0].At != want[0] {
		t.Fatalf("all, oldest first: %+v", got)
	}

	// The byte cap drops the oldest, and an answer stops at maxMetricsAnswer.
	big := strings.Repeat("x", 1<<20)
	for i := range 20 {
		m.add(agentproto.MetricsSample{At: now.Unix() + int64(i), Out: sampleOut(big, 300)}, now)
	}
	if m.bytes > maxMetricsBytes || len(m.samples) != 15 || m.samples[0].At != now.Unix()+5 {
		t.Fatalf("held %d bytes in %d samples from %d", m.bytes, len(m.samples), m.samples[0].At-now.Unix())
	}
	if st, err := os.Stat(metricsFile()); err != nil || st.Size() != int64(m.file) || m.file > 2*m.bytes {
		t.Fatalf("the file holds %v bytes (%v), the buffer %d", st.Size(), err, m.bytes)
	}
	if got := askMetrics(t, m, 0); len(got) != 3 || got[0].At != now.Unix()+5 {
		t.Fatalf("a bounded answer: %d samples", len(got))
	}
	if got := ats(newMetrics().samples); len(got) != 15 || got[0] != now.Unix()+5 {
		t.Fatalf("after a restart: %v", got)
	}
}

// fakeSampler puts a docker CLI on the sampler's PATH that answers the
// script and records each docker events call; it fails while
// <dir>/fail-events exists.
func fakeSampler(t *testing.T) (dir string) {
	t.Helper()
	stateDir = t.TempDir()
	dir = t.TempDir()
	const id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"ps) if [ \"$2\" = -q ]; then echo " + id + "; else printf '\"paas-web-blue\"\\t\"" + testSvc + "\"\\t\"\"\\t\"\"\\t\"" + id + "\"\\n'; fi ;;\n" +
		"stats) printf 'paas-web-blue\\t1.50%%\\t10MiB / 1GiB\\n' ;;\n" +
		"inspect) [ \"$4\" = " + id + " ] && echo \"" + id + " 4\" ;;\n" +
		"events) echo \"$@\" >> " + dir + "/events; if [ -e " + dir + "/fail-events ]; then exit 1; fi ;;\n" +
		"exec) cat " + dir + "/page 2>/dev/null ;;\n" + // Traefik's metrics page (wget in its container)
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	saved := baseEnv
	t.Cleanup(func() { baseEnv = saved })
	baseEnv = append(slices.Clone(saved), "PATH="+dir+":/usr/bin:/bin")
	return dir
}

// windows returns the since and until of each docker events call.
func windows(t *testing.T, dir string) [][2]int64 {
	t.Helper()
	raw, _ := os.ReadFile(filepath.Join(dir, "events"))
	var out [][2]int64
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		f := strings.Fields(l)
		if len(f) < 5 || f[1] != "--since" || f[3] != "--until" {
			t.Fatalf("docker %q", l)
		}
		s, _ := strconv.ParseInt(f[2], 10, 64)
		u, _ := strconv.ParseInt(f[4], 10, 64)
		out = append(out, [2]int64{s, u})
	}
	return out
}

// The sampler runs the panel's script and keeps its output; each sample's
// kill window starts where the last one read ended, also after a sample
// whose docker events failed and after a restart.
func TestMetricsSamplerKeepsTheKillWindowContinuous(t *testing.T) {
	dir := fakeSampler(t)
	m := newMetrics()
	ctx := context.Background()
	m.sample(ctx)
	if err := os.WriteFile(filepath.Join(dir, "fail-events"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m.sample(ctx)
	_ = os.Remove(filepath.Join(dir, "fail-events"))
	newMetrics().sample(ctx) // a restarted agent

	w := windows(t, dir)
	if len(w) != 3 {
		t.Fatalf("windows %v", w)
	}
	if w[0][1]-w[0][0] != 60 {
		t.Fatalf("the first sample reads the last minute: %v", w[0])
	}
	if w[1][0] != w[0][1] || w[2][0] != w[0][1] {
		t.Fatalf("the window starts where the last one read ended, not at a failed one: %v", w)
	}
	got := newMetrics().samples
	if len(got) != 3 {
		t.Fatalf("%d samples kept", len(got))
	}
	out := got[0].Out
	for _, want := range []string{`"paas-web-blue"	"` + testSvc + `"`, "paas-web-blue\t1.50%\t10MiB / 1GiB", "\nhost\t", "until\t",
		"\n" + hoststats.Separator + "\n" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 4"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the sample lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(got[1].Out, "until\t") {
		t.Fatalf("a failed docker events reports no window end:\n%s", got[1].Out)
	}
}

// The agent keeps Traefik's histograms compacted, a line per service, so
// the samples it holds while the panel is away carry latency too (D-156).
func TestMetricsSamplerKeepsLatencyCompact(t *testing.T) {
	dir := fakeSampler(t)
	page := []string{"# HELP process_start_time_seconds", "process_start_time_seconds 1.7e+09"}
	for _, code := range []string{"200", "404", "500"} {
		for _, le := range append(strings.Split(hoststats.LatencyBounds, ","), "+Inf") {
			page = append(page, `traefik_service_request_duration_seconds_bucket{code="`+code+`",method="GET",protocol="http",service="paas-svc-0123abcd@file",le="`+le+`"} 2`)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "page"), []byte(strings.Join(page, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newMetrics()
	m.sample(context.Background())
	if len(m.samples) != 1 {
		t.Fatalf("%d samples", len(m.samples))
	}
	out := m.samples[0].Out
	want := "\n" + hoststats.Separator + "\nlatency\t" + hoststats.LatencyBounds + "\t1.7e+09\npaas-svc-0123abcd\t6 6 6 6 6 6 6 6 6 6 6 6\n"
	if !strings.HasSuffix(out, want) || strings.Contains(out, "traefik_service_request_duration_seconds_bucket") {
		t.Fatalf("the sample does not end with the compact latency %q:\n%s", want, out)
	}
}

// The panel asks over the agent's SSH for the samples after the last one it
// stored and gets them as JSON lines, oldest first.
func TestAgentAnswersTheMetricsRequest(t *testing.T) {
	stateDir = t.TempDir()
	ro := testRollouts(fakedocker.New(t))
	now := time.Now()
	for i := int64(3); i >= 1; i-- {
		ro.metrics.add(agentproto.MetricsSample{At: now.Unix() - 60*i, Out: sampleOut("s"+strconv.FormatInt(i, 10), 1)}, now)
	}
	pem, pub := panelKey(t)
	c, err := connectWith(t, pem, ssh.FingerprintSHA256(pub), ro)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	out, err := c.SubsystemContext(context.Background(), agentproto.SubsystemMetrics, strings.NewReader(`{"since":`+strconv.FormatInt(now.Unix()-180, 10)+"}\n"))
	if err != nil {
		t.Fatalf("metrics: %v %q", err, out)
	}
	got := decodeSamples(t, out)
	if len(got) != 2 || got[0].At != now.Unix()-120 || got[1].Out != sampleOut("s1", 1) {
		t.Fatalf("answer %+v", got)
	}
	if out, err := c.SubsystemContext(context.Background(), agentproto.SubsystemMetrics, strings.NewReader("not json\n")); err == nil || !strings.Contains(out, "error:") {
		t.Fatalf("a bad request must be refused: %q %v", out, err)
	}
}
