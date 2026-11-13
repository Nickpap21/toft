package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A deploy by project/environment/name, followed to its end; a refused
// deploy prints the problem's detail and code and exits 1.
func TestDeployWaitAndProblem(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer paas_k" || r.Header.Get("X-Toft-Org") != "org-1" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/projects":
			_, _ = w.Write([]byte(`{"data":[{"id":"p1","name":"storefront"}]}`))
		case "GET /api/v1/workspace/services":
			_, _ = w.Write([]byte(`{"data":[{"id":"s1","name":"api","project_id":"p1","environment_name":"production"},{"id":"s2","name":"api","project_id":"p1","environment_name":"staging"}]}`))
		case "POST /api/v1/services/s2/deployments":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["image_tag"] != "1.2.3" {
				t.Errorf("image_tag = %v", in["image_tag"])
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"data":{"id":"d1","service_id":"s2","status":"pending"}}`))
		case "GET /api/v1/services/s2/deployments/d1/logs/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: log\ndata: {\"message\":\"pulling 1.2.3\"}\n\nevent: done\ndata: {\"status\":\"running\"}\n\n"))
		case "POST /api/v1/services/s1/deployments":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":"the saved configuration breaks the deploy policy","detail":"the saved configuration breaks the deploy policy","code":"deploy_policy_violation","status":422,"data":{"code":"deploy_policy_violation","violations":[{"rule":"docker_socket","message":"mounts the Docker socket","field":"volumes","service":""}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	env := map[string]string{"TOFT_URL": srv.URL, "TOFT_API_KEY": "paas_k", "TOFT_ORG": "org-1"}
	getenv := func(k string) string { return env[k] }

	var out, errOut bytes.Buffer
	code := run([]string{"deploy", "storefront/staging/api", "--image", "1.2.3", "--wait"}, getenv, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	for _, want := range []string{"deployment d1 started (pending)", "pulling 1.2.3", "deployment d1: running"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	errOut.Reset()
	code = run([]string{"deploy", "storefront/production/api"}, getenv, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "deploy_policy_violation") || !strings.Contains(errOut.String(), "docker_socket: mounts the Docker socket") {
		t.Errorf("stderr = %s", errOut.String())
	}

	if code := run([]string{"deploy"}, getenv, &out, &errOut); code != 2 {
		t.Errorf("usage exit = %d", code)
	}
	if code := run([]string{"deploy", "x"}, func(string) string { return "" }, &out, &errOut); code != 2 {
		t.Errorf("missing config exit = %d", code)
	}
}
