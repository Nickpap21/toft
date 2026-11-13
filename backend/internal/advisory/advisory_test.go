package advisory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func ids(as []Advisory) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.ID)
	}
	return out
}

func TestParseRange(t *testing.T) {
	in := func(r, v string) bool {
		t.Helper()
		spans, err := parseRange(r)
		require.NoError(t, err, r)
		ver, err := parseVersion(v)
		require.NoError(t, err)
		return Advisory{spans: spans}.affects(ver)
	}
	assert.True(t, in("< 29.8.2", "29.8.1"))
	assert.False(t, in("< 29.8.2", "29.8.2"))
	assert.True(t, in("<= 25.0.18, 26.0.0 - 29.8.1", "25.0.18"))
	assert.False(t, in("<= 25.0.18, 26.0.0 - 29.8.1", "25.0.19"))
	assert.True(t, in("<= 25.0.18, 26.0.0 - 29.8.1", "29.8.1"))
	assert.False(t, in("<= 25.0.18, 26.0.0 - 29.8.1", "29.8.2"))
	// A lower bound pairs with the next upper bound.
	assert.True(t, in(">= v25, < v25.0.2", "25.0.1"))
	assert.False(t, in(">= v25, < v25.0.2", "24.0.9"))
	assert.False(t, in("> v26.0.0", "26.0.0"))
	assert.True(t, in("> v26.0.0", "30.1.0"))
	assert.True(t, in("28.2.x, 28.3.0 - 28.3.2", "28.2.7"))
	assert.False(t, in("28.2.x, 28.3.0 - 28.3.2", "28.3.3"))
	assert.True(t, in("26.0.0, 26.0.1", "26.0.1"))
	assert.False(t, in("26.0.0, 26.0.1", "26.0.2"))
	assert.True(t, in("< 19.03.15, >= 20.0.0, < 20.10.3", "19.03.14"))
	assert.False(t, in("< 19.03.15, >= 20.0.0, < 20.10.3", "19.03.15"))
	assert.True(t, in("< 26.0.0-rc3", "25.0.5"))

	for _, bad := range []string{"", "N/A", "<= latest", "28.2.1.x", "1.2.3.4"} {
		_, err := parseRange(bad)
		assert.Error(t, err, bad)
	}
}

func TestKnownReads(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range mustKnown() {
		assert.False(t, seen[a.ID], "twice: %s", a.ID)
		seen[a.ID] = true
		assert.Contains(t, severityRank, a.Severity, a.ID)
	}
	assert.Len(t, seen, 27)
}

func TestAffecting(t *testing.T) {
	b := New("", zap.NewNop())
	// Production's app server on 2026-10-20.
	got := b.Affecting("29.4.3")
	assert.Equal(t, []string{"GHSA-6jhp-7446-hf7f", "GHSA-7cfq-22r6-qp73", "GHSA-rg2x-37c3-w2rh", "GHSA-x86f-5xw2-fm2r", "GHSA-6m9p-4h64-m6vh", "GHSA-vp62-88p7-qqf5"}, ids(got))
	assert.Equal(t, "https://github.com/moby/moby/security/advisories/GHSA-6jhp-7446-hf7f", got[0].URL)
	assert.Equal(t, []string{"GHSA-6jhp-7446-hf7f"}, ids(b.Affecting("29.8.2")))
	assert.Empty(t, b.Affecting("29.9.3"))
	assert.Empty(t, b.Affecting(""))
	assert.NotNil(t, b.Affecting(""), "an empty list, not null, in JSON")
	assert.Contains(t, ids(b.Affecting("27.1.0")), "GHSA-v23v-6jw2-98fq")
	assert.NotContains(t, ids(b.Affecting("27.1.1")), "GHSA-v23v-6jw2-98fq")
}

// feed is GitHub's answer, trimmed: one shipped advisory, one published
// after the release, one withdrawn, one whose range doesn't read.
const feed = `[
 {"ghsa_id":"GHSA-7cfq-22r6-qp73","severity":"high","summary":"x","withdrawn_at":null,
  "vulnerabilities":[{"package":{"ecosystem":"","name":"Docker Engine"},"vulnerable_version_range":"< 29.8.2","patched_versions":"29.8.2"}]},
 {"ghsa_id":"GHSA-new1-new1-new1","severity":"critical","summary":"A new one","withdrawn_at":null,
  "vulnerabilities":[
   {"package":{"ecosystem":"go","name":"github.com/moby/moby/v2"},"vulnerable_version_range":"< v2.0.0-beta.30","patched_versions":"v2.0.0-beta.30"},
   {"package":{"ecosystem":"","name":"Docker Engine"},"vulnerable_version_range":"< 29.9.1","patched_versions":"29.9.1"}]},
 {"ghsa_id":"GHSA-gone-gone-gone","severity":"high","summary":"Withdrawn","withdrawn_at":"2026-10-29T00:00:00Z",
  "vulnerabilities":[{"package":{"ecosystem":"","name":"Docker Engine"},"vulnerable_version_range":"< 30.0.0","patched_versions":"30.0.0"}]},
 {"ghsa_id":"GHSA-gomo-gomo-gomo","severity":"high","summary":"Go module only","withdrawn_at":null,
  "vulnerabilities":[{"package":{"ecosystem":"go","name":"github.com/moby/moby/v2"},"vulnerable_version_range":"< v2.0.0-beta.31","patched_versions":"v2.0.0-beta.31"}]},
 {"ghsa_id":"GHSA-odd1-odd1-odd1","severity":"moderate","summary":"Odd","withdrawn_at":null,
  "vulnerabilities":[{"package":{"ecosystem":"","name":"Docker Engine"},"vulnerable_version_range":"some releases","patched_versions":""}]}
]`

func TestFetchAddsNewOnes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/vnd.github+json", r.Header.Get("Accept"))
		_, _ = w.Write([]byte(feed))
	}))
	defer srv.Close()
	b := New(srv.URL, zap.NewNop())
	require.NoError(t, b.fetch(context.Background()))

	got := b.Affecting("29.9.0")
	require.Equal(t, []string{"GHSA-new1-new1-new1", "GHSA-6jhp-7446-hf7f"}, ids(got))
	assert.Equal(t, Advisory{ID: "GHSA-new1-new1-new1", Severity: "critical", Summary: "A new one", FixedIn: "29.9.1",
		URL: "https://github.com/moby/moby/security/advisories/GHSA-new1-new1-new1", spans: got[0].spans}, got[0])
	// The shipped one isn't doubled.
	assert.Equal(t, []string{"GHSA-new1-new1-new1", "GHSA-6jhp-7446-hf7f", "GHSA-7cfq-22r6-qp73", "GHSA-6m9p-4h64-m6vh"}, ids(b.Affecting("29.8.1")))
}

func TestFetchFailureKeepsWhatItHad(t *testing.T) {
	ok := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ok {
			http.Error(w, "rate limited", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(feed))
	}))
	defer srv.Close()
	b := New(srv.URL, zap.NewNop())
	require.NoError(t, b.fetch(context.Background()))
	ok = false
	require.Error(t, b.fetch(context.Background()))
	assert.Equal(t, []string{"GHSA-new1-new1-new1", "GHSA-6jhp-7446-hf7f"}, ids(b.Affecting("29.9.0")))
}
