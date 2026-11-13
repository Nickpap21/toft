package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/internal/hoststats"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
)

// Metrics kept while the panel is away (D-113): every sampleEvery the agent
// runs the panel's sampling script (hoststats.Cmd) and keeps its output and
// time in metricsFile, one JSON line per sample, oldest first. The panel
// asks for the samples it hasn't stored (SubsystemMetrics) and parses each
// as one it took over SSH, so a gap under keepMetrics fills on reconnect.

const (
	sampleEvery   = 60 * time.Second
	sampleTimeout = 30 * time.Second
	// keepMetrics is the panel's retention of the samples.
	keepMetrics = 48 * time.Hour
	// maxMetricsBytes caps the samples kept, as written to metricsFile. A
	// sample is about 350 bytes per running container, so 16 MiB holds the
	// 48 h of a server running about 15 containers (with ~15 routed services'
	// latency at under 100 bytes each, D-156, about 37 h: an estimate, not
	// measured); a busier server keeps
	// a shorter window instead of more memory. The agent runs under
	// GOMEMLIMIT=96MiB (D-106), holds the buffer in memory and, compacting
	// it, encodes it once more: 16 MiB leaves room for its other work.
	maxMetricsBytes = 16 << 20
	// maxSampleBytes drops a sample larger than this: the panel reads an
	// answer line of up to 1 MiB.
	maxSampleBytes = 512 << 10
	// maxMetricsAnswer bounds one answer; the panel asks for the rest a
	// minute later.
	maxMetricsAnswer = 4 << 20
)

func metricsFile() string { return filepath.Join(stateDir, "metrics.jsonl") }

// metrics is the agent's buffer of samples.
type metrics struct {
	mu      sync.Mutex
	samples []heldSample // oldest first
	bytes   int          // the held samples' lines, summed
	file    int          // metricsFile's size: held lines, and dropped ones until compacted
	// since is the next sample's kill window start: the last sample's
	// window end (hoststats.Until), kept until a sample reads its kills.
	since   int64
	failing bool // the last sample failed: logged once
}

// heldSample is a sample and the size of its line in metricsFile.
type heldSample struct {
	agentproto.MetricsSample
	size int
}

// newMetrics loads metricsFile.
func newMetrics() *metrics {
	m := &metrics{}
	f, err := os.Open(metricsFile())
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), maxMetricsBytes)
	bad := false
	for sc.Scan() {
		var s agentproto.MetricsSample
		if json.Unmarshal(sc.Bytes(), &s) != nil {
			bad = true
			continue
		}
		m.hold(s, len(sc.Bytes())+1)
		m.file += len(sc.Bytes()) + 1
	}
	if sc.Err() != nil {
		bad = true
	}
	for _, s := range m.samples {
		if u := hoststats.Until(s.Out); u != 0 {
			m.since = u
		}
	}
	if m.trim(time.Now()) || bad {
		m.compact()
	}
	return m
}

// run samples every sampleEvery until ctx ends.
func (m *metrics) run(ctx context.Context) {
	t := time.NewTicker(sampleEvery)
	defer t.Stop()
	for {
		m.sample(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sample runs the script once and keeps its output. A failed run is a gap,
// as a failed sample over SSH is.
func (m *metrics) sample(ctx context.Context) {
	at := time.Now()
	m.mu.Lock()
	since := m.since
	m.mu.Unlock()
	sctx, cancel := context.WithTimeout(ctx, sampleTimeout)
	defer cancel()
	c := exec.CommandContext(sctx, "/bin/sh", "-c", hoststats.Cmd(since))
	c.Env = baseEnv
	c.WaitDelay = 5 * time.Second // docker children holding the output open
	out, err := c.CombinedOutput()
	if err == nil {
		// Traefik's histograms, a line per service (D-156).
		out = []byte(hoststats.CompactLatency(string(out)))
	}
	if err == nil && len(out) > maxSampleBytes {
		err = fmt.Errorf("the sample is %d bytes, more than %d", len(out), maxSampleBytes)
	}
	if ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		if !m.failing {
			slog.Warn("could not sample the server's metrics; retrying every minute", "error", err, "output", string(bytes.TrimSpace(out)))
		}
		m.failing = true
		return
	}
	if m.failing {
		slog.Info("sampling the server's metrics again")
	}
	m.failing = false
	m.add(agentproto.MetricsSample{At: at.Unix(), Out: string(out)}, at)
}

// add keeps s, drops what the caps no longer hold and writes s to
// metricsFile: appended, or the whole buffer once dropped lines make up
// half the file. The caller holds mu.
func (m *metrics) add(s agentproto.MetricsSample, now time.Time) {
	if u := hoststats.Until(s.Out); u != 0 {
		m.since = u
	}
	line, err := json.Marshal(s)
	if err != nil {
		return
	}
	line = append(line, '\n')
	m.hold(s, len(line))
	m.trim(now)
	if m.file+len(line) > 2*m.bytes {
		m.compact()
		return
	}
	if err := appendFile(metricsFile(), line); err != nil {
		slog.Warn("could not keep a metrics sample on disk", "error", err)
		return
	}
	m.file += len(line)
}

func (m *metrics) hold(s agentproto.MetricsSample, size int) {
	m.samples = append(m.samples, heldSample{MetricsSample: s, size: size})
	m.bytes += size
}

// trim drops the samples older than keepMetrics, then the oldest while
// they pass maxMetricsBytes, and reports whether it dropped any.
func (m *metrics) trim(now time.Time) bool {
	cutoff := now.Add(-keepMetrics).Unix()
	n := 0
	for n < len(m.samples) && (m.samples[n].At < cutoff || m.bytes > maxMetricsBytes) {
		m.bytes -= m.samples[n].size
		n++
	}
	m.samples = m.samples[n:]
	return n > 0
}

// compact writes the held samples to metricsFile in place of what it holds.
func (m *metrics) compact() {
	var buf bytes.Buffer
	buf.Grow(m.bytes)
	for _, s := range m.samples {
		line, _ := json.Marshal(s.MetricsSample)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := writeFileAtomic(metricsFile(), buf.Bytes(), 0o600); err != nil {
		slog.Warn("could not write the metrics samples to disk", "error", err)
		return
	}
	// Copy the slice, so its dropped samples' memory is let go.
	m.samples = append([]heldSample(nil), m.samples...)
	m.file = buf.Len()
}

// serve answers SubsystemMetrics: the samples after the request's Since,
// oldest first, up to maxMetricsAnswer.
func (m *metrics) serve(ch io.ReadWriter) error {
	var req agentproto.MetricsRequest
	if err := readRequest(ch, &req); err != nil {
		return err
	}
	m.mu.Lock()
	out := make([]agentproto.MetricsSample, 0, len(m.samples))
	n := 0
	for _, s := range m.samples {
		if s.At <= req.Since {
			continue
		}
		if n += s.size; n > maxMetricsAnswer && len(out) > 0 {
			break
		}
		out = append(out, s.MetricsSample)
	}
	m.mu.Unlock()
	enc := json.NewEncoder(ch)
	for _, s := range out {
		if err := enc.Encode(s); err != nil {
			return fmt.Errorf("send the samples: %w", err)
		}
	}
	return nil
}

// appendFile appends data to path, made if missing.
func appendFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
