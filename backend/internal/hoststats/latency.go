package hoststats

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Per-service request latency (D-156): every Traefik Toft starts keeps a
// histogram of each service's request durations, read by Cmd's sixth
// section on remote servers and over HTTP on the panel host.

// LatencyBounds are the upper bounds, in seconds, of the request duration
// buckets every Traefik Toft starts keeps: the remote servers'
// (server.traefikConfig) and the panel host's (docker-compose.yml). A
// histogram read with other bounds is not measured.
const LatencyBounds = "0.005,0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5,10"

// LatencyPort is a remote Traefik's metrics entrypoint, bound to its
// container's loopback: neither the web, the app networks nor the host
// reach it, only a process inside the container (docker exec).
const LatencyPort = "8083"

// Why a server's latency was not measured.
const (
	// UnmeasuredAgent: the sample has no sixth section, so the server's
	// agent predates it.
	UnmeasuredAgent = "agent"
	// UnmeasuredAdopted: Traefik's page did not answer, and Toft did not
	// start that Traefik (it has no paas-traefik-acme volume).
	UnmeasuredAdopted = "adopted"
	// UnmeasuredTraefik: the Traefik Toft started did not answer with
	// LatencyBounds' buckets: started before them (it restarts with them,
	// cutOverRoutes), stopped, or its page did not answer.
	UnmeasuredTraefik = "traefik"
)

// latencyCmd prints the sixth section: the lines of Traefik's metrics page
// Toft reads, or "unmeasured\t<why>". It reads at most the page's first 4
// MiB, so a page with many series can't outgrow the agent's memory
// (GOMEMLIMIT); a cut-off page drops series, which the collector keeps the
// last counts of. A service's series multiply by status code and method,
// and the method can't be chosen freely: Traefik v3.6 labels every method
// outside RFC 7231's, PATCH and PRI as EXTENSION_METHOD (getMethod in
// pkg/middlewares/metrics/metrics.go), a non-UTF-8 one as
// NON_UTF8_HTTP_METHOD.
const latencyCmd = `{ docker exec paas-traefik wget -qO- -T 5 http://127.0.0.1:` + LatencyPort + `/metrics 2>/dev/null | head -c 4194304` +
	` | grep -E '^(traefik_service_request_duration_seconds_bucket\{.*service="paas-svc-|process_start_time_seconds )'` +
	` || { docker inspect --format '{{range .Mounts}}{{.Name}},{{end}}' paas-traefik 2>/dev/null | grep -q 'paas-traefik-acme,'` +
	` && printf 'unmeasured\t` + UnmeasuredTraefik + `\n' || printf 'unmeasured\t` + UnmeasuredAdopted + `\n'; }; }`

// LatencyBuckets is how many buckets a histogram has: one per bound of
// LatencyBounds, then the one above them.
func LatencyBuckets() int { return len(latencyBounds) }

// LatencyBoundsSeconds are LatencyBounds as numbers.
func LatencyBoundsSeconds() []float64 {
	out := make([]float64, 0, len(latencyBounds)-1)
	for _, b := range latencyBounds[:len(latencyBounds)-1] {
		v, _ := strconv.ParseFloat(b, 64) // a constant: TestLatencyBoundsParse
		out = append(out, v)
	}
	return out
}

// Latency is a server's Traefik's histograms at one sample.
type Latency struct {
	// Unmeasured says why there are none; "" when they were read.
	Unmeasured string
	// Start is Traefik's process start (unix seconds): a different one
	// means it restarted, and every counter started again.
	Start float64
	// Services are the request counts of each Toft service Traefik has
	// answered for since it started, by Traefik service name without its
	// provider (paas-svc-<id8>): cumulative, one per bound of
	// LatencyBounds, then all of them (+Inf). Summed over status codes and
	// methods; HTTP only, since an SSE or WebSocket request lasts as long as
	// its stream.
	Services map[string][]uint64
}

var (
	// latencyServicePattern is a Toft service, by either provider.
	latencyServicePattern = regexp.MustCompile(`^(paas-svc-[0-9a-f]{8})@(docker|file)$`)
	latencyBounds         = append(strings.Split(LatencyBounds, ","), "+Inf")
)

// ParseLatency reads a sixth section: the page's lines, as the script
// prints them over SSH, or the compact form the agent keeps
// (CompactLatency). Lines that do not parse are skipped.
func ParseLatency(lines []string) Latency {
	for _, l := range lines {
		if why, ok := strings.CutPrefix(l, "unmeasured\t"); ok {
			if why != UnmeasuredAdopted {
				why = UnmeasuredTraefik // the only other the script prints
			}
			return Latency{Unmeasured: why}
		}
		if _, ok := strings.CutPrefix(l, "latency\t"); ok {
			return parseCompact(lines)
		}
	}
	return parseCompact(compactLines(lines))
}

