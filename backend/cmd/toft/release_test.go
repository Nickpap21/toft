package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readRun(t *testing.T, u *upgrader) upgradeRun {
	t.Helper()
	var r upgradeRun
	b, err := os.ReadFile(filepath.Join(u.p.work, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// The panel follows an upgrade through run.json and run.out (D-68).
func TestUpgrade_RecordsTheRun(t *testing.T) {
	h, u, out := installed(t)
	if err := u.upgrade(context.Background(), "v0.2.0", false); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	r := readRun(t, u)
	if r.To != "v0.2.0" || r.From != "v0.1.0" || r.Outcome != "succeeded" || r.Step != "health" || r.FinishedAt == nil || r.Log == "" {
		t.Fatalf("run = %+v", r)
	}
	b, _ := os.ReadFile(filepath.Join(u.p.work, "run.out"))
	if !strings.Contains(string(b), "Upgrading Toft v0.1.0 → v0.2.0") || !strings.Contains(string(b), "Toft v0.2.0 is running.") {
		t.Fatalf("run.out:\n%s", b)
	}
	if env, _ := readEnv(u.p.secrets); env["TOFT_UPGRADE_DIR"] != u.p.work {
		t.Fatalf("TOFT_UPGRADE_DIR = %q", env["TOFT_UPGRADE_DIR"])
	}

	h.healthy = func(name, image string) string {
		if name == "paas-backend" && strings.Contains(image, "v0.3.0") {
			return "unhealthy"
		}
		return "healthy"
	}
	_ = u.upgrade(context.Background(), "v0.3.0", false)
	if r := readRun(t, u); r.Outcome != "rolled_back" || r.Step != "health" || !strings.Contains(r.Error, "rolled back") {
		t.Fatalf("run = %+v", r)
	}

	h.healthy = nil
	h.failAtlas = true
	_ = u.upgrade(context.Background(), "v0.4.0", false)
	if r := readRun(t, u); r.Outcome != "failed" || r.Step != "migrate" {
		t.Fatalf("run = %+v", r)
	}
	b, _ = os.ReadFile(filepath.Join(u.p.work, "run.out"))
	if strings.Contains(string(b), "v0.2.0 is running") {
		t.Fatal("run.out keeps only the last upgrade")
	}
}

func TestNewestRelease(t *testing.T) {
	if got := newestRelease([]string{"latest", "v0.1.2", "sha-abc", "v0.10.0", "v0.9.9", "0.11.0", "v0.11.0-rc1"}); got != "v0.10.0" {
		t.Fatalf("newestRelease = %q", got)
	}
	if got := newestRelease([]string{"latest"}); got != "" {
		t.Fatalf("newestRelease = %q", got)
	}
}

// A registry that wants a bearer token, as GHCR does, and Docker's login.
func TestReleaseCheck(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			user, pass, ok := r.BasicAuth()
			if !ok || user != "toft" || pass != "ghp_secret" || r.URL.Query().Get("scope") != "repository:acme/paas-backend:pull" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"token":"t0k"}`))
		case "/v2/acme/paas-backend/tags/list":
			if r.Header.Get("Authorization") != "Bearer t0k" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="ghcr.io",scope="repository:acme/paas-backend:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"name":"acme/paas-backend","tags":["v0.1.0","v0.2.0","latest"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")

	_, u, out := installed(t)
	cfg := filepath.Join(t.TempDir(), "config.json")
	auth := base64.StdEncoding.EncodeToString([]byte("toft:ghp_secret"))
	write(t, cfg, `{"auths":{"`+host+`":{"auth":"`+auth+`"}}}`)
	c := registryClient{http: srv.Client(), auths: dockerAuths(cfg)}
	now := func() time.Time { return time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC) }

	if err := u.releaseCheck(context.Background(), c, host+"/acme/paas-backend", now); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var av available
	b, _ := os.ReadFile(filepath.Join(u.p.work, "available.json"))
	_ = json.Unmarshal(b, &av)
	if av.Latest != "v0.2.0" || av.Current != "v0.1.0" || !av.CheckedAt.Equal(now()) || av.Error != "" {
		t.Fatalf("available = %+v", av)
	}
	if !strings.Contains(out.String(), "Toft v0.2.0 is available; this panel runs v0.1.0.") {
		t.Errorf("output:\n%s", out)
	}

	// A refused login is recorded, and the last known release is kept.
	c.auths = map[string]string{host: "toft:wrong"}
	if err := u.releaseCheck(context.Background(), c, host+"/acme/paas-backend", now); err == nil {
		t.Fatal("a refused login passed")
	}
	b, _ = os.ReadFile(filepath.Join(u.p.work, "available.json"))
	_ = json.Unmarshal(b, &av)
	if av.Latest != "v0.2.0" || !strings.Contains(av.Error, "refused") {
		t.Fatalf("available = %+v", av)
	}
}
