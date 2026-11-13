package hoststats

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// bucketLines is one Traefik series' histogram as its metrics page prints
// it: cumulative counts per bound of LatencyBounds, then +Inf.
func bucketLines(labels string, cumulative ...string) []string {
	les := append(strings.Split(LatencyBounds, ","), "+Inf")
	out := make([]string, 0, len(les))
	for i, le := range les {
		out = append(out, `traefik_service_request_duration_seconds_bucket{`+labels+`,le="`+le+`"} `+cumulative[i])
	}
	return out
}

// page is a sixth section as the script prints it over SSH: the lines the
// grep kept from the page of the pinned Traefik (seen in the spike, 2026-11-05).
func page() []string {
	lines := []string{"process_start_time_seconds 1.79388842277e+09"}
	lines = append(lines, bucketLines(`code="200",method="GET",protocol="http",service="paas-svc-aaaaaaaa@file"`, "2", "2", "3", "3", "3", "3", "3", "3", "3", "3", "3", "3")...)
	lines = append(lines, bucketLines(`code="500",method="POST",protocol="http",service="paas-svc-aaaaaaaa@file"`, "0", "0", "0", "1", "1", "1", "1", "1", "1", "1", "1", "1")...)
	// Streams: their duration is the stream's, not an answer's.
	lines = append(lines, bucketLines(`code="200",method="GET",protocol="sse",service="paas-svc-aaaaaaaa@file"`, "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "1", "9")...)
	lines = append(lines, bucketLines(`code="200",method="GET",protocol="websocket",service="paas-svc-aaaaaaaa@file"`, "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "4")...)
	lines = append(lines, bucketLines(`code="200",method="GET",protocol="http",service="paas-svc-dddddddd@docker"`, "1", "1", "1", "1", "1", "1", "1", "1", "1", "1", "1", "1.000000e+06")...)
	// Traefik's own, and a tenant-named service: never a Toft service.
	lines = append(lines, bucketLines(`code="200",method="GET",protocol="http",service="api@internal"`, "5", "5", "5", "5", "5", "5", "5", "5", "5", "5", "5", "5")...)
	lines = append(lines, bucketLines(`code="200",method="GET",protocol="http",service="paas-svc-evil-name@docker"`, "5", "5", "5", "5", "5", "5", "5", "5", "5", "5", "5", "5")...)
	return lines
}

func TestParseLatencySumsOverCodesAndMethods(t *testing.T) {
	l := ParseLatency(page())
	if l.Unmeasured != "" {
		t.Fatalf("unmeasured %q", l.Unmeasured)
	}
	if l.Start != 1.79388842277e+09 {
		t.Errorf("start %v", l.Start)
	}
	want := []uint64{2, 2, 3, 4, 4, 4, 4, 4, 4, 4, 4, 4}
	if got := l.Services["paas-svc-aaaaaaaa"]; !equal(got, want) {
		t.Errorf("aaaaaaaa %v, want %v (200 GET and 500 POST summed, streams left out)", got, want)
	}
	if got := l.Services["paas-svc-dddddddd"]; len(got) != 12 || got[11] != 1000000 {
		t.Errorf("dddddddd %v, want a +Inf of 1000000 (Prometheus' exponent form)", got)
	}
}

func TestParseLatencyIgnoresServicesThatAreNotToft(t *testing.T) {
	l := ParseLatency(page())
	if len(l.Services) != 2 {
		t.Errorf("services %v, want only paas-svc-aaaaaaaa and paas-svc-dddddddd", l.Services)
	}
}

func TestParseLatencyDropsStreams(t *testing.T) {
	l := ParseLatency(page())
	if got := l.Services["paas-svc-aaaaaaaa"][11]; got != 4 {
		t.Errorf("+Inf %d, want 4: sse and websocket answers are not counted", got)
	}
}

// A Traefik started with other buckets (the panel's, before its compose
// file was applied; another release's) is not measured: its counts would
// land in the wrong buckets.
func TestParseLatencyMismatchedBoundsReadAsNone(t *testing.T) {
	old := []string{"process_start_time_seconds 1.7e+09"}
	for _, le := range []string{"0.1", "0.3", "1.2", "5", "+Inf"} {
		old = append(old, `traefik_service_request_duration_seconds_bucket{code="200",method="GET",protocol="http",service="paas-svc-aaaaaaaa@docker",le="`+le+`"} 1`)
	}
	if l := ParseLatency(old); l.Unmeasured != UnmeasuredTraefik || l.Services != nil {
		t.Errorf("%+v, want unmeasured %q", l, UnmeasuredTraefik)
	}
	// An agent that compacted with other bounds.
	compact := []string{"latency\t0.1,0.3,1.2,5\t1.7e+09", "paas-svc-aaaaaaaa\t1 1 1 1 1"}
	if l := ParseLatency(compact); l.Unmeasured != UnmeasuredTraefik || l.Services != nil {
		t.Errorf("%+v, want unmeasured %q", l, UnmeasuredTraefik)
	}
}

// The script's reason when Traefik's page did not answer.
func TestParseLatencyReasons(t *testing.T) {
	for line, want := range map[string]string{
		"unmeasured\tadopted": UnmeasuredAdopted,
		"unmeasured\ttraefik": UnmeasuredTraefik,
		"unmeasured\tforged":  UnmeasuredTraefik,
	} {
		if l := ParseLatency([]string{line}); l.Unmeasured != want {
			t.Errorf("%q: %q, want %q", line, l.Unmeasured, want)
		}
	}
	if l := ParseLatency(nil); l.Unmeasured != UnmeasuredTraefik {
		t.Errorf("an empty section: %q, want %q", l.Unmeasured, UnmeasuredTraefik)
	}
}

// The agent keeps the compact form: one line per service, which reads as
// the raw lines do, and compacting it again changes nothing.
func TestCompactLatency(t *testing.T) {
	out := strings.Join([]string{"listing", Separator, "stats", Separator, "counters", Separator, "kills", Separator, "restarts", Separator}, "\n") +
		"\n" + strings.Join(page(), "\n") + "\n"
	compact := CompactLatency(out)
	if len(compact) >= len(out)/4 {
		t.Errorf("compact is %d bytes of %d", len(compact), len(out))
	}
	if !strings.HasPrefix(compact, "listing\n@@\nstats\n@@\ncounters\n@@\nkills\n@@\nrestarts\n@@\n") {
		t.Errorf("the first five sections changed:\n%s", compact)
	}
	if again := CompactLatency(compact); again != compact {
		t.Errorf("compacting again changed it:\n%s\n---\n%s", compact, again)
	}
	sixth := strings.Split(strings.TrimSpace(compact[strings.LastIndex(compact, Separator)+len(Separator):]), "\n")
	raw, kept := ParseLatency(page()), ParseLatency(sixth)
	if kept.Start != raw.Start || len(kept.Services) != len(raw.Services) {
		t.Fatalf("compact reads %+v, raw %+v", kept, raw)
	}
	for svc, counts := range raw.Services {
		if !equal(kept.Services[svc], counts) {
			t.Errorf("%s: compact %v, raw %v", svc, kept.Services[svc], counts)
		}
	}
	// Five sections (an older script): nothing to compact.
	five := "a\n@@\nb\n@@\nc\n@@\nd\n@@\ne\n"
	if got := CompactLatency(five); got != five {
		t.Errorf("five sections changed: %q", got)
	}
}

func TestCmdReadsTraefiksLoopbackMetrics(t *testing.T) {
	cmd := Cmd(0)
	if strings.Count(cmd, "echo "+Separator) != 5 {
		t.Errorf("want six sections:\n%s", cmd)
	}
	if !strings.Contains(cmd, "docker exec paas-traefik wget -qO- -T 5 http://127.0.0.1:"+LatencyPort+"/metrics") {
		t.Errorf("the sixth section does not read Traefik's loopback entrypoint:\n%s", cmd)
	}
}

// A page of many series is read up to its first 4 MiB: the agent keeps the
// output in memory.
func TestLatencyCmdCapsThePage(t *testing.T) {
	dir := t.TempDir()
	line := `traefik_service_request_duration_seconds_bucket{service="paas-svc-aaaa1111@docker",le="+Inf"} 1`
	fake := "#!/bin/sh\nyes '" + line + "' | head -c 6000000\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", latencyCmd)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 || len(out) > 4<<20 {
		t.Errorf("read %d bytes of the page, want 1 to %d", len(out), 4<<20)
	}
}

func TestLatencyBoundsParse(t *testing.T) {
	b := LatencyBoundsSeconds()
	if len(b) != LatencyBuckets()-1 || b[0] != 0.005 || b[len(b)-1] != 10 {
		t.Fatalf("%v", b)
	}
	for i := 1; i < len(b); i++ {
		if b[i] <= b[i-1] {
			t.Fatalf("bounds not increasing: %v", b)
		}
	}
}

func equal(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