// CompactLatency returns Cmd's output with its sixth section in the compact
// form: a header with the bounds and Traefik's start, then a line per
// service with its cumulative counts. The agent keeps samples this way: a
// service's lines on the page take about 2 KB per status code and method,
// its compact line under 100 bytes. Output without a sixth section, or with
// a compact one, is returned as is.
func CompactLatency(out string) string {
	lines := strings.Split(out, "\n")
	section, start := 0, -1
	for i, l := range lines {
		if strings.TrimSpace(strings.TrimRight(l, "\r")) == Separator {
			if section++; section == 5 {
				start = i + 1
				break
			}
		}
	}
	if start < 0 {
		return out
	}
	sixth := lines[start:]
	for _, l := range sixth {
		if strings.HasPrefix(l, "latency\t") || strings.HasPrefix(l, "unmeasured\t") {
			return out
		}
	}
	return strings.Join(append(lines[:start:start], compactLines(sixth)...), "\n") + "\n"
}

// compactLines turns the page's lines into the compact form; a histogram
// with other bounds, or no answer at all, becomes "unmeasured".
func compactLines(lines []string) []string {
	start, seen := "", false
	sums := make(map[string][]float64)
	for _, l := range lines {
		l = strings.TrimRight(l, "\r")
		if v, ok := strings.CutPrefix(l, "process_start_time_seconds "); ok {
			start, seen = strings.TrimSpace(v), true
			continue
		}
		rest, ok := strings.CutPrefix(l, "traefik_service_request_duration_seconds_bucket{")
		if !ok {
			continue
		}
		labels, value, ok := strings.Cut(rest, "} ")
		if !ok {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || n < 0 || math.IsInf(n, 0) {
			continue
		}
		lv := parseLabels(labels)
		m := latencyServicePattern.FindStringSubmatch(lv["service"])
		if m == nil || lv["protocol"] != "http" {
			continue
		}
		seen = true
		i := indexOf(latencyBounds, lv["le"])
		if i < 0 {
			return []string{"unmeasured\t" + UnmeasuredTraefik} // other bounds
		}
		if sums[m[1]] == nil {
			sums[m[1]] = make([]float64, len(latencyBounds))
		}
		sums[m[1]][i] += n
	}
	if !seen {
		return []string{"unmeasured\t" + UnmeasuredTraefik}
	}
	out := make([]string, 0, len(sums)+1)
	out = append(out, "latency\t"+LatencyBounds+"\t"+start)
	for _, svc := range sortedNames(sums) {
		counts := make([]string, len(latencyBounds))
		for i, n := range sums[svc] {
			counts[i] = strconv.FormatUint(uint64(n), 10)
		}
		out = append(out, svc+"\t"+strings.Join(counts, " "))
	}
	return out
}

// parseCompact reads the compact form. Other bounds read as not measured;
// a service line that does not parse is skipped.
func parseCompact(lines []string) Latency {
	l := Latency{Unmeasured: UnmeasuredTraefik}
	for _, line := range lines {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) == 2 && f[0] == "unmeasured" {
			return ParseLatency([]string{line})
		}
		if len(f) == 3 && f[0] == "latency" {
			if f[1] != LatencyBounds {
				return Latency{Unmeasured: UnmeasuredTraefik}
			}
			l.Unmeasured, l.Services = "", make(map[string][]uint64)
			l.Start, _ = strconv.ParseFloat(f[2], 64)
			continue
		}
		if len(f) != 2 || l.Services == nil || !strings.HasPrefix(f[0], "paas-svc-") {
			continue
		}
		fields := strings.Fields(f[1])
		if len(fields) != len(latencyBounds) {
			continue
		}
		counts := make([]uint64, len(fields))
		ok := true
		for i, s := range fields {
			n, err := strconv.ParseUint(s, 10, 64)
			if err != nil || i > 0 && n < counts[i-1] { // cumulative: never down
				ok = false
				break
			}
			counts[i] = n
		}
		if ok {
			l.Services[f[0]] = counts
		}
	}
	return l
}

// parseLabels parses a Prometheus label set (a="x",b="y"); a value keeps
// its escapes, which no label Toft reads has.
func parseLabels(s string) map[string]string {
	out := make(map[string]string, 5)
	for s != "" {
		k, rest, ok := strings.Cut(s, `="`)
		if !ok {
			break
		}
		end := 0
		for end < len(rest) && (rest[end] != '"' || end > 0 && rest[end-1] == '\\') {
			end++
		}
		if end == len(rest) {
			break
		}
		out[strings.TrimLeft(k, ",")] = rest[:end]
		s = rest[end+1:]
	}
	return out
}

func indexOf(list []string, v string) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return -1
}

func sortedNames(m map[string][]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
