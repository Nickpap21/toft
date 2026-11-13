// Package advisory says which of Docker's published security advisories
// affect a Docker Engine release (D-133): moby/moby's GitHub advisories, as
// a hand-checked list shipped with each release (known.go) and those
// published since, fetched daily.
package advisory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Advisory is one published advisory against Docker Engine.
type Advisory struct {
	ID       string `json:"id"`
	Severity string `json:"severity"` // low, medium, high or critical
	Summary  string `json:"summary"`
	FixedIn  string `json:"fixed_in"`
	URL      string `json:"url"`
	spans    []span
}

// version is a Docker Engine release: 29.8.2, 19.03.15.
type version [3]int

// span is the releases from from up to to; from's zero value is the first.
type span struct {
	from, to version
	toIncl   bool
}

func (s span) has(v version) bool {
	c := cmp(v, s.to)
	return cmp(v, s.from) >= 0 && (c < 0 || s.toIncl && c == 0)
}

func cmp(a, b version) int {
	for i := range a {
		if a[i] != b[i] {
			return a[i] - b[i]
		}
	}
	return 0
}

var last = version{1 << 30}

// parseVersion reads 29.8.2, v25, 26.0.0-rc3 (as 26.0.0) or 1.12.
func parseVersion(s string) (version, error) {
	var v version
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+~"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) > 3 || s == "" {
		return v, fmt.Errorf("not a release: %q", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, fmt.Errorf("not a release: %q", s)
		}
		v[i] = n
	}
	return v, nil
}

// parseRange reads a vulnerable range as moby's advisories write it. A comma
// separates spans ("<= 25.0.18, 26.0.0 - 29.8.1") except after a lower bound,
// which the next upper bound closes (">= v25, < v25.0.2"). Also "A - B",
// "28.2.x" and a release on its own.
func parseRange(r string) ([]span, error) {
	var out []span
	var open *version
	for _, p := range strings.Split(r, ",") {
		p = strings.TrimSpace(p)
		op := ""
		for _, o := range []string{"<=", ">=", "<", ">"} {
			if strings.HasPrefix(p, o) {
				op, p = o, strings.TrimSpace(p[len(o):])
				break
			}
		}
		if open != nil && op != "<" && op != "<=" {
			out, open = append(out, span{from: *open, to: last}), nil
		}
		switch {
		case op == "<" || op == "<=":
			v, err := parseVersion(p)
			if err != nil {
				return nil, err
			}
			s := span{to: v, toIncl: op == "<="}
			if open != nil {
				s.from, open = *open, nil
			}
			out = append(out, s)
		case op == ">=" || op == ">":
			v, err := parseVersion(p)
			if err != nil {
				return nil, err
			}
			if op == ">" {
				v[2]++
			}
			open = &v
		case strings.Contains(p, " - "):
			a, b, _ := strings.Cut(p, " - ")
			from, err := parseVersion(a)
			if err != nil {
				return nil, err
			}
			to, err := parseVersion(b)
			if err != nil {
				return nil, err
			}
			out = append(out, span{from: from, to: to, toIncl: true})
		case strings.HasSuffix(p, ".x"):
			v, err := parseVersion(strings.TrimSuffix(p, ".x"))
			if err != nil || v[2] != 0 {
				return nil, fmt.Errorf("not a release series: %q", p)
			}
			to := v
			to[1]++
			out = append(out, span{from: v, to: to})
		default:
			v, err := parseVersion(p)
			if err != nil {
				return nil, err
			}
			out = append(out, span{from: v, to: v, toIncl: true})
		}
	}
	if open != nil {
		out = append(out, span{from: *open, to: last})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no range in %q", r)
	}
	return out, nil
}

func (a Advisory) affects(v version) bool {
	for _, s := range a.spans {
		if s.has(v) {
			return true
		}
	}
	return false
}

func advisoryURL(id string) string {
	return "https://github.com/moby/moby/security/advisories/" + id
}

// Book holds the advisories: the shipped ones and those fetched since.
type Book struct {
	known   []Advisory
	client  *http.Client
	feedURL string
	log     *zap.Logger

	mu      sync.RWMutex
	fetched []Advisory
}

// FeedURL is moby/moby's repository advisories on GitHub's API.
const FeedURL = "https://api.github.com/repos/moby/moby/security-advisories?per_page=100"

// New is the shipped list, with feedURL fetched by Run.
func New(feedURL string, log *zap.Logger) *Book {
	return &Book{known: mustKnown(), client: &http.Client{Timeout: 30 * time.Second}, feedURL: feedURL, log: log}
}

var severityRank = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}

// Affecting is every advisory that affects the release, most severe first;
// none for a release it can't read (Docker didn't answer).
func (b *Book) Affecting(release string) []Advisory {
	out := make([]Advisory, 0)
	v, err := parseVersion(release)
	if err != nil {
		return out
	}
	b.mu.RLock()
	all := slices.Concat(b.known, b.fetched)
	b.mu.RUnlock()
	for _, a := range all {
		if a.affects(v) {
			out = append(out, a)
		}
	}
	slices.SortStableFunc(out, func(x, y Advisory) int { return severityRank[x.Severity] - severityRank[y.Severity] })
	return out
}

// Run fetches the feed at start and daily, an hour after a failure, until
// ctx ends. A panel without the internet warns once, not every hour.
func (b *Book) Run(ctx context.Context) {
	failing := false
	for {
		wait := 24 * time.Hour
		if err := b.fetch(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			if !failing {
				b.log.Warn("fetch Docker's advisories; the shipped list still applies, and it retries hourly", zap.Error(err))
			}
			failing, wait = true, time.Hour
		} else {
			failing = false
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// feedAdvisory is the part of GitHub's repository advisory read here.
type feedAdvisory struct {
	ID              string  `json:"ghsa_id"`
	Severity        string  `json:"severity"`
	Summary         string  `json:"summary"`
	WithdrawnAt     *string `json:"withdrawn_at"`
	Vulnerabilities []struct {
		Package struct {
			Name string `json:"name"`
		} `json:"package"`
		Range   string `json:"vulnerable_version_range"`
		Patched string `json:"patched_versions"`
	} `json:"vulnerabilities"`
}

func (b *Book) fetch(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.feedURL, nil)
	if err != nil {
		return fmt.Errorf("build the request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("get the feed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("get the feed: %s", resp.Status)
	}
	var feed []feedAdvisory
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&feed); err != nil {
		return fmt.Errorf("decode the feed: %w", err)
	}
	fetched := b.newOnes(feed)
	b.mu.Lock()
	b.fetched = fetched
	b.mu.Unlock()
	return nil
}

// newOnes is the feed's advisories the shipped list lacks, from their
// "Docker Engine" entries; one without such an entry, or whose range doesn't
// read, is left out, and logged, until a release ships it in the list.
func (b *Book) newOnes(feed []feedAdvisory) []Advisory {
	out := make([]Advisory, 0)
	for _, f := range feed {
		if f.WithdrawnAt != nil || slices.ContainsFunc(b.known, func(a Advisory) bool { return a.ID == f.ID }) {
			continue
		}
		engine := false
		for _, v := range f.Vulnerabilities {
			if v.Package.Name != "Docker Engine" {
				continue
			}
			engine = true
			spans, err := parseRange(v.Range)
			if err != nil {
				b.log.Warn("a Docker advisory's range doesn't read", zap.String("id", f.ID), zap.Error(err))
				break
			}
			sev := strings.ToLower(f.Severity)
			if sev == "moderate" {
				sev = "medium"
			}
			if _, ok := severityRank[sev]; !ok {
				sev = "medium"
			}
			out = append(out, Advisory{ID: f.ID, Severity: sev, Summary: cut(strings.TrimSpace(f.Summary), 300),
				FixedIn: cut(strings.TrimSpace(strings.TrimPrefix(v.Patched, ">=")), 40), URL: advisoryURL(f.ID), spans: spans})
			break
		}
		if !engine {
			b.log.Warn("a Docker advisory names no Docker Engine releases", zap.String("id", f.ID))
		}
	}
	return out
}

func cut(s string, n int) string {
	if len(s) > n {
		return strings.ToValidUTF8(s[:n], "")
	}
	return s
}
